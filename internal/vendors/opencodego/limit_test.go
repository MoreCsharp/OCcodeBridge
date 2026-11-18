package opencodego

import (
	"testing"
	"time"

	"github.com/xiao-qiu-qiu/PlanBridge/internal/vendors"
)

func TestClassifyLimitErrorRecognizesUsageWindows(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	ptr := func(at time.Time) *time.Time { return &at }
	cases := []struct {
		name       string
		status     int
		body       string
		wantWindow string
		wantReset  *time.Time
	}{
		{
			name:       "documented GoUsageLimitError with ISO reset",
			status:     429,
			body:       fixture(t, "limit_go_usage_window.json"),
			wantWindow: vendors.WindowFiveHour,
			wantReset:  ptr(time.Date(2026, 11, 18, 14, 30, 0, 0, time.UTC)),
		},
		{
			name:       "weekly wording with countdown",
			status:     429,
			body:       `{"error":{"message":"Your weekly usage limit is reached and it resets in 2h 30m."}}`,
			wantWindow: vendors.WindowWeekly,
			wantReset:  ptr(now.Add(2*time.Hour + 30*time.Minute)),
		},
		{
			name:       "monthly wording",
			status:     429,
			body:       `{"error":{"name":"GoUsageLimit","message":"Monthly usage limit reached for this key."}}`,
			wantWindow: vendors.WindowMonthly,
		},
		{
			name:       "structured window with retryAfter",
			status:     429,
			body:       `{"error":{"type":"GoUsageLimitError","window":"fiveHour","message":"go usage limit reached"},"retryAfter":120}`,
			wantWindow: vendors.WindowFiveHour,
			wantReset:  ptr(now.Add(2 * time.Minute)),
		},
		{
			name:       "snake_case window value",
			status:     429,
			body:       `{"error":{"type":"GoUsageLimitError","window":"five_hour"}}`,
			wantWindow: vendors.WindowFiveHour,
		},
		{
			name:       "snake_case type with unix millisecond reset",
			status:     429,
			body:       `{"error":{"type":"go_usage_limit","message":"go usage limit reached"},"resetsAt":1796083200000}`,
			wantWindow: "",
			wantReset:  ptr(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)),
		},
		{
			name:       "message without a type still names the window",
			status:     429,
			body:       `{"error":{"message":"5-hour limit reached, retry later"}}`,
			wantWindow: vendors.WindowFiveHour,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			block := Vendor{}.ClassifyLimitError(testCase.status, []byte(testCase.body), now)
			if block == nil {
				t.Fatal("a Go usage window limit must be classified")
			}
			if block.Window != testCase.wantWindow {
				t.Fatalf("window = %q, want %q", block.Window, testCase.wantWindow)
			}
			if testCase.wantReset == nil {
				if block.ResetAt != nil {
					t.Fatalf("reset = %s, want nil", block.ResetAt)
				}
			} else {
				assertTime(t, "reset", block.ResetAt, *testCase.wantReset)
			}
			if block.Reason == "" || block.Permanent {
				t.Fatalf("block = %#v, want a transient limit with a reason", block)
			}
		})
	}
}

func TestClassifyLimitErrorLeavesOrdinaryFailuresAlone(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"plain rate limit", 429, fixture(t, "limit_rate_limit.json")},
		{"rate limit with retryAfter only", 429, `{"error":{"type":"rate_limit_error","message":"Too many requests"},"retryAfter":30}`},
		{"unauthorized usage limit body", 401, fixture(t, "limit_go_usage_window.json")},
		{"forbidden usage limit body", 403, fixture(t, "limit_go_usage_window.json")},
		{"unparseable 429", 429, `<html><body>429</body></html>`},
		{"empty 429", 429, ""},
		{"usage limit on a different status", 400, fixture(t, "limit_go_usage_window.json")},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if block := (Vendor{}).ClassifyLimitError(testCase.status, []byte(testCase.body), now); block != nil {
				t.Fatalf("classified %#v, want nil so it stays a transient failure", block)
			}
		})
	}
}
