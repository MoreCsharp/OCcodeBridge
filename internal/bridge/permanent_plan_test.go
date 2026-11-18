package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/xiao-qiu-qiu/OCcodeBridge/internal/vendors"
)

const planlessVendorID = "planless"

// planlessVendor is a plan without API access: its usage call is rejected with a
// 403 carrying the vendor's own reason, which is the case a plan-only subscription
// produces. The vendor keeps the body on the error so the bridge can reuse
// ClassifyLimitError instead of guessing from the status alone.
type planlessVendor struct{}

func init() { vendors.Register(planlessVendor{}) }

func (planlessVendor) ID() string          { return planlessVendorID }
func (planlessVendor) DisplayName() string { return "Planless Vendor" }
func (planlessVendor) BaseURL() string     { return "https://planless.invalid/v1" }
func (planlessVendor) Headers(vendors.Request) http.Header {
	return http.Header{}
}
func (planlessVendor) ListModels(context.Context, vendors.Credential, vendors.HTTPDoer) ([]vendors.Model, error) {
	return nil, vendors.ErrNotImplemented
}
func (planlessVendor) FetchUsage(context.Context, vendors.Credential, vendors.HTTPDoer) (vendors.Usage, error) {
	return vendors.Usage{}, fmt.Errorf("planless: usage: %w", &planlessError{status: 403, body: []byte(`{"code":"upgrade_required","message":"go plan has no api access"}`)})
}
func (planlessVendor) ClassifyLimitError(status int, body []byte, _ time.Time) *vendors.LimitBlock {
	if status != http.StatusForbidden || !strings.Contains(string(body), "upgrade_required") {
		return nil
	}
	return &vendors.LimitBlock{Permanent: true, Reason: "套餐不支持 API 访问：请升级套餐"}
}
func (planlessVendor) ObserveResponse(map[string]any) *vendors.Observation { return nil }

type planlessError struct {
	status int
	body   []byte
}

func (e *planlessError) Error() string        { return "planless: management call rejected" }
func (e *planlessError) StatusCode() int      { return e.status }
func (e *planlessError) ResponseBody() []byte { return e.body }

var _ vendors.StatusCoder = (*planlessError)(nil)

func permanentPlanError(reason string) *upstreamError {
	return &upstreamError{APIError: &APIError{403, "upstream_plan_unsupported", reason}, HTTPStatus: 403, UpstreamStatus: 403, Scope: "account"}
}

func TestPermanentPlanBlockPersistsAcrossRestart(t *testing.T) {
	s := NewService()
	c := Credential{ID: "one", APIKey: "fixture-plan-key", Vendor: testVendorID}
	quotaTestAuth(t, s, c)
	if err := s.observeQuotaError(c, permanentPlanError("go plan has no api access")); err != nil {
		t.Fatal(err)
	}
	block := s.creds[c.ID].QuotaBlock
	if block == nil || !block.Permanent || block.RetryAt != nil || len(block.Limits) != 1 || block.Limits[0].Type != quotaPlanLimit {
		t.Fatalf("permanent block not recorded: %+v", block)
	}
	if !strings.Contains(block.Reason, "api access") {
		t.Fatalf("vendor reason lost: %q", block.Reason)
	}
	status := s.quotaStatus(c.ID)
	if status["quota_paused"] != true || status["plan_unsupported"] != true || status["quota_reason"] == "" {
		t.Fatalf("console state does not expose the permanent reason: %+v", status)
	}
	if _, err := s.selectedCredential(ExecutorRequest{StorageJSON: jsonBytes(c)}); statusOf(err) != 429 {
		t.Fatalf("a plan without API access was scheduled: %v", err)
	} else {
		var local *upstreamError
		if !errors.As(err, &local) || !strings.Contains(local.Message, "plan does not provide API access") {
			t.Fatalf("scheduling refusal must name the permanent state: %v", err)
		}
	}
	persisted := string(quotaTestRead(t, s, c.ID))
	if !bytes.Contains([]byte(persisted), []byte(`"permanent": true`)) {
		t.Fatalf("permanent state was not persisted: %s", persisted)
	}
	// A restart re-reads the auth file: the state must survive without polling.
	reloaded := NewService()
	if auth := quotaTestParse(t, reloaded, c.ID, []byte(persisted)); auth["Disabled"] != true {
		t.Fatalf("reloaded credential is still scheduled: %v", auth)
	}
	if reloaded.creds[c.ID].QuotaBlock == nil || !reloaded.creds[c.ID].QuotaBlock.Permanent {
		t.Fatalf("reloaded credential lost the permanent block: %+v", reloaded.creds[c.ID].QuotaBlock)
	}
}

