package bridge

import "testing"

func TestObserveServedModelRecordsUpstreamModel(t *testing.T) {
	for _, tt := range []struct {
		name   string
		frames []map[string]any
		served string
	}{
		{
			name:   "reported model",
			frames: []map[string]any{{"model": "deepseek/deepseek-v4.1-flash-1017"}},
			served: "deepseek/deepseek-v4.1-flash-1017",
		},
		{
			name: "last frame wins",
			frames: []map[string]any{
				{"model": "deepseek/deepseek-v4.1-flash-1017"},
				{"model": "kimi/kimi-k2"},
			},
			served: "kimi/kimi-k2",
		},
		{
			name:   "frame without a model leaves the record alone",
			frames: []map[string]any{{"model": "kimi/kimi-k2"}, {}},
			served: "kimi/kimi-k2",
		},
		{
			name:   "empty model is not a served model",
			frames: []map[string]any{{"model": ""}},
			served: "",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			entry := &LogEntry{}
			attempt := &Attempt{}
			for _, frame := range tt.frames {
				observeServedModel(frame, entry, attempt)
			}
			if entry.ServedModel != tt.served || attempt.ServedModel != tt.served {
				t.Fatalf("served model: entry %q attempt %q, want %q", entry.ServedModel, attempt.ServedModel, tt.served)
			}
		})
	}
}
