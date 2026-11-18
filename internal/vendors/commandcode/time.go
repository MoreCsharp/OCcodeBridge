package commandcode

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// parseFlexibleTime reads a timestamp the API may report as an ISO string, unix
// seconds or unix milliseconds. The CLI feeds windowLimits.resetAt straight to
// Date.now(), so a millisecond number is what the API is expected to send; the
// other forms are accepted rather than dropping a reset time the user can see.
func parseFlexibleTime(raw json.RawMessage) *time.Time {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return nil
	}
	if text[0] == '"' {
		var literal string
		if err := json.Unmarshal(raw, &literal); err != nil {
			return nil
		}
		return parseTimestamp(strings.TrimSpace(literal))
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return nil
	}
	return timestampFromUnix(value)
}

// parseTimestamp reads an ISO 8601 timestamp or a numeric string.
func parseTimestamp(text string) *time.Time {
	if text == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			utc := parsed.UTC()
			return &utc
		}
	}
	if value, err := strconv.ParseFloat(text, 64); err == nil {
		return timestampFromUnix(value)
	}
	return nil
}

// timestampFromUnix accepts unix seconds and unix milliseconds. Anything below
// 1e11 read as seconds would land after the year 5138, so a smaller number can
// only be seconds and a larger one only milliseconds.
func timestampFromUnix(value float64) *time.Time {
	if value <= 0 {
		return nil
	}
	if value < 1e11 {
		value *= 1000
	}
	stamp := time.UnixMilli(int64(value)).UTC()
	return &stamp
}