func TestPermanentPlanBlockIsNotClearedByUsagePolling(t *testing.T) {
	s := usageTestService(t, func(map[string]any) (int, string) {
		return 200, `{"accountId":"acct","planName":"Fixture Plan","windows":[{"kind":"7d","percent":3}]}`
	})
	c := s.creds["one"]
	quotaTestAuth(t, s, c)
	if err := s.observeQuotaError(c, permanentPlanError("go plan has no api access")); err != nil {
		t.Fatal(err)
	}
	blocked := s.creds[c.ID].QuotaBlock
	if blocked == nil || !blocked.Permanent {
		t.Fatal("permanent block missing")
	}
	observed := blocked.ObservedAt
	room := quotaTestRoom("acct", observed.Add(time.Second))
	if err := s.observeQuotaUsage(s.creds[c.ID], room, observed.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if s.creds[c.ID].QuotaBlock == nil || !s.creds[c.ID].QuotaBlock.Permanent {
		t.Fatal("a healthy usage sample cleared a plan that has no API access")
	}
	value, err := s.credentialUsage(usageTestRequest())
	if err != nil || value.Status != "ok" || s.creds[c.ID].QuotaBlock == nil || !s.creds[c.ID].QuotaBlock.Permanent {
		t.Fatalf("a healthy usage query cleared the permanent state: %+v %v", value.Status, err)
	}
}

func TestUsagePollingDetectsPlanWithoutAPIAccess(t *testing.T) {
	s := NewService()
	c := Credential{ID: "one", Type: Provider, APIKey: "fixture-plan-key", Vendor: planlessVendorID}
	s.creds[c.ID] = c
	quotaTestAuth(t, s, c)
	value, err := s.credentialUsage(usageTestRequest())
	if err != nil || value.Status != "plan_unsupported" || !strings.Contains(value.Error, "升级套餐") {
		t.Fatalf("polling did not report the plan limitation: %+v %v", value, err)
	}
	block := s.creds[c.ID].QuotaBlock
	if block == nil || !block.Permanent || block.Source != "usage" {
		t.Fatalf("polling did not enter the permanent state: %+v", block)
	}
	if quotaTestParse(t, s, c.ID, quotaTestRead(t, s, c.ID))["Disabled"] != true {
		t.Fatal("the permanent state did not stop scheduling")
	}
}

func TestRecheckClearsPermanentPlanState(t *testing.T) {
	for _, healthy := range []bool{true, false} {
		t.Run(fmt.Sprint("healthy=", healthy), func(t *testing.T) {
			upstreamOK, calls := false, 0
			s := usageTestService(t, func(map[string]any) (int, string) {
				calls++
				if !upstreamOK {
					return 403, `{"error":{"message":"still no api access"}}`
				}
				return 200, `{"planName":"Fixture Plan","windows":[{"kind":"7d","percent":3}]}`
			})
			c := s.creds["one"]
			quotaTestAuth(t, s, c)
			if err := s.observeQuotaError(c, permanentPlanError("go plan has no api access")); err != nil {
				t.Fatal(err)
			}
			// Prime the cache with a failed sample: an explicit re-check must not
			// reuse it even though its short TTL has not expired.
			if _, err := s.credentialUsage(usageTestRequest()); err != nil {
				t.Fatal(err)
			}
			primed := calls
			upstreamOK = healthy
			raw, err := s.management(jsonBytes(ManagementRequest{Method: "POST", Path: apiBase + "/credentials/recheck", Body: jsonBytes(map[string]any{"id": c.ID}), HostCallbackID: "usage-callback"}))
			if err != nil {
				t.Fatal(err)
			}
			response := raw.(ManagementResponse)
			var value struct {
				Cleared bool   `json:"cleared"`
				Status  string `json:"status"`
				Error   string `json:"error"`
			}
			if response.StatusCode != 200 || json.Unmarshal(response.Body, &value) != nil {
				t.Fatalf("recheck failed: %s", response.Body)
			}
			if calls <= primed {
				t.Fatalf("recheck reused the cached sample: %d calls", calls)
			}
			if value.Cleared != healthy {
				t.Fatalf("cleared = %v, want %v: %s", value.Cleared, healthy, response.Body)
			}
			if healthy {
				if s.creds[c.ID].QuotaBlock != nil {
					t.Fatalf("recheck kept the permanent state: %+v", s.creds[c.ID].QuotaBlock)
				}
				if strings.Contains(string(quotaTestRead(t, s, c.ID)), `"quota_block"`) {
					t.Fatal("recheck did not persist the cleared state")
				}
				if quotaTestParse(t, s, c.ID, quotaTestRead(t, s, c.ID))["Disabled"] != false {
					t.Fatal("recheck did not re-enable scheduling")
				}
			} else if s.creds[c.ID].QuotaBlock == nil || !s.creds[c.ID].QuotaBlock.Permanent {
				t.Fatalf("a failed recheck cleared the permanent state: %+v", s.creds[c.ID].QuotaBlock)
			}
		})
	}
}

func TestReplacingKeyClearsPermanentPlanState(t *testing.T) {
	s := NewService()
	c := Credential{ID: "one", APIKey: "fixture-old-key", Vendor: testVendorID}
	quotaTestAuth(t, s, c)
	if err := s.observeQuotaError(c, permanentPlanError("go plan has no api access")); err != nil {
		t.Fatal(err)
	}
	raw, err := s.management(jsonBytes(ManagementRequest{Method: "PUT", Path: apiBase + "/credentials", Query: url.Values{"id": {c.ID}}, Body: jsonBytes(map[string]any{"api_key": "fixture-replaced-key"})}))
	if err != nil {
		t.Fatal(err)
	}
	if response := raw.(ManagementResponse); response.StatusCode != 200 {
		t.Fatalf("key replacement failed: %s", response.Body)
	}
	if s.creds[c.ID].APIKey != "fixture-replaced-key" || s.creds[c.ID].QuotaBlock != nil || s.creds[c.ID].QuotaIdentity != nil {
		t.Fatalf("new key inherited the old plan state: %+v", s.creds[c.ID])
	}
	if strings.Contains(string(quotaTestRead(t, s, c.ID)), `"quota_block"`) {
		t.Fatal("the replaced key file still carries the permanent block")
	}
	if auth := quotaTestParse(t, s, c.ID, quotaTestRead(t, s, c.ID)); auth["Disabled"] != false {
		t.Fatalf("the replaced key is still disabled: %v", auth)
	}
}
