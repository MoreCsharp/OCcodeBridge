package bridge

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xiao-qiu-qiu/OCcodeBridge/internal/vendors"
)

// rawObservationVendor reports a fixed observation so a test can supply a raw
// fragment larger than any real frame.
type rawObservationVendor struct {
	fixtureVendor
	observation *vendors.Observation
}

func (v rawObservationVendor) ObserveResponse(map[string]any) *vendors.Observation {
	return v.observation
}

func TestObserveVendorFrameKeepsAllowanceAndRawFragment(t *testing.T) {
	s := NewService()
	stream := upstreamStream{vendor: fixtureVendor{}}
	entry := LogEntry{ID: "one"}
	s.observeVendorFrame(stream, &entry, map[string]any{"allowance": map[string]any{"charged": 0.25}})
	if entry.Allowance["charged"] != 0.25 || entry.AllowanceRaw == "" {
		t.Fatalf("allowance not recorded: %+v / %q", entry.Allowance, entry.AllowanceRaw)
	}
	var raw map[string]any
	if json.Unmarshal([]byte(entry.AllowanceRaw), &raw) != nil || raw["charged"] != 0.25 {
		t.Fatalf("raw fragment is not the upstream JSON: %q", entry.AllowanceRaw)
	}
	plain := LogEntry{ID: "two"}
	s.observeVendorFrame(stream, &plain, map[string]any{"choices": []any{}})
	if plain.Allowance != nil || plain.AllowanceRaw != "" {
		t.Fatal("a frame without an allowance recorded one")
	}
	if raw, _ := json.Marshal(plain); strings.Contains(string(raw), "allowance") {
		t.Fatalf("log JSON must omit absent allowance fields: %s", raw)
	}
}

func TestObserveVendorFrameTruncatesRawFragment(t *testing.T) {
	s := NewService()
	huge := `{"charged":0.25,"pad":"` + strings.Repeat("x", allowanceRawLimit*2) + `"}`
	entry := LogEntry{ID: "one"}
	s.observeVendorFrame(upstreamStream{vendor: rawObservationVendor{observation: &vendors.Observation{
		Allowance: map[string]any{"charged": 0.25}, Raw: json.RawMessage(huge),
	}}}, &entry, map[string]any{})
	if entry.Allowance["charged"] != 0.25 {
		t.Fatalf("allowance lost while truncating: %+v", entry.Allowance)
	}
	if len(entry.AllowanceRaw) > allowanceRawLimit+len("…") {
		t.Fatalf("raw fragment exceeded the limit: %d bytes", len(entry.AllowanceRaw))
	}
	if !strings.HasSuffix(entry.AllowanceRaw, "…") || !strings.HasPrefix(entry.AllowanceRaw, `{"charged":0.25`) {
		t.Fatalf("truncated fragment is not a readable prefix: %q", entry.AllowanceRaw[:32])
	}
}

func TestAllowanceRawTextCutsOnRuneBoundary(t *testing.T) {
	pad := strings.Repeat("额", allowanceRawLimit)
	text := allowanceRawText(json.RawMessage(`"` + pad + `"`))
	if !strings.HasSuffix(text, "…") || len(text) > allowanceRawLimit+len("…") {
		t.Fatalf("rune-boundary truncation failed: %d bytes", len(text))
	}
	if strings.Contains(text, "\ufffd") {
		t.Fatal("truncation produced a replacement character")
	}
	if allowanceRawText(nil) != "" {
		t.Fatal("an empty fragment must record nothing")
	}
}
