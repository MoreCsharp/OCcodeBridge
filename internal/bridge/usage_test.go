package bridge

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// usageTestService serves the fixture vendor's usage endpoint through CPA's host
// HTTP bridge, so the whole vendor→egress→DTO path is exercised.
func usageTestService(t *testing.T, respond func(map[string]any) (int, string)) *Service {
	t.Helper()
	s := registeredService(t, "")
	s.creds["one"] = Credential{ID: "one", Type: Provider, APIKey: "secret-test-key", Vendor: testVendorID}
	var mu sync.Mutex
	streams := map[string]string{}
	next := 0
	s.SetHost(func(method string, payload, out any) error {
		req := payload.(map[string]any)
		switch method {
		case "host.http.do_stream":
			if req["method"] != "GET" || req["host_callback_id"] != "usage-callback" {
				return fmt.Errorf("wrong method/context")
			}
			status, body := respond(req)
			mu.Lock()
			next++
			id := fmt.Sprint(next)
			streams[id] = body
			mu.Unlock()
			*out.(*upstreamStream) = upstreamStream{StatusCode: status, StreamID: id}
		case "host.http.stream_read":
			mu.Lock()
			body := streams[str(req["stream_id"])]
			mu.Unlock()
			*out.(*readChunk) = readChunk{Payload: []byte(body), Done: true}
		case "host.http.stream_close":
			mu.Lock()
			delete(streams, str(req["stream_id"]))
			mu.Unlock()
		default:
			return fmt.Errorf("unexpected callback %s", method)
		}
		return nil
	})
	return s
}

func usageTestRequest() ManagementRequest {
	return ManagementRequest{Method: "GET", Path: apiBase + "/credentials/usage", Query: url.Values{"id": {"one"}}, HostCallbackID: "usage-callback"}
}

const testLimits = `{"accountId":"private-user","planName":"Fixture Plan","balance":12.5,"windows":[{"kind":"5h","percent":0,"resetAt":"2026-09-25T11:21:35.329131025Z"},{"kind":"7d","percent":11},{"kind":"unsupported","percent":101.5}]}`

func TestCredentialUsageCacheErrorsAndKeyReplacement(t *testing.T) {
	calls, status := 0, 200
	s := usageTestService(t, func(req map[string]any) (int, string) {
		calls++
		key := req["headers"].(http.Header).Get("Authorization")
		if key != "Bearer secret-test-key" && key != "Bearer replacement-key" {
			t.Errorf("credential header missing")
		}
		if status != 200 {
			return status, `{"error":{"message":"secret-test-key private-error"}}`
		}
		return 200, testLimits
	})
	req := usageTestRequest()
	raw, err := s.management(jsonBytes(req))
	if err != nil {
		t.Fatal(err)
	}
	response := raw.(ManagementResponse)
	var first credentialUsage
	if response.StatusCode != 200 || json.Unmarshal(response.Body, &first) != nil || first.Status != "ok" || first.Plan == nil || first.Plan.Name != "Fixture Plan" || first.Vendor != testVendorID || len(first.Limits) != 2 || *first.Limits[0].PercentUsed != 0 || first.Limits[0].ResetsAt == nil || first.Balance == nil || *first.Balance != 12.5 {
		t.Fatalf("bad usage response: %s", response.Body)
	}
	for _, secret := range []string{"secret-test-key", "private-user"} {
		if strings.Contains(string(response.Body), secret) {
			t.Fatal("private field exposed")
		}
	}
	req.Query.Set("refresh", "1")
	s.credentialUsage(req)
	if calls != 1 {
		t.Fatalf("a request inside the short cache window re-queried: %d", calls)
	}
	past := time.Now().Add(-2 * time.Minute)
	s.usageCache["one"].value.CheckedAt = &past
	status = 429
	failed, _ := s.credentialUsage(req)
	if failed.Status != "rate_limited" || failed.UpdatedAt == nil || !failed.UpdatedAt.Equal(*first.UpdatedAt) || len(failed.Limits) != 2 || calls != 2 || strings.Contains(failed.Error, "secret") {
		t.Fatalf("lost stale data or leaked upstream error: %+v", failed)
	}
	status = 200
	c := s.creds["one"]
	c.APIKey = "replacement-key"
	s.creds["one"] = c
	replaced, _ := s.credentialUsage(req)
	if replaced.Status != "ok" || calls != 3 {
		t.Fatalf("old key cache reused: %+v, calls %d", replaced, calls)
	}
	c.Disabled = true
	s.creds["one"] = c
	disabled, _ := s.credentialUsage(req)
	if disabled.Status != "ok" || calls != 3 || !disabled.UpdatedAt.Equal(*replaced.UpdatedAt) || !s.creds["one"].Disabled {
		t.Fatal("manual disable discarded usage or changed the manual flag")
	}
	c.Disabled = false
	c.ProxyURL = "http://fixture-user:fixture-password@127.0.0.1:1"
	s.creds["one"] = c
	proxy, _ := s.credentialUsage(req)
	if proxy.Status != "unavailable" || calls != 3 {
		t.Fatal("credential proxy silently bypassed")
	}
}

