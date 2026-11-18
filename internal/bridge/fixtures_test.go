package bridge

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// capturingHost records the outgoing request bodies so tests can assert on what
// the executor actually sends upstream, and replies with a minimal completion.
type capturingHost struct {
	mu     sync.Mutex
	bodies []map[string]any
	urls   []string
	reads  int
	plan   hostPlan
}

func (c *capturingHost) call(method string, payload, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	request, _ := payload.(map[string]any)
	switch method {
	case "host.http.do_stream":
		var body map[string]any
		if err := json.Unmarshal(request["body"].([]byte), &body); err != nil {
			return err
		}
		c.bodies = append(c.bodies, body)
		c.urls = append(c.urls, str(request["url"]))
		*out.(*upstreamStream) = upstreamStream{StatusCode: c.plan.status, Headers: c.plan.header, StreamID: fmt.Sprintf("capture-%d", len(c.bodies))}
		return nil
	case "host.http.stream_read":
		chunks := c.plan.chunks
		if len(chunks) == 0 {
			// Success shorthand: one minimal completion, then EOF.
			chunks = []readChunk{{Payload: simpleSSE()}}
		}
		index := c.reads
		c.reads++
		if index >= len(chunks) {
			*out.(*readChunk) = readChunk{Done: true}
		} else {
			*out.(*readChunk) = chunks[index]
		}
		return nil
	case "host.http.stream_close", "host.stream.emit", "host.stream.close":
		return nil
	default:
		return fmt.Errorf("unexpected host method %q", method)
	}
}

func registerService(t *testing.T, configYAML string) (*Service, error) {
	t.Helper()
	s := NewService()
	_, err := s.Handle("plugin.register", jsonBytes(map[string]any{"config_yaml": []byte(configYAML)}))
	return s, err
}

// registerMigratedService opts a fixture into a directory that already carries
// settings.json, so a test can start from a saved configuration instead of YAML.
func registerMigratedService(t *testing.T, configYAML string, settingsJSON string) (*Service, error) {
	t.Helper()
	var config struct {
		DataDir string `yaml:"data_dir"`
	}
	if err := yaml.Unmarshal([]byte(configYAML), &config); err != nil {
		t.Fatalf("decode migrated fixture: %v", err)
	}
	if config.DataDir == "" {
		t.Fatal("migrated fixture requires an explicit data_dir")
	}
	path := filepath.Join(config.DataDir, "settings.json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("migrated fixture requires a fresh settings file: %v", err)
	}
	if err := os.WriteFile(path, []byte(settingsJSON), 0600); err != nil {
		t.Fatalf("write migrated fixture: %v", err)
	}
	return registerService(t, configYAML)
}

func modelConfig(t *testing.T, extra string) string {
	t.Helper()
	out := fmt.Sprintf("data_dir: %q\n", filepath.ToSlash(t.TempDir()))
	out += "models:\n  - id: deepseek-flash\n    upstream_id: opencode-go/deepseek-v4.1-flash\n"
	return out + extra
}

func requestWithPayload(payload string) json.RawMessage {
	return requestForModel("deepseek-flash", payload)
}

func requestForModel(model, payload string) json.RawMessage {
	return jsonBytes(ExecutorRequest{
		Model:          model,
		Payload:        []byte(payload),
		StorageJSON:    jsonBytes(Credential{Type: Provider, ID: "credential-1", Label: "test credential", APIKey: "test-key", Vendor: "opencode-go"}),
		HostCallbackID: "callback-1",
	})
}

// upstreamBodyOf returns the request body actually sent by the executor.
func upstreamBodyOf(t *testing.T, s *Service, payload string) map[string]any {
	t.Helper()
	host := &capturingHost{plan: ssePlan(simpleSSE())}
	s.SetHost(host.call)
	if _, err := s.Handle("executor.execute", requestWithPayload(payload)); err != nil {
		t.Fatalf("execute: %v", err)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if len(host.bodies) != 1 {
		t.Fatalf("expected 1 upstream body, got %d", len(host.bodies))
	}
	return host.bodies[0]
}

func TestCredentialPriorityNormalisation(t *testing.T) {
	zero, negative, high := 0, -3, 500
	for _, tt := range []struct {
		name  string
		in    *int
		want  int
		isNil bool
	}{
		{"absent", nil, 0, true},
		{"zero", &zero, 0, true},
		{"negative", &negative, 0, true},
		{"value", &high, maxCredentialPriority, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizedPriority(tt.in)
			if tt.isNil {
				if got != nil {
					t.Fatalf("want unset, got %d", *got)
				}
				return
			}
			if got == nil || *got != tt.want {
				t.Fatalf("want %d, got %v", tt.want, got)
			}
		})
	}
}

