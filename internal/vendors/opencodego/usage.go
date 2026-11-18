package opencodego

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xiao-qiu-qiu/OCcodeBridge/internal/vendors"
)

// usageEndpoint is the console usage call. It deliberately does not sit on
// BaseURL: the usage API lives on the zen path, not on the inference base. It is
// a variable so tests can point it at a local server.
var usageEndpoint = "https://opencode.ai/zen/go/v1/usage"

// microCentsPerUnit converts the console's micro-cents (1e-8 USD) to USD.
const microCentsPerUnit = 1e8

// errUsageUnrecognised reports a usage payload whose windows could not be read.
// It is an error rather than an empty sample so the console keeps showing "unknown"
// instead of an account with no limits.
var errUsageUnrecognised = errors.New("opencode-go: usage response unrecognised")

// apiKeyWindowKeys maps the API-key usage shape onto shared window kinds
// (spec §1: {"usage":{"rolling","weekly","monthly"}}).
var apiKeyWindowKeys = [...]struct{ key, kind string }{
	{"rolling", vendors.WindowFiveHour},
	{"weekly", vendors.WindowWeekly},
	{"monthly", vendors.WindowMonthly},
}

// consoleMeterKeys maps the legacy console Go page's meters onto shared window
// kinds. The names come from the community usage extension's WINDOW_KEYS: the
// console HTML carries no numbers, it loads them from /console/api/go/status. The
// month meter has no window of its own, so its reset is the paid period end.
var consoleMeterKeys = [...]struct {
	key       string
	kind      string
	periodEnd bool
}{
	{"fiveHour", vendors.WindowFiveHour, false},
	{"week", vendors.WindowWeekly, false},
	{"month", vendors.WindowMonthly, true},
}

