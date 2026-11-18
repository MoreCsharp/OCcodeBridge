package bridge

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strings"
	"testing"
	"time"
)

func quotaProviderRequest(authID string) json.RawMessage {
	return jsonBytes(map[string]any{"auth_index": "fixture-index", "auth_id": authID, "provider": Provider, "host_callback_id": "usage-callback"})
}

func TestQuotaProviderRegistration(t *testing.T) {
	s := registeredService(t, "")
	raw := jsonBytes(map[string]any{"config_yaml": []byte(fmt.Sprintf("data_dir: %q\n", s.config().DataDir))})
	for _, method := range []string{"plugin.register", "plugin.reconfigure"} {
		result, err := s.Handle(method, raw)
		if err != nil {
			t.Fatal(err)
		}
		if result.(map[string]any)["capabilities"].(map[string]any)["quota_provider"] != true {
			t.Fatalf("%s does not advertise the native quota capability", method)
		}
	}
	identifier, err := s.Handle("quota.identifier", nil)
	if err != nil || identifier.(map[string]any)["identifier"] != Provider {
		t.Fatalf("quota identifier: %v, %v", identifier, err)
	}
	description, err := s.Handle("quota.describe", nil)
	var decoded struct {
		SupportedProviders []string `json:"supported_providers"`
		SupportsReset      bool     `json:"supports_reset"`
	}
	if err != nil || json.Unmarshal(jsonBytes(description), &decoded) != nil || len(decoded.SupportedProviders) != 1 || decoded.SupportedProviders[0] != Provider || decoded.SupportsReset {
		t.Fatalf("quota description: %v, %v", description, err)
	}
	reset, err := s.Handle("quota.reset", quotaProviderRequest("one.json"))
	if err != nil || reset.(map[string]any)["success"] != false {
		t.Fatalf("upstream quota reset must be rejected: %v, %v", reset, err)
	}
}

func TestQuotaProviderFetchSharesUsageAndPreservesDisabledAccount(t *testing.T) {
	calls := 0
	s := usageTestService(t, func(map[string]any) (int, string) {
		calls++
		return 200, `{"accountId":"acct","planName":"Fixture Plan","windows":[{"kind":"5h","percent":0,"resetAt":"2026-10-18T12:00:00.123456789Z"},{"kind":"7d","percent":25},{"kind":"month","percent":110},{"kind":"unsupported","percent":null}]}`
	})
	c := s.creds["one"]
	c.Disabled = true
	quotaTestAuth(t, s, c)
	result, err := s.Handle("quota.fetch", quotaProviderRequest("one.json"))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Subscription struct{ Plan string } `json:"subscription"`
		Groups       []struct {
			Buckets []struct {
				Window            string  `json:"window"`
				RemainingFraction float64 `json:"remainingFraction"`
				ResetTime         string  `json:"resetTime"`
			} `json:"buckets"`
		} `json:"groups"`
	}
	body := jsonBytes(result)
	if json.Unmarshal(body, &response) != nil || len(response.Groups) != 1 || len(response.Groups[0].Buckets) != 3 || response.Subscription.Plan != "Fixture Plan" {
		t.Fatalf("invalid normalized quota: %s", body)
	}
	buckets := response.Groups[0].Buckets
	if buckets[0].Window != "5h" || buckets[0].RemainingFraction != 1 || buckets[0].ResetTime != "2026-10-18T12:00:00.123456789Z" || buckets[1].Window != "7d" || buckets[1].RemainingFraction != .75 || buckets[2].RemainingFraction != 0 {
		t.Fatalf("quota windows lost their units or zero/overflow values: %s", body)
	}
	for _, secret := range []string{"secret-test-key", "private-user", "private-subscription", "account_hash", "estimate", "auth_index"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("quota exposed a private or non-quota field: %s", secret)
		}
	}
	if _, err := s.Handle("quota.fetch", quotaProviderRequest("one.json")); err != nil {
		t.Fatal(err)
	}
	value, err := s.credentialUsage(ManagementRequest{Query: url.Values{"id": {"one"}}, HostCallbackID: "usage-callback"})
	if err != nil || value.Status != "ok" || calls != 1 || !s.creds["one"].Disabled {
		t.Fatalf("quota bypassed shared usage or changed manual state: calls=%d err=%v", calls, err)
	}
}

