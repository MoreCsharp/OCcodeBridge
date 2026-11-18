package opencodego

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xiao-qiu-qiu/OCcodeBridge/internal/vendors"
)

// usageLimitMessage matches the wording that identifies a Go usage window limit.
// A bare rate_limit_error does not match, which keeps ordinary throttling out of
// the quota path.
var usageLimitMessage = regexp.MustCompile(`usage limit|allowance|5-hour|5 hour|five hour|rolling|weekly|monthly|7-day`)

// resetTimeInMessage finds an ISO timestamp inside a limit message.
var resetTimeInMessage = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})`)

// countdownInMessage finds "resets in 4h 30m" wording, and countdownPart splits
// its day/hour/minute/second tokens.
var (
	countdownInMessage = regexp.MustCompile(`\bin\s+((?:\d+\s*[dhms]\b\s*)+)`)
	countdownPart      = regexp.MustCompile(`(\d+)\s*([dhms])`)
)

// ClassifyLimitError reports an exhausted Go usage window on a 429 and returns
// nil for everything else. The upstream GoUsageLimitError body has never been
// observed with a real key (spec §5), so detection is deliberately tolerant: an
// error type/name mentioning GoUsageLimit, or a message naming a usage window.
// Ordinary throttling (rate_limit_error with no usage wording), 401 and 403 all
// return nil.
func (Vendor) ClassifyLimitError(status int, body []byte, now time.Time) *vendors.LimitBlock {
	if status != http.StatusTooManyRequests {
		return nil
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return nil
	}
	envelope := object(payload["error"])
	message := firstText(envelope, "message", "detail")
	if message == "" {
		message = firstText(payload, "message", "detail")
	}
	// GoUsageLimitError may be reported as error.type, error.name, error.code or
	// at the top level, with underscores or camel case.
	typeNames := errorNames(envelope) + " " + errorNames(payload)
	named := strings.Contains(typeNames, "gousagelimit")
	if !named && !usageLimitMessage.MatchString(strings.ToLower(message)) {
		return nil
	}
	window := windowFromText(firstText(envelope, "window") + " " + firstText(payload, "window") + " " + message)
	resetAt := resetFromFields(payload, envelope, now)
	if resetAt == nil {
		resetAt = resetFromMessage(message, now)
	}
	reason := message
	if reason == "" {
		reason = "OpenCode Go usage limit reached"
	}
	return &vendors.LimitBlock{
		Window:  window,
		ResetAt: resetAt,
		Reason:  limitBytes(reason, 200),
	}
}

// errorNames flattens the fields that could carry an error name and normalises
// punctuation so "GoUsageLimitError" and "go_usage_limit" compare alike.
func errorNames(m map[string]any) string {
	names := firstText(m, "type", "name", "code") + " " + firstText(m, "error")
	normaliser := strings.NewReplacer("_", "", "-", "", " ", "")
	return normaliser.Replace(strings.ToLower(names))
}

// windowFromText maps the wording to a window kind. The 5-hour window is checked
// first because messages name it together with the window it belongs to. The text
// is normalised first so structured values such as "five_hour" also match.
func windowFromText(value string) string {
	text := strings.ReplaceAll(strings.ToLower(value), "_", "")
	switch {
	case strings.Contains(text, "5-hour"), strings.Contains(text, "5 hour"),
		strings.Contains(text, "5hour"), strings.Contains(text, "five hour"),
		strings.Contains(text, "fivehour"), strings.Contains(text, "rolling"):
		return vendors.WindowFiveHour
	case strings.Contains(text, "week"):
		return vendors.WindowWeekly
	case strings.Contains(text, "month"):
		return vendors.WindowMonthly
	}
	return ""
}

// resetFromFields reads an absolute reset time first and the retryAfter delay
// last, since the two need different interpretations.
func resetFromFields(payload, envelope map[string]any, now time.Time) *time.Time {
	var sources []map[string]any
	for _, source := range []map[string]any{envelope, payload} {
		if source == nil {
			continue
		}
		sources = append(sources, source)
		// Some envelopes nest the hint as error.rateLimit.
		if rateLimit := object(source["rateLimit"]); rateLimit != nil {
			sources = append(sources, rateLimit)
		}
	}
	for _, source := range sources {
		if at, ok := firstTime(source, "resetsAt", "resetAt", "reset", "resets_at", "reset_at"); ok {
			return &at
		}
	}
	for _, source := range sources {
		if at, ok := retryAfterTime(source["retryAfter"], now); ok {
			return &at
		}
	}
	return nil
}

// retryAfterTime reads a delay in seconds, or an absolute timestamp when the value
// is too large to be a delay.
func retryAfterTime(value any, now time.Time) (time.Time, bool) {
	if seconds, ok := toFloat(value); ok {
		if seconds > 0 && seconds < 1e8 {
			return now.Add(time.Duration(seconds * float64(time.Second))).UTC(), true
		}
		return unixTime(seconds)
	}
	return coerceTime(value)
}

// resetFromMessage reads an ISO timestamp or an "in 4h 30m" countdown from the
// limit message itself.
func resetFromMessage(message string, now time.Time) *time.Time {
	if match := resetTimeInMessage.FindString(message); match != "" {
		if at, ok := coerceTime(match); ok {
			return &at
		}
	}
	if match := countdownInMessage.FindStringSubmatch(strings.ToLower(message)); match != nil {
		if delta, ok := parseCountdown(match[1]); ok {
			at := now.Add(delta).UTC()
			return &at
		}
	}
	return nil
}

// parseCountdown sums day/hour/minute/second tokens such as "4h 30m".
func parseCountdown(value string) (time.Duration, bool) {
	var total time.Duration
	found := false
	for _, part := range countdownPart.FindAllStringSubmatch(value, -1) {
		count, err := strconv.Atoi(part[1])
		if err != nil {
			continue
		}
		found = true
		switch part[2] {
		case "d":
			total += time.Duration(count) * 24 * time.Hour
		case "h":
			total += time.Duration(count) * time.Hour
		case "m":
			total += time.Duration(count) * time.Minute
		case "s":
			total += time.Duration(count) * time.Second
		}
	}
	return total, found
}