// FetchUsage reads one usage sample. Both the API-key shape and the legacy
// console shape are accepted; missing windows and missing fields stay absent/nil.
func (Vendor) FetchUsage(ctx context.Context, c vendors.Credential, doer vendors.HTTPDoer) (vendors.Usage, error) {
	if doer == nil {
		return vendors.Usage{}, errors.New("opencode-go: FetchUsage needs an HTTP doer")
	}
	request, err := newRequest(ctx, usageEndpoint, c)
	if err != nil {
		return vendors.Usage{}, err
	}
	response, err := doer.Do(request)
	if err != nil {
		return vendors.Usage{}, fmt.Errorf("opencode-go: usage request failed: %w", err)
	}
	body, err := readBody(response)
	if err != nil {
		return vendors.Usage{}, fmt.Errorf("opencode-go: usage response unreadable: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return vendors.Usage{}, &upstreamStatusError{op: "usage", status: response.StatusCode, detail: bodySnippet(body, c.APIKey)}
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return vendors.Usage{}, fmt.Errorf("opencode-go: usage response is not valid JSON: %w", err)
	}
	now := time.Now().UTC()
	windows, periodEnd := parseUsage(payload, now)
	if len(windows) == 0 {
		return vendors.Usage{}, errUsageUnrecognised
	}
	planName, accountID := planAndAccount(payload, c.APIKey)
	return vendors.Usage{
		PlanName:  planName,
		PeriodEnd: periodEnd,
		Windows:   windows,
		FetchedAt: now,
		AccountID: accountID,
	}, nil
}

// parseUsage prefers the API-key shape and falls back to the console shape. It
// also returns the paid period end when the console reports one.
func parseUsage(payload map[string]any, now time.Time) ([]vendors.Window, *time.Time) {
	if usage := object(payload["usage"]); usage != nil {
		if windows := apiKeyWindows(usage, now); len(windows) > 0 {
			return windows, nil
		}
	}
	access := object(payload["access"])
	if access == nil {
		return nil, nil
	}
	var periodEnd *time.Time
	if at, ok := coerceTime(access["endsAt"]); ok {
		periodEnd = &at
	}
	meters := object(access["meters"])
	if meters == nil {
		return nil, periodEnd
	}
	return consoleWindows(meters, periodEnd, now), periodEnd
}

// apiKeyWindows reads {"rolling","weekly","monthly"} meters.
func apiKeyWindows(usage map[string]any, now time.Time) []vendors.Window {
	windows := make([]vendors.Window, 0, len(apiKeyWindowKeys))
	for _, spec := range apiKeyWindowKeys {
		meter := object(usage[spec.key])
		if meter == nil {
			continue
		}
		window := vendors.Window{Kind: spec.kind}
		if percent, ok := firstNumber(meter, "percent", "percentUsed", "usedPercent"); ok {
			window.UsedPercent = clampPercent(percent)
		}
		window.Used, window.Cap = meterAmounts(meter)
		if window.UsedPercent == nil && window.Used != nil && window.Cap != nil && *window.Cap > 0 {
			window.UsedPercent = clampPercent(*window.Used / *window.Cap * 100)
		}
		if at, ok := firstTime(meter, "resetsAt", "resetAt", "reset", "resets_at", "reset_at"); ok {
			window.ResetAt = &at
		}
		window.Status = firstText(meter, "status")
		// The weekly window resets on Monday 00:00 UTC whether or not the upstream
		// says so, so a missing reset time is derived and marked.
		if spec.kind == vendors.WindowWeekly && window.ResetAt == nil {
			at := nextWeeklyReset(now)
			window.ResetAt = &at
			window.Status = derivedStatus(window.Status)
		}
		if !windowReported(window) {
			continue
		}
		windows = append(windows, window)
	}
	return windows
}

// consoleWindows reads the legacy console {"access":{"meters":{...}}} shape.
func consoleWindows(meters map[string]any, periodEnd *time.Time, now time.Time) []vendors.Window {
	windows := make([]vendors.Window, 0, len(consoleMeterKeys))
	for _, spec := range consoleMeterKeys {
		meter := object(meters[spec.key])
		if meter == nil {
			continue
		}
		window := vendors.Window{Kind: spec.kind}
		window.Used, window.Cap = meterAmounts(meter)
		if window.Used != nil && window.Cap != nil && *window.Cap > 0 {
			window.UsedPercent = clampPercent(*window.Used / *window.Cap * 100)
		}
		if percent, ok := firstNumber(meter, "percent", "percentUsed"); ok {
			window.UsedPercent = clampPercent(percent)
		}
		if at, ok := firstTime(meter, "resetsAt", "resetAt", "resets_at", "reset_at"); ok {
			window.ResetAt = &at
		}
		window.Status = firstText(meter, "status")
		if spec.periodEnd && window.ResetAt == nil && periodEnd != nil {
			window.ResetAt = periodEnd
		}
		if spec.kind == vendors.WindowWeekly && window.ResetAt == nil {
			at := nextWeeklyReset(now)
			window.ResetAt = &at
			window.Status = derivedStatus(window.Status)
		}
		if !windowReported(window) {
			continue
		}
		windows = append(windows, window)
	}
	return windows
}

// meterAmounts reads the money fields of one window, preferring the console's
// micro-cents (1e-8 USD) over plain amounts when both are present.
func meterAmounts(meter map[string]any) (used, cap *float64) {
	if value, ok := firstNumber(meter, "usedMicroCents"); ok {
		used = floatPtr(value / microCentsPerUnit)
	} else if value, ok := firstNumber(meter, "used"); ok {
		used = floatPtr(value)
	}
	if value, ok := firstNumber(meter, "limitMicroCents", "capMicroCents"); ok {
		cap = floatPtr(value / microCentsPerUnit)
	} else if value, ok := firstNumber(meter, "limit", "cap"); ok {
		cap = floatPtr(value)
	}
	return used, cap
}

// windowReported reports whether a meter carried anything but its kind, so an
// empty meter object never becomes a "0% used" window.
func windowReported(window vendors.Window) bool {
	return window.UsedPercent != nil || window.Used != nil || window.Cap != nil || window.ResetAt != nil || window.Status != ""
}

// nextWeeklyReset is the next Monday 00:00 UTC, which is when the Go weekly
// window resets (opencode.ai/docs/console/go: "It resets on Monday at 00:00 UTC").
func nextWeeklyReset(now time.Time) time.Time {
	utc := now.UTC()
	midnight := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	daysAhead := (8 - int(utc.Weekday())) % 7
	if daysAhead == 0 {
		// Monday itself: the upcoming reset is a full week away.
		daysAhead = 7
	}
	return midnight.AddDate(0, 0, daysAhead)
}

// derivedStatus marks a reset time the vendor did not report, so the console can
// tell a computed schedule from an upstream value.
func derivedStatus(status string) string {
	if status == "" {
		return "derived"
	}
	return status + " derived"
}

// planAndAccount reads the plan/workspace labels some usage responses carry. When
// neither is reported the account is a hash prefix of the key, never the key.
func planAndAccount(payload map[string]any, apiKey string) (planName, accountID string) {
	planName = firstText(payload, "planName", "plan_name")
	if planName == "" {
		if plan := object(payload["plan"]); plan != nil {
			planName = firstText(plan, "name", "id", "tier")
		} else {
			planName = firstText(payload, "plan")
		}
	}
	accountID = firstText(payload, "accountId", "account_id", "workspaceId", "workspace_id")
	if accountID == "" {
		if workspace := object(payload["workspace"]); workspace != nil {
			accountID = firstText(workspace, "id", "slug", "name")
		}
	}
	if accountID == "" {
		sum := sha256.Sum256([]byte(strings.TrimSpace(apiKey)))
		accountID = "key-" + hex.EncodeToString(sum[:8])
	}
	return planName, accountID
}
