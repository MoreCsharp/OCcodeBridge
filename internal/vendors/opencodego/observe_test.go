package opencodego

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestObserveResponseFindsAllowance(t *testing.T) {
	cases := []struct {
		name  string
		frame string
		want  map[string]any
	}{
		{
			name:  "documented opencode.allowance number",
			frame: `{"opencode":{"allowance":0.0123}}`,
			want:  map[string]any{"value": 0.0123},
		},
		{
			name:  "documented opencode.allowance object",
			frame: `{"opencode":{"allowance":{"charged":0.25,"unit":"usd"}}}`,
			want:  map[string]any{"charged": 0.25, "unit": "usd"},
		},
		{
			name:  "usage envelope with nested allowance",
			frame: `{"usage":{"opencode":{"allowance":2}}}`,
			want:  map[string]any{"value": 2.0},
		},
		{
			name:  "usage envelope carrying the allowance directly",
			frame: `{"usage":{"opencode":{"charged":1.5}}}`,
			want:  map[string]any{"charged": 1.5},
		},
		{
			name:  "usage.allowance",
			frame: `{"usage":{"allowance":3}}`,
			want:  map[string]any{"value": 3.0},
		},
		{
			name:  "bare opencode number",
			frame: `{"opencode":0.5}`,
			want:  map[string]any{"value": 0.5},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var frame map[string]any
			if err := json.Unmarshal([]byte(testCase.frame), &frame); err != nil {
				t.Fatalf("decode frame: %v", err)
			}
			observation := Vendor{}.ObserveResponse(frame)
			if observation == nil {
				t.Fatal("allowance not observed")
			}
			if !reflect.DeepEqual(observation.Allowance, testCase.want) {
				t.Fatalf("allowance = %#v, want %#v", observation.Allowance, testCase.want)
			}
			if len(observation.Raw) == 0 || !json.Valid(observation.Raw) {
				t.Fatalf("raw fragment = %q, want valid JSON", observation.Raw)
			}
		})
	}
}

func TestObserveResponseIgnoresFramesWithoutAllowance(t *testing.T) {
	frames := []string{
		`{"choices":[{"delta":{"content":"hello"}}]}`,
		`{"opencode":{"model":"kimi-k3"}}`,
		`{"opencode":{"allowance":{}}}`,
		`{"usage":{"other":1}}`,
		`{}`,
	}
	for _, frameJSON := range frames {
		var frame map[string]any
		if err := json.Unmarshal([]byte(frameJSON), &frame); err != nil {
			t.Fatalf("decode frame: %v", err)
		}
		if observation := (Vendor{}).ObserveResponse(frame); observation != nil {
			t.Fatalf("frame %s observed %#v, want nil", frameJSON, observation)
		}
	}
}

func TestObserveResponseKeepsRawFragment(t *testing.T) {
	frame := map[string]any{"opencode": map[string]any{"allowance": 4.5}}
	observation := Vendor{}.ObserveResponse(frame)
	if observation == nil {
		t.Fatal("allowance not observed")
	}
	if !strings.Contains(string(observation.Raw), "4.5") {
		t.Fatalf("raw fragment = %q, want the reported value", observation.Raw)
	}
}
