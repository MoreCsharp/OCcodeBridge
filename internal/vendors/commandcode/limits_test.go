package commandcode

import (
	"testing"
	"time"

	"github.com/MoreCsharp/OCcodeBridge/internal/vendors"
)

func TestClassifyLimitErrorReportsUpgradeRequiredAsPermanent(t *testing.T) {
	cases := map[string]string{
		"openai envelope": `{"error":{"message":"Your plan does not include API access.","type":"permission_error","code":"upgrade_required"}}`,
		"cli envelope":    `{"success":false,"error":{"code":"upgrade_required","status":403,"message":"Upgrade required"}}`,
		"message only":    `{"error":{"message":"You're on the Go plan, the only plan without API access. Upgrade to GOAT or higher."}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			block := (Vendor{}).ClassifyLimitError(403, []byte(body), time.Now())
			if block == nil {
				t.Fatal("a plan without API access must be reported")
			}
			if !block.Permanent {
				t.Fatal("the Go plan limit cannot be cleared by polling usage")
			}
			if block.Reason != "Go 套餐无 API 权限，请升级到 GOAT 及以上" {
				t.Fatalf("reason = %q", block.Reason)
			}
		})
	}
}

func TestClassifyLimitErrorReportsRateLimitWindows(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		window string
	}{
		{
			name:   "five hour window",
			body:   `{"success":false,"error":{"code":"RATE_LIMITED","status":429,"message":"5-hour limit reached","rateLimit":{"window":"fiveHour","reset":1794993000}}}`,
			window: vendors.WindowFiveHour,
		},
		{
			name:   "weekly window",
			body:   `{"success":false,"error":{"code":"RATE_LIMITED","status":429,"message":"weekly limit reached","rateLimit":{"window":"weekly","reset":1794993000,"model":"deepseek/deepseek-v4-flash"}}}`,
			window: vendors.WindowWeekly,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			block := (Vendor{}).ClassifyLimitError(429, []byte(testCase.body), time.Now())
			if block == nil {
				t.Fatal("a window limit must be reported")
			}
			if block.Window != testCase.window {
				t.Fatalf("window = %q, want %q", block.Window, testCase.window)
			}
			if block.Permanent {
				t.Fatal("a rolling window is not permanent")
			}
			if block.ResetAt == nil || block.ResetAt.Unix() != 1794993000 {
				t.Fatalf("reset = %v, want the unix-second reset", block.ResetAt)
			}
		})
	}
}

func TestClassifyLimitErrorFallsBackToTheMessage(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		window string
		reset  bool
	}{
		{
			name:   "five hour message",
			body:   `{"error":{"type":"rate_limit_error","code":"RATE_LIMITED","message":"You've reached your 5-hour usage limit for your plan. Your limit resets at 2026-11-20T15:00:00Z."}}`,
			window: vendors.WindowFiveHour,
			reset:  true,
		},
		{
			name:   "weekly message",
			body:   `{"success":false,"error":{"code":"RATE_LIMITED","status":429,"message":"You've reached your weekly usage limit for your plan."}}`,
			window: vendors.WindowWeekly,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			block := (Vendor{}).ClassifyLimitError(429, []byte(testCase.body), time.Now())
			if block == nil {
				t.Fatal("a usage limit message must be reported")
			}
			if block.Window != testCase.window {
				t.Fatalf("window = %q, want %q", block.Window, testCase.window)
			}
			if testCase.reset {
				if block.ResetAt == nil || block.ResetAt.Format(time.RFC3339) != "2026-11-20T15:00:00Z" {
					t.Fatalf("reset = %v", block.ResetAt)
				}
			} else if block.ResetAt != nil {
				t.Fatalf("reset = %v, want unknown", block.ResetAt)
			}
		})
	}
}

func TestClassifyLimitErrorIgnoresOrdinaryFailures(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{
			name:   "upstream throttle",
			status: 429,
			body:   `{"error":{"message":"Rate-limited by the upstream. Retry with backoff.","type":"rate_limit_error","code":"rate_limit_error"}}`,
		},
		{
			name:   "region",
			status: 403,
			body:   `{"error":{"message":"The model isn't served in your region.","type":"permission_error","code":"model_region_unavailable"}}`,
		},
		{
			name:   "content policy",
			status: 403,
			body:   `{"error":{"message":"refused","type":"permission_error","code":"content_policy_refused"}}`,
		},
		{
			name:   "unreadable body",
			status: 429,
			body:   `not json`,
		},
		{
			name:   "missing model",
			status: 400,
			body:   `{"error":{"message":"unsupported_model","code":"unsupported_model"}}`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if block := (Vendor{}).ClassifyLimitError(testCase.status, []byte(testCase.body), time.Now()); block != nil {
				t.Fatalf("block = %+v, want nil", block)
			}
		})
	}
}

func TestClassifyLimitErrorUnrelatedUpgradeWordingIsNotPermanent(t *testing.T) {
	body := []byte(`{"error":{"type":"permission_error","code":"upstream_refused","message":"Provider refused; upgrade your client and retry"}}`)
	if block := (Vendor{}).ClassifyLimitError(403, body, time.Now()); block != nil {
		t.Fatalf("unrelated 403 must not pause the credential: %+v", block)
	}
}

// A "daily" rateLimit is a free model's per-day request cap, not a plan window:
// it must stay an ordinary rate limit and never pause the whole account.
func TestClassifyLimitErrorDailyRequestCapIsNotAPlanWindow(t *testing.T) {
	body := []byte(`{"error":{"type":"rate_limit_error","code":"RATE_LIMITED","message":"daily limit reached","rateLimit":{"window":"daily","reset":1794993000,"model":"glyph-cluster:free"}}}`)
	if block := (Vendor{}).ClassifyLimitError(429, body, time.Now()); block != nil {
		t.Fatalf("daily request cap must not pause the account: %+v", block)
	}
}
