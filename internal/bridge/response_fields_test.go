package bridge

import (
	"bytes"
	"testing"
	"time"
)

func TestStreamAggregatePreservesResponseFields(t *testing.T) {
	for _, tt := range []struct {
		name     string
		metadata any
		want     map[string]any
	}{
		{
			name: "recursive merge ignores nested nulls",
			metadata: map[string]any{
				"gateway": map[string]any{
					"routing":  map[string]any{"finalProvider": nil, "route_id": nil, "region": "us-east"},
					"trace_id": nil,
				},
				"billing": map[string]any{"currency": nil, "charged": true},
			},
			want: map[string]any{
				"gateway": map[string]any{
					"routing":  map[string]any{"finalProvider": "fireworks", "route_id": "route-1", "region": "us-east"},
					"trace_id": "trace-1",
				},
				"billing": map[string]any{"currency": "USD", "charged": true},
			},
		},
		{
			name:     "top-level null preserves metadata",
			metadata: nil,
			want: map[string]any{
				"gateway": map[string]any{
					"routing":  map[string]any{"finalProvider": "fireworks", "route_id": "route-1"},
					"trace_id": "trace-1",
				},
				"billing": map[string]any{"currency": "USD"},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := registeredService(t, "stream-aggregate")
			first := sseFrame(map[string]any{
				"id": "chat-fields-1", "object": "chat.completion.chunk", "obfuscation": "first-padding",
				"service_tier": "priority",
				"provider_metadata": map[string]any{
					"gateway": map[string]any{
						"routing":  map[string]any{"finalProvider": "fireworks", "route_id": "route-1"},
						"trace_id": "trace-1",
					},
					"billing": map[string]any{"currency": "USD"},
				},
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "hel"}}},
			})
			second := sseFrame(map[string]any{
				"id": nil, "object": "chat.completion.chunk", "obfuscation": "last-padding",
				"service_tier": nil, "provider_metadata": tt.metadata,
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "lo"}, "finish_reason": "stop"}},
			})
			s.SetHost(newFakeHost(ssePlan(first, second, []byte("data: [DONE]\r\n\r\n"))).call)
			result, err := s.Handle("executor.execute", executorRequest(""))
			if err != nil {
				t.Fatalf("aggregate response: %v", err)
			}
			body, err := decodeObject(result.(Response).Payload)
			if err != nil {
				t.Fatal(err)
			}
			if body["id"] != "chat-fields-1" || body["service_tier"] != "priority" {
				t.Fatalf("later null erased response fields: %s", result.(Response).Payload)
			}
			if got := body["provider_metadata"]; !bytes.Equal(jsonBytes(got), jsonBytes(tt.want)) {
				t.Fatalf("provider_metadata = %s, want %s", jsonBytes(got), jsonBytes(tt.want))
			}
			if body["object"] != "chat.completion" {
				t.Errorf("aggregate object = %v, want chat.completion", body["object"])
			}
			if _, ok := body["obfuscation"]; ok {
				t.Error("aggregate leaked stream-only obfuscation")
			}
			choices := list(body["choices"])
			if len(choices) != 1 || object(object(choices[0])["message"])["content"] != "hello" || object(choices[0])["finish_reason"] != "stop" {
				t.Fatalf("response field merge damaged choices: %#v", choices)
			}
		})
	}
}

func TestNativeFallbackRecordsBothAttempts(t *testing.T) {
	s := registeredService(t, "native-fallback")
	native := jsonErrorPlan(500, map[string]any{"error": "empty response content"})
	success := sseFrame(map[string]any{
		"choices": []any{map[string]any{
			"index": 0, "delta": map[string]any{"content": "hello"}, "finish_reason": "stop",
		}},
	})
	h := newFakeHost(native, ssePlan(success, []byte("data: [DONE]\r\n\r\n")))
	s.SetHost(h.call)
	result, err := s.Handle("executor.execute", executorRequest(""))
	if err != nil {
		t.Fatalf("native-to-stream fallback: %v", err)
	}
	if !bytes.Contains(result.(Response).Payload, []byte("hello")) {
		t.Fatalf("fallback lost successful response: %s", result.(Response).Payload)
	}
	if len(h.opened) != 2 || h.opened[0] || !h.opened[1] {
		t.Fatalf("upstream attempts = %#v, want native then stream", h.opened)
	}
	if len(s.logs) != 1 || len(s.logs[0].Attempts) != 2 {
		t.Fatalf("fallback logs = %#v, want one log with two attempts", s.logs)
	}
	entry := s.logs[0]
	if entry.Status != 200 {
		t.Fatalf("final log status = %d, want 200", entry.Status)
	}
	for i, want := range []struct {
		status int
		mode   string
	}{
		{status: 500, mode: "nonstream"},
		{status: 200, mode: "stream-aggregate"},
	} {
		attempt := entry.Attempts[i]
		if attempt.Status != want.status || attempt.Mode != want.mode {
			t.Fatalf("attempt %d = %#v, want status/mode %d/%s", i, attempt, want.status, want.mode)
		}
	}
}

func TestWrappedRateLimitKeepsTransportAndUpstreamStatus(t *testing.T) {
	inner := map[string]any{"error": map[string]any{"message": "Rate limit exceeded"}}
	body := map[string]any{"error": map[string]any{
		"message": "request failed with status 429: " + string(jsonBytes(inner)),
	}}
	err := classifyUpstreamError(200, nil, body, time.Unix(0, 0))
	detail := err.(*upstreamError)
	if detail.HTTPStatus != 200 || detail.UpstreamStatus != 429 || statusOf(err) != 429 {
		t.Fatalf("wrapped rate limit changed statuses: %#v", detail)
	}
}