func TestQuotaProviderRejectsUnrelatedAndRevokedCredentials(t *testing.T) {
	for _, tc := range []struct {
		name, provider, authID string
		revoked, rotated       bool
		wantStatus             int
	}{
		{"different provider", "antigravity", "one.json", false, false, 400},
		{"missing identity", Provider, "", false, false, 400},
		{"unknown auth", Provider, "other.json", false, false, 404},
		{"removed auth", Provider, "one.json", true, false, 404},
		{"old routing identity", Provider, "one.json", false, true, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := usageTestService(t, func(map[string]any) (int, string) {
				t.Error("invalid quota target reached upstream")
				return 200, testLimits
			})
			s.revoked["one"] = tc.revoked
			if tc.rotated {
				c := s.creds["one"]
				c.RoutingID = "rotated-auth"
				s.creds["one"] = c
			}
			_, err := s.Handle("quota.fetch", jsonBytes(map[string]any{"provider": tc.provider, "auth_id": tc.authID}))
			if statusOf(err) != tc.wantStatus {
				t.Fatalf("invalid quota target: %v", err)
			}
		})
	}
}

// The amount pair a vendor reports is exact, so it outranks its percentage; the
// month window keeps the contract's own spelling, and a kind that is not a plan
// window (such as a per-day request cap) never becomes a bucket.
func TestQuotaProviderPrefersReportedAmountsAndMapsEveryWindow(t *testing.T) {
	s := usageTestService(t, func(map[string]any) (int, string) {
		return 200, `{"planName":"Fixture Plan","windows":[
			{"kind":"5h","percent":50,"used":9.5,"cap":10},
			{"kind":"7d"},
			{"kind":"daily","percent":40},
			{"kind":"month","used":3,"cap":4},
			{"kind":"month","percent":-1}
		]}`
	})
	result, err := s.Handle("quota.fetch", quotaProviderRequest("one.json"))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Groups []struct {
			Buckets []struct {
				Window            string  `json:"window"`
				RemainingFraction float64 `json:"remainingFraction"`
			} `json:"buckets"`
		} `json:"groups"`
	}
	if json.Unmarshal(jsonBytes(result), &response) != nil || len(response.Groups) != 1 {
		t.Fatalf("invalid normalized quota: %s", jsonBytes(result))
	}
	buckets := response.Groups[0].Buckets
	want := []struct {
		window    string
		remaining float64
	}{
		{"5h", 0.05},      // used/cap wins over the 50% percentage
		{"monthly", 0.25}, // used/cap only
	}
	if len(buckets) != len(want) {
		t.Fatalf("unexpected buckets: %s", jsonBytes(result))
	}
	for i, expected := range want {
		if buckets[i].Window != expected.window || math.Abs(buckets[i].RemainingFraction-expected.remaining) > 1e-9 {
			t.Fatalf("bucket %d = %s / %v, want %s / %v", i, buckets[i].Window, buckets[i].RemainingFraction, expected.window, expected.remaining)
		}
	}
}

func TestQuotaProviderOmitsPlanWhenVendorReportsNone(t *testing.T) {
	s := usageTestService(t, func(map[string]any) (int, string) {
		return 200, `{"windows":[{"kind":"7d","percent":25}]}`
	})
	result, err := s.Handle("quota.fetch", quotaProviderRequest("one.json"))
	var response map[string]json.RawMessage
	if err != nil || json.Unmarshal(jsonBytes(result), &response) != nil || response["subscription"] != nil || response["groups"] == nil {
		t.Fatalf("valid windows lost or an empty plan published: %v, %v", result, err)
	}
}

func TestQuotaProviderReportsFailureInsteadOfStaleOrUnknownQuota(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   int
	}{
		{"unauthorized", 401, `{}`, 401},
		{"rate limited", 429, `{}`, 429},
		{"timeout", 504, `{}`, 504},
		{"upstream failure", 500, `{}`, 502},
		{"invalid response", 200, `not-json`, 502},
		{"no windows", 200, `{"windows":[]}`, 502},
		{"unknown usage", 200, `{"windows":[{"kind":"5h"}]}`, 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := usageTestService(t, func(map[string]any) (int, string) { return tc.status, tc.body })
			past := time.Now().Add(-2 * time.Minute)
			percent := 5.0
			s.usageCache["one"] = &usageCacheEntry{fingerprint: usageFingerprint(s.creds["one"]), value: credentialUsage{
				Status: "ok", CheckedAt: &past, UpdatedAt: &past, Limits: []usageLimit{{Type: "weekly", PercentUsed: &percent}},
			}}
			if _, err := s.Handle("quota.fetch", quotaProviderRequest("one.json")); statusOf(err) != tc.want {
				t.Fatalf("stale or unknown quota reported as current: %v", err)
			}
		})
	}
}