func TestCredentialsAreOrderedByPriorityThenLabel(t *testing.T) {
	s := NewService()
	five, three := 5, 3
	s.creds = map[string]Credential{
		"id-1": {ID: "id-1", Label: "乙", Priority: &five},
		"id-2": {ID: "id-2", Label: "甲"},
		"id-3": {ID: "id-3", Label: "丙", Priority: &five},
		"id-4": {ID: "id-4", Label: "丁", Priority: &three},
	}
	got := []string{}
	for _, item := range s.credentials() {
		got = append(got, str(item["label"]))
	}
	want := []string{"丙", "乙", "丁", "甲"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("order = %v, want %v (priority desc, then label, unset last)", got, want)
	}
}

// errorEntry drives one failing upstream response and returns the recorded log.
func errorEntry(t *testing.T, s *Service, plan hostPlan) LogEntry {
	t.Helper()
	host := &capturingHost{plan: plan}
	s.SetHost(host.call)
	if _, err := s.Handle("executor.execute", requestWithPayload(`{"messages":[{"role":"user","content":"hello"}]}`)); err == nil {
		t.Fatal("expected the request to fail")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.logs) == 0 {
		t.Fatal("no log entry recorded")
	}
	return s.logs[len(s.logs)-1]
}

func jsonErrorPlan(status int, body any) hostPlan {
	return hostPlan{status: status, header: http.Header{"Content-Type": []string{"application/json"}}, chunks: []readChunk{{Payload: jsonBytes(body), Done: true}}}
}

func TestProviderIsRecoveredFromStructuredRoutingBlock(t *testing.T) {
	s, err := registerService(t, modelConfig(t, ""))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	entry := errorEntry(t, s, jsonErrorPlan(429, map[string]any{
		"error":            map[string]any{"message": "ordinary rate limit"},
		"providerMetadata": map[string]any{"gateway": map[string]any{"routing": map[string]any{"resolvedProvider": "fireworks"}}},
	}))
	if entry.Provider != "fireworks" {
		t.Errorf("provider = %q, want fireworks", entry.Provider)
	}
	if !strings.Contains(entry.ProviderSource, "resolvedProvider") {
		t.Errorf("provider_source = %q, want it to name resolvedProvider", entry.ProviderSource)
	}
}

func TestProviderIsRecoveredFromNestedErrorMessage(t *testing.T) {
	// The shape actually observed in production: HTTP 200 with an SSE error
	// frame whose message embeds the routing block as text.
	s, err := registerService(t, modelConfig(t, ""))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	inner := `Failed to create stream: inference request failed: ` +
		`failed to invoke model 'deepseek/deepseek-v4.1-flash' with streaming: request failed with status 429: ` +
		`{"error":{"message":"Rate limit exceeded for deepseek/deepseek-v4.1-flash: this team's limit of ` +
		`100000000 input tokens per minute (per region) was reached."},"providerMetadata":{"gateway":{"routing":` +
		`{"originalModelId":"deepseek/deepseek-v4.1-flash","resolvedProvider":"baseten"}}}}`
	plan := ssePlan(sseFrame(map[string]any{"error": map[string]any{"code": "stream_initialization_failed", "message": inner}}))
	entry := errorEntry(t, s, plan)
	if entry.Provider != "baseten" {
		t.Errorf("provider = %q, want baseten", entry.Provider)
	}
	if !strings.Contains(entry.ProviderSource, "error.message") {
		t.Errorf("provider_source = %q, want the message fallback", entry.ProviderSource)
	}
	if entry.RateLimitScope != "upstream_team" {
		t.Errorf("rate_limit_scope = %q, want upstream_team", entry.RateLimitScope)
	}
	if entry.UpstreamHTTPStatus != 200 {
		t.Errorf("upstream_http_status = %d, want the transport status 200", entry.UpstreamHTTPStatus)
	}
}

func TestProviderStaysUnknownWhenUpstreamReportsNone(t *testing.T) {
	s, err := registerService(t, modelConfig(t, ""))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	entry := errorEntry(t, s, jsonErrorPlan(500, map[string]any{"error": "empty response content"}))
	if entry.Provider != "unknown" {
		t.Errorf("provider = %q, want unknown", entry.Provider)
	}
}

func TestSuccessfulProviderIsNotOverwritten(t *testing.T) {
	// A locally synthesised rate limit carries no provider; the value already
	// read from the successful frame must survive.
	entry := LogEntry{Provider: "wafer", ProviderSource: "choices[0].delta.provider_metadata.gateway.routing.finalProvider"}
	attempt := Attempt{Provider: "wafer"}
	err := classifyUpstreamError(429, http.Header{}, map[string]any{"error": "rate limit exceeded for deepseek/deepseek-v4.1-flash"}, time.Now())
	logErrorDetails(&entry, &attempt, err)
	if entry.Provider != "wafer" || attempt.Provider != "wafer" {
		t.Errorf("provider = %q/%q, want the earlier value preserved", entry.Provider, attempt.Provider)
	}
}
