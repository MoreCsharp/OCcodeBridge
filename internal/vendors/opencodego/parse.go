package opencodego

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
)

// object narrows a decoded JSON value to an object, tolerating anything else.
func object(value any) map[string]any {
	if narrowed, ok := value.(map[string]any); ok {
		return narrowed
	}
	return nil
}

// firstText returns the first non-empty string among keys.
func firstText(m map[string]any, keys ...string) string {
	for _, key := range keys {
		text, ok := m[key].(string)
		if !ok {
			continue
		}
		if trimmed := strings.TrimSpace(text); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// firstNumber returns the first key that reads as a finite number. Money fields
// arrive as JSON numbers or as numeric strings (the server models bigints), so
// both are accepted.
func firstNumber(m map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		if value, ok := toFloat(m[key]); ok {
			return value, true
		}
	}
	return 0, false
}

func toFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return 0, false
		}
		return typed, true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return 0, false
		}
		return parsed, true
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, false
		}
		parsed, err := strconv.ParseFloat(trimmed, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return 0, false
		}
		return parsed, true
	}
	return 0, false
}

// firstTime returns the first key that reads as a timestamp.
func firstTime(m map[string]any, keys ...string) (time.Time, bool) {
	for _, key := range keys {
		if at, ok := coerceTime(m[key]); ok {
			return at, true
		}
	}
	return time.Time{}, false
}

// coerceTime accepts the reset formats the usage APIs use: ISO strings, unix
// seconds and unix milliseconds, as numbers or numeric strings.
func coerceTime(value any) (time.Time, bool) {
	if text, ok := value.(string); ok {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return time.Time{}, false
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05-0700", "2006-01-02 15:04:05"} {
			if at, err := time.Parse(layout, trimmed); err == nil {
				return at.UTC(), true
			}
		}
		if number, ok := toFloat(trimmed); ok {
			return unixTime(number)
		}
		return time.Time{}, false
	}
	if number, ok := toFloat(value); ok {
		return unixTime(number)
	}
	return time.Time{}, false
}

// unixTime reads a unix timestamp in seconds or milliseconds. Unix seconds stay
// below 1e11 until the year 5138, so anything larger can only be milliseconds.
func unixTime(value float64) (time.Time, bool) {
	if value <= 0 {
		return time.Time{}, false
	}
	if value >= 1e11 {
		return time.UnixMilli(int64(value)).UTC(), true
	}
	return time.Unix(int64(value), 0).UTC(), true
}

// clampPercent keeps a reported percentage inside 0-100; a non-finite value
// stays unknown instead of becoming a number.
func clampPercent(value float64) *float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	clamped := math.Max(0, math.Min(100, value))
	return &clamped
}

// floatPtr returns a pointer to value, for the optional Usage fields.
func floatPtr(value float64) *float64 {
	return &value
}
