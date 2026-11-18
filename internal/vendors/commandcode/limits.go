package commandcode

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/MoreCsharp/OCcodeBridge/internal/vendors"
)

// upgradeRequiredReason is what the console shows for a plan without API access:
// polling usage cannot clear it, so the bridge stops scheduling instead of
// cooling the credential down.
const upgradeRequiredReason = "Go 套餐无 API 权限，请升级到 GOAT 及以上"

// usageLimitMessage matches the window error that names no rateLimit object:
// "You've reached your 5-hour usage limit for your plan."
var usageLimitMessage = regexp.MustCompile(`(?i)usage limit for your plan`)

// resetAtMessage picks the ISO reset time out of the same message, the form the
// CLI's extractResetAtMs reads.
var resetAtMessage = regexp.MustCompile(`(?i)resets at (\d{4}-\d{2}-\d{2}T[\d:.]+Z)`)

// errorFields are the fields an error body carries, whether it is the documented
// OpenAI envelope or the CLI's {"success":false,"error":{...}} shape.
type errorFields struct {
	Code      string          `json:"code"`
	Status    int             `json:"status"`
	Type      string          `json:"type"`
	Message   string          `json:"message"`
	RateLimit *limitRateLimit `json:"rateLimit"`
}

// errorEnvelope nests the error object the API puts under "error".
type errorEnvelope struct {
	errorFields
	Error json.RawMessage `json:"error"`
}

// limitRateLimit names the exhausted window and its reset, in unix seconds.
type limitRateLimit struct {
	Window string          `json:"window"`
	Reset  json.RawMessage `json:"reset"`
	Model  string          `json:"model"`
}

// windowKinds maps the API's rateLimit.window onto the shared plan windows. A
// "daily" window is deliberately absent: it is the per-day request cap of a free
// model (e.g. "up to 1000 requests a day"), not a plan window, so it must stay an
// ordinary request-scoped rate limit instead of pausing the whole account.
var windowKinds = map[string]string{
	"fivehour": vendors.WindowFiveHour,
	"weekly":   vendors.WindowWeekly,
}

// ClassifyLimitError reports the rolling window CommandCode exhausted, and the Go
// plan that has no API access at all. Everything else stays an ordinary rate
// limit (nil), including an upstream 429 that names no window: the bridge then
// cools the credential down instead of pausing the account.
func (Vendor) ClassifyLimitError(status int, body []byte, _ time.Time) *vendors.LimitBlock {
	parsed := parseErrorFields(body)

	if status == http.StatusForbidden {
		if strings.EqualFold(strings.TrimSpace(parsed.Code), "upgrade_required") || mentionsGoPlan(parsed.Message) {
			return &vendors.LimitBlock{Permanent: true, Reason: upgradeRequiredReason}
		}
		return nil
	}
	if status != http.StatusTooManyRequests && !strings.EqualFold(strings.TrimSpace(parsed.Code), "rate_limited") {
		return nil
	}
	if parsed.RateLimit != nil {
		if kind, ok := windowKinds[normalizeWindow(parsed.RateLimit.Window)]; ok {
			return &vendors.LimitBlock{Window: kind, ResetAt: parseFlexibleTime(parsed.RateLimit.Reset)}
		}
	}
	if usageLimitMessage.MatchString(parsed.Message) {
		kind := vendors.WindowFiveHour
		if strings.Contains(strings.ToLower(parsed.Message), "weekly") {
			kind = vendors.WindowWeekly
		}
		return &vendors.LimitBlock{Window: kind, ResetAt: messageReset(parsed.Message)}
	}
	return nil
}

// mentionsGoPlan reports the 403 message that names the Go plan, which is the only
// plan without API access, so a body without a code still classifies. A bare
// "upgrade" is not enough: a Permanent block stops scheduling until an operator
// acts, so an unrelated 403 must never trigger it.
func mentionsGoPlan(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(lower, "go plan") && strings.Contains(lower, "api")
}

// normalizeWindow folds the window spellings onto the keys of windowKinds.
func normalizeWindow(window string) string {
	return strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(window)))
}

// parseErrorFields flattens an error body onto one set of fields: the nested
// object wins, because that is the one both documented shapes fill.
func parseErrorFields(body []byte) errorFields {
	var envelope errorEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return errorFields{}
	}
	nested := strings.TrimSpace(string(envelope.Error))
	if nested == "" || nested == "null" {
		return envelope.errorFields
	}
	var fields errorFields
	if err := json.Unmarshal(envelope.Error, &fields); err == nil {
		return fields
	}
	// Some upstreams report the error as a bare string.
	var text string
	if err := json.Unmarshal(envelope.Error, &text); err == nil && text != "" {
		envelope.Message = text
	}
	return envelope.errorFields
}

// messageReset reads the ISO reset time out of a window limit message, which is
// the only reset the API gives when rateLimit is absent.
func messageReset(message string) *time.Time {
	match := resetAtMessage.FindStringSubmatch(message)
	if len(match) != 2 {
		return nil
	}
	return parseTimestamp(match[1])
}
