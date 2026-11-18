package bridge

import (
	"testing"
)

// The plugin no longer rewrites provider routing, but client providerOptions that
// belong to a model family must still reach the upstream untouched.
func TestClientProviderOptionsAreForwardedUnchanged(t *testing.T) {
	s, err := registerService(t, modelConfig(t, "nonstream_mode: stream-aggregate\n"))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	h := &capturingHost{plan: ssePlan(simpleSSE())}
	s.SetHost(h.call)
	request, err := decodeObject([]byte(`{"messages":[{"role":"user","content":"hello"}],"providerOptions":{"anthropic":{"thinking":{"type":"enabled","budgetTokens":2048}},"deepseek":{"reasoningEffort":"max"}},"provider":{"only":["alibaba"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Handle("executor.execute", requestWithPayload(string(jsonBytes(request)))); err != nil {
		t.Fatalf("execute: %v", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.bodies) != 1 {
		t.Fatalf("upstream requests = %d, want 1", len(h.bodies))
	}
	body := h.bodies[0]
	if got := string(jsonBytes(object(body["providerOptions"]))); got != `{"anthropic":{"thinking":{"budgetTokens":2048,"type":"enabled"}},"deepseek":{"reasoningEffort":"max"}}` {
		t.Errorf("upstream providerOptions = %s, want the client object unchanged", got)
	}
	if got := string(jsonBytes(object(body["provider"]))); got != `{"only":["alibaba"]}` {
		t.Errorf("upstream provider = %s, want the client object unchanged", got)
	}
	if body["model"] != "deepseek-v4.1-flash" {
		t.Errorf("upstream model = %v, want the vendor prefix removed", body["model"])
	}
}