func TestCredentialUsageDisabledQuotaRecoveryPreservesManualState(t *testing.T) {
	percent, calls := 100, 0
	s := usageTestService(t, func(map[string]any) (int, string) {
		calls++
		return 200, fmt.Sprintf(`{"accountId":"acct","planName":"Fixture Plan","windows":[{"kind":"7d","percent":%d}]}`, percent)
	})
	c := s.creds["one"]
	c.Disabled = true
	quotaTestAuth(t, s, c)
	query := func() credentialUsage {
		t.Helper()
		raw, err := s.management(jsonBytes(usageTestRequest()))
		if err != nil {
			t.Fatal(err)
		}
		response := raw.(ManagementResponse)
		var value credentialUsage
		if response.StatusCode != 200 || json.Unmarshal(response.Body, &value) != nil || value.Status != "ok" {
			t.Fatalf("manual-disabled account usage query failed: %s", response.Body)
		}
		return value
	}
	first := query()
	if calls != 1 || *first.Limits[0].PercentUsed != 100 || s.creds[c.ID].QuotaBlock == nil || !s.creds[c.ID].Disabled {
		t.Fatal("disabled account did not fetch current usage and persist quota protection")
	}
	percent = 3
	past := time.Now().Add(-2 * usageTTL)
	s.usageCache[c.ID].value.CheckedAt = &past
	recovered := query()
	if calls != 2 || *recovered.Limits[0].PercentUsed != 3 || s.creds[c.ID].QuotaBlock != nil || !s.creds[c.ID].Disabled {
		t.Fatal("fresh usage recovery changed manual disable or left stale quota state")
	}
	status := s.quotaStatus(c.ID)
	if status["manual_disabled"] != true || status["quota_paused"] != false || status["effective_disabled"] != true {
		t.Fatal("recovered account lost its independent manual disable")
	}
	if quotaTestParse(t, NewService(), c.ID, quotaTestRead(t, s, c.ID))["Disabled"] != true {
		t.Fatal("quota recovery enabled the physical credential or reloaded CPA auth")
	}
	if _, err := s.selectedCredential(ExecutorRequest{StorageJSON: jsonBytes(c)}); statusOf(err) != 401 {
		t.Fatalf("usage queries enabled manual-disabled inference: %v", err)
	}
}

func TestCredentialUsageConcurrentReaders(t *testing.T) {
	var calls atomic.Int32
	s := usageTestService(t, func(map[string]any) (int, string) {
		calls.Add(1)
		return 200, testLimits
	})
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := s.credentialUsage(usageTestRequest())
			if err != nil || value.Status != "ok" {
				t.Errorf("concurrent query: %v %s", err, value.Status)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("duplicate upstream fetches: %d", calls.Load())
	}
}

func TestCredentialUsageRejectsOldInFlightKey(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	s := usageTestService(t, func(req map[string]any) (int, string) {
		if req["headers"].(http.Header).Get("Authorization") == "Bearer secret-test-key" {
			close(started)
			<-release
		}
		return 200, testLimits
	})
	done := make(chan error, 1)
	go func() { _, err := s.credentialUsage(usageTestRequest()); done <- err }()
	<-started
	s.mu.Lock()
	c := s.creds["one"]
	c.APIKey = "replacement-key"
	s.creds["one"] = c
	s.mu.Unlock()
	fresh, err := s.credentialUsage(usageTestRequest())
	if err != nil || fresh.Status != "ok" {
		t.Fatalf("replacement query failed: %v", err)
	}
	close(release)
	if err := <-done; statusOf(err) != 409 {
		t.Fatalf("old credential response accepted: %v", err)
	}
	current, _ := s.credentialUsage(usageTestRequest())
	if !current.UpdatedAt.Equal(*fresh.UpdatedAt) {
		t.Fatal("old in-flight query overwrote new cache")
	}
}

func TestCredentialUsageUnknownWindowsStayUnknown(t *testing.T) {
	s := usageTestService(t, func(map[string]any) (int, string) {
		return 200, `{"windows":[{"kind":"7d"},{"kind":"unmapped","percent":5},{"kind":"5h","percent":-4}]}`
	})
	value, err := s.credentialUsage(usageTestRequest())
	if err != nil || value.Status != "ok" {
		t.Fatalf("unexpected response: %+v, %v", value, err)
	}
	if len(value.Limits) != 2 || value.Limits[0].Type != "weekly" || value.Limits[0].PercentUsed != nil || value.Limits[1].Type != "five_hour" || value.Limits[1].PercentUsed != nil {
		t.Fatalf("missing or negative usage was fabricated: %+v", value.Limits)
	}
}

// A credential without a vendor cannot be scheduled, so its usage query must say
// so instead of guessing an upstream.
func TestCredentialUsageRequiresVendor(t *testing.T) {
	s := usageTestService(t, func(map[string]any) (int, string) {
		t.Error("a credential without a vendor reached the upstream")
		return 200, testLimits
	})
	s.mu.Lock()
	c := s.creds["one"]
	c.Vendor = ""
	s.creds["one"] = c
	s.mu.Unlock()
	value, err := s.credentialUsage(usageTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	if value.Status != "unavailable" || !strings.Contains(value.Error, "厂商") || value.Limits == nil {
		t.Fatalf("vendor-less credential query = %+v", value)
	}
}
