package bridge

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const samplerUsagePath = "/usage"

type samplerRequest struct {
	method, path, key, callbackID string
}

// samplerHost fakes CPA's background HTTP bridge: it only accepts plain GETs to
// the fixture vendor's usage endpoint and records every host_callback_id it gets.
type samplerHost struct {
	mu          sync.Mutex
	requests    []samplerRequest
	accounts    map[string]string
	failures    map[string]int
	bodies      map[string]string
	blocking    map[string]bool
	closed      map[string]bool
	blockReads  bool
	next        int
	reset       time.Time
	readStarted chan struct{}
	readOnce    sync.Once
	release     chan struct{}
	releaseOnce sync.Once
}

func newSamplerHost() *samplerHost {
	return &samplerHost{
		accounts: map[string]string{}, failures: map[string]int{}, bodies: map[string]string{},
		blocking: map[string]bool{}, closed: map[string]bool{},
		reset:       time.Now().UTC().Add(5 * time.Hour),
		readStarted: make(chan struct{}), release: make(chan struct{}),
	}
}

func (h *samplerHost) call(method string, payload, out any) error {
	req, _ := payload.(map[string]any)
	switch method {
	case "host.http.do_stream":
		key := ""
		if header, ok := req["headers"].(http.Header); ok {
			key = strings.TrimPrefix(header.Get("Authorization"), "Bearer ")
		}
		path := ""
		if parsed, err := url.Parse(str(req["url"])); err == nil {
			path = parsed.Path
		}
		h.mu.Lock()
		h.requests = append(h.requests, samplerRequest{str(req["method"]), path, key, str(req["host_callback_id"])})
		if str(req["method"]) != "GET" || !strings.HasSuffix(path, samplerUsagePath) {
			h.mu.Unlock()
			return fmt.Errorf("sampler host rejected %s %s", str(req["method"]), str(req["url"]))
		}
		h.next++
		streamID := fmt.Sprintf("usage-%d", h.next)
		status := h.failures[key]
		if status == 0 {
			status = http.StatusOK
		}
		h.bodies[streamID] = h.body(h.accounts[key], status)
		if h.blockReads {
			h.blocking[streamID] = true
		}
		h.mu.Unlock()
		*out.(*upstreamStream) = upstreamStream{StatusCode: status, StreamID: streamID}
		return nil
	case "host.http.stream_read":
		streamID := str(req["stream_id"])
		h.mu.Lock()
		body, known := h.bodies[streamID]
		blocked := h.blocking[streamID]
		h.mu.Unlock()
		if !known {
			return fmt.Errorf("unknown sampler stream %q", streamID)
		}
		if blocked {
			h.readOnce.Do(func() { close(h.readStarted) })
			<-h.release
		}
		*out.(*readChunk) = readChunk{Payload: []byte(body), Done: true}
		return nil
	case "host.http.stream_close":
		streamID := str(req["stream_id"])
		h.mu.Lock()
		h.closed[streamID] = true
		blocked := h.blocking[streamID]
		h.mu.Unlock()
		if blocked {
			h.releaseOnce.Do(func() { close(h.release) })
		}
		return nil
	default:
		return fmt.Errorf("unexpected host callback %q", method)
	}
}

func (h *samplerHost) body(account string, status int) string {
	if status != http.StatusOK {
		return `{"error":{"message":"sampler upstream failure"}}`
	}
	return fmt.Sprintf(`{"accountId":%q,"planName":"Fixture Plan","windows":[{"kind":"5h","percent":3,"resetAt":%q}]}`, account, h.reset.Format(time.RFC3339Nano))
}

func (h *samplerHost) snapshot() []samplerRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]samplerRequest(nil), h.requests...)
}

func (h *samplerHost) pathRequests(path string) []samplerRequest {
	var out []samplerRequest
	for _, req := range h.snapshot() {
		if req.path == path {
			out = append(out, req)
		}
	}
	return out
}

func (h *samplerHost) pathCount(path string) int { return len(h.pathRequests(path)) }

func (h *samplerHost) closedCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.closed)
}

func samplerCredential(id, key string) Credential {
	return Credential{Type: Provider, ID: id, Label: id, APIKey: key, Vendor: testVendorID}
}

func samplerService(t *testing.T, host *samplerHost, creds ...Credential) *Service {
	t.Helper()
	s := registeredService(t, "")
	for _, c := range creds {
		s.creds[c.ID] = c
	}
	s.SetHost(host.call)
	return s
}

// startSampler registers the management routes, the only trigger the plugin
// needs to begin background sampling, and shuts the loop down after the test.
func startSampler(t *testing.T, s *Service) {
	t.Helper()
	if _, err := s.Handle("management.register", nil); err != nil {
		t.Fatalf("register management: %v", err)
	}
	t.Cleanup(func() { _, _ = s.Handle("plugin.shutdown", nil) })
}

func waitSampler(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the usage sampler")
}

func samplerLedger(s *Service, status string, ids ...string) func() bool {
	return func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		for _, id := range ids {
			entry := s.usageCache[id]
			if entry == nil || entry.done != nil || entry.value.CheckedAt == nil || entry.value.Status != status {
				return false
			}
			if status == "ok" && (entry.value.accountHash == "" || len(entry.value.Limits) == 0) {
				return false
			}
		}
		return true
	}
}

func ageSamplerCache(t *testing.T, s *Service, age time.Duration, ids ...string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	when := time.Now().Add(-age)
	for _, id := range ids {
		entry := s.usageCache[id]
		if entry == nil || entry.value.CheckedAt == nil {
			t.Fatalf("no usage ledger for %s", id)
		}
		entry.value.CheckedAt = &when
	}
}

// fixtureAccountHash is the identity the bridge derives for a fixture account.
func fixtureAccountHash(account string) string {
	return usageHash("vendor-account", testVendorID+"|"+account)
}

func TestUsageSamplerBaselineWithoutManagementHandle(t *testing.T) {
	host := newSamplerHost()
	host.accounts["k1"] = "acct-a"
	s := samplerService(t, host, samplerCredential("one", "k1"))
	startSampler(t, s)
	waitSampler(t, samplerLedger(s, "ok", "one"))

	requests := host.snapshot()
	if len(requests) != 1 {
		t.Fatalf("baseline requests = %d, want one usage call: %+v", len(requests), requests)
	}
	for _, req := range requests {
		if req.callbackID != "" {
			t.Fatalf("background sampling used a management callback id: %+v", req)
		}
	}
	if host.pathCount(samplerUsagePath) != 1 {
		t.Fatalf("baseline paths = %+v", requests)
	}
	s.mu.RLock()
	value := s.usageCache["one"].value
	s.mu.RUnlock()
	if value.accountHash != fixtureAccountHash("acct-a") || value.Plan == nil || value.Plan.Name != "Fixture Plan" {
		t.Fatalf("baseline sample = %+v", value)
	}
	if len(value.Limits) != 1 || value.Limits[0].Type != "five_hour" || value.Limits[0].PercentUsed == nil || *value.Limits[0].PercentUsed != 3 {
		t.Fatalf("baseline windows = %+v", value.Limits)
	}
	if value.Limits[0].ResetsAt == nil || !value.Limits[0].ResetsAt.Equal(host.reset) {
		t.Fatalf("baseline reset = %v, want %s", value.Limits[0].ResetsAt, host.reset)
	}
	if targets := s.usageSamplingTargets(time.Now()); len(targets) != 0 {
		t.Fatalf("fresh baseline still scheduled: %v", targets)
	}
}

func TestUsageSamplerSecondRoundKeepsFreshCache(t *testing.T) {
	host := newSamplerHost()
	host.accounts["k1"] = "acct-a"
	s := samplerService(t, host, samplerCredential("one", "k1"))
	startSampler(t, s)
	waitSampler(t, samplerLedger(s, "ok", "one"))

	before := len(host.snapshot())
	s.mu.RLock()
	stamp := *s.usageCache["one"].value.CheckedAt
	s.mu.RUnlock()
	if targets := s.usageSamplingTargets(time.Now()); len(targets) != 0 {
		t.Fatalf("fresh ledger scheduled %v", targets)
	}
	s.sampleUsage()
	if got := len(host.snapshot()); got != before {
		t.Fatalf("second round re-queried a fresh cache: %d -> %d", before, got)
	}
	s.mu.RLock()
	after := *s.usageCache["one"].value.CheckedAt
	s.mu.RUnlock()
	if !after.Equal(stamp) {
		t.Fatal("fresh cache was refreshed")
	}
}

func TestUsageSamplerGroupsAccountKeysIncludingDisabled(t *testing.T) {
	host := newSamplerHost()
	host.accounts["k1"], host.accounts["k2"], host.accounts["k3"], host.accounts["k4"] = "acct-a", "acct-a", "acct-b", "acct-a"
	paused := samplerCredential("paused", "k4")
	paused.Disabled = true
	proxied := samplerCredential("proxied", "k5")
	proxied.ProxyURL = "http://127.0.0.1:1"
	s := samplerService(t, host, samplerCredential("one", "k1"), samplerCredential("two", "k2"), samplerCredential("three", "k3"), paused, proxied)
	startSampler(t, s)
	waitSampler(t, samplerLedger(s, "ok", "one", "two", "three", "paused"))
	waitSampler(t, samplerLedger(s, "unavailable", "proxied"))
	if targets := s.usageSamplingTargets(time.Now()); len(targets) != 0 {
		t.Fatalf("round one left work: %v", targets)
	}

	ageSamplerCache(t, s, 2*usageTTL, "one", "two", "three", "paused")
	targets := s.usageSamplingTargets(time.Now())
	if len(targets) != 2 || targets[0] != "one" || targets[1] != "three" {
		t.Fatalf("same-account keys were not grouped: %v", targets)
	}
	before := host.pathCount(samplerUsagePath)
	s.sampleUsage()
	if got := host.pathCount(samplerUsagePath) - before; got != 2 {
		t.Fatalf("account samples = %d, want one per account", got)
	}
	keys := map[string]int{}
	for _, req := range host.pathRequests(samplerUsagePath)[before:] {
		keys[req.key]++
	}
	if len(keys) != 2 || keys["k1"] != 1 || keys["k3"] != 1 {
		t.Fatalf("grouped account keys = %v", keys)
	}
	for _, req := range host.snapshot() {
		if req.key == "k5" {
			t.Fatalf("credential proxy was bypassed: %+v", req)
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.creds[paused.ID].Disabled || !s.usageCache[paused.ID].value.CheckedAt.Equal(*s.usageCache["one"].value.CheckedAt) {
		t.Fatal("grouped sampling lost manual-disabled state or left stale usage")
	}
}

func TestUsageSamplerOnlyDisabledAccountKeepsManualState(t *testing.T) {
	host := newSamplerHost()
	host.accounts["disabled-key"] = "disabled-account"
	c := samplerCredential("disabled", "disabled-key")
	c.Disabled = true
	s := samplerService(t, host, c)
	quotaTestAuth(t, s, c)
	excluded := []Credential{samplerCredential("revoked", "revoked-key"), samplerCredential("empty", "")}
	for _, other := range excluded {
		s.creds[other.ID] = other
	}
	s.revoked["revoked"] = true
	if targets := s.usageSamplingTargets(time.Now()); len(targets) != 1 || targets[0] != c.ID {
		t.Fatalf("manual-disabled account was skipped or invalid credentials were sampled: %v", targets)
	}
	s.sampleUsage()
	if !samplerLedger(s, "ok", c.ID)() || len(host.snapshot()) != 1 || !s.creds[c.ID].Disabled {
		t.Fatal("background usage sampling failed or enabled a disabled account")
	}
	if quotaTestParse(t, NewService(), c.ID, quotaTestRead(t, s, c.ID))["Disabled"] != true {
		t.Fatal("background sampling changed persisted manual disable")
	}
}

func TestUsageSamplerReplacedKeyAndAuthFailureBackoff(t *testing.T) {
	host := newSamplerHost()
	host.accounts["old-key"], host.accounts["new-key"] = "acct-a", "acct-b"
	s := samplerService(t, host, samplerCredential("one", "old-key"))
	startSampler(t, s)
	waitSampler(t, samplerLedger(s, "ok", "one"))
	s.mu.RLock()
	oldIdentity := s.usageCache["one"].value.accountHash
	s.mu.RUnlock()

	s.mu.Lock()
	c := s.creds["one"]
	c.APIKey = "new-key"
	s.creds["one"] = c
	s.mu.Unlock()
	before := host.pathCount(samplerUsagePath)
	s.sampleUsage()
	fresh := host.pathRequests(samplerUsagePath)[before:]
	if len(fresh) != 1 || fresh[0].key != "new-key" {
		t.Fatalf("replaced key reused the old identity cache: %+v", fresh)
	}
	s.mu.RLock()
	newIdentity := s.usageCache["one"].value.accountHash
	s.mu.RUnlock()
	if newIdentity == oldIdentity || newIdentity == "" {
		t.Fatal("account identity was not re-resolved after the key was replaced")
	}

	host2 := newSamplerHost()
	host2.accounts["a"], host2.accounts["b"] = "acct-a", "acct-b"
	host2.failures["a"], host2.failures["b"] = 401, 429
	s2 := samplerService(t, host2, samplerCredential("a1", "a"), samplerCredential("b1", "b"))
	startSampler(t, s2)
	waitSampler(t, samplerLedger(s2, "unauthorized", "a1"))
	waitSampler(t, samplerLedger(s2, "rate_limited", "b1"))
	if got := host2.pathCount(samplerUsagePath); got != 2 {
		t.Fatalf("initial failed samples = %d", got)
	}
	ageSamplerCache(t, s2, time.Minute, "a1", "b1")
	if targets := s2.usageSamplingTargets(time.Now()); len(targets) != 0 {
		t.Fatalf("five minute backoff ignored: %v", targets)
	}
	s2.sampleUsage()
	if got := host2.pathCount(samplerUsagePath); got != 2 {
		t.Fatalf("retried inside the five minute backoff (%d)", got)
	}
	ageSamplerCache(t, s2, 6*time.Minute, "a1", "b1")
	if targets := s2.usageSamplingTargets(time.Now()); len(targets) != 2 {
		t.Fatalf("backoff never expired: %v", targets)
	}
	s2.sampleUsage()
	if got := host2.pathCount(samplerUsagePath); got != 4 {
		t.Fatalf("retry after backoff = %d", got)
	}
}

func TestUsageSamplerColdAccountTriesUnattemptedSibling(t *testing.T) {
	host := newSamplerHost()
	host.failures["expired-key"] = http.StatusUnauthorized
	host.accounts["working-key"] = "same-account"
	s := samplerService(t, host)
	for _, c := range []Credential{samplerCredential("a-expired", "expired-key"), samplerCredential("b-working", "working-key")} {
		c.QuotaIdentity = &QuotaIdentity{AccountHash: fixtureAccountHash("same-account"), KeyHash: quotaKeyHash(c)}
		quotaTestAuth(t, s, c)
	}

	s.sampleUsage()
	if got := host.pathRequests(samplerUsagePath); len(got) != 1 || got[0].key != "expired-key" {
		t.Fatalf("cold account sampled more than one key: %+v", got)
	}
	// Even after the first key's backoff expires, the untried sibling must win.
	ageSamplerCache(t, s, 6*time.Minute, "a-expired")
	failedAt := *s.usageCache["a-expired"].value.CheckedAt
	s.sampleUsage()
	if !samplerLedger(s, "ok", "b-working")() {
		t.Fatal("expired first key starved the working sibling after a cold start")
	}
	if !s.usageCache["a-expired"].value.CheckedAt.Equal(failedAt) || s.usageCache["a-expired"].value.Status != "unauthorized" {
		t.Fatal("successful sibling erased the failed key's own backoff")
	}
	s.sampleUsage()
	if got := host.pathCount(samplerUsagePath); got != 2 {
		t.Fatalf("fresh successful account was sampled again: %d requests", got)
	}
	ageSamplerCache(t, s, 2*usageTTL, "b-working")
	s.sampleUsage()
	got := host.pathRequests(samplerUsagePath)
	if len(got) != 3 || got[1].key != "working-key" || got[2].key != "working-key" {
		t.Fatalf("working sibling did not retain sampling priority: %+v", got)
	}
}

func TestUsageSamplerFailedAccountRotatesAndKeepsPerKeyBackoff(t *testing.T) {
	host := newSamplerHost()
	host.failures["first-key"], host.failures["second-key"] = http.StatusUnauthorized, http.StatusTooManyRequests
	s := samplerService(t, host)
	for _, c := range []Credential{samplerCredential("a-first", "first-key"), samplerCredential("b-second", "second-key")} {
		c.QuotaIdentity = &QuotaIdentity{AccountHash: "known-account", KeyHash: quotaKeyHash(c)}
		quotaTestAuth(t, s, c)
	}
	s.sampleUsage()
	s.sampleUsage()
	s.sampleUsage()
	if got := host.pathCount(samplerUsagePath); got != 2 {
		t.Fatalf("failed keys were retried inside their own backoff: %d requests", got)
	}
	// Both are due, but the lexically later key has waited longer.
	ageSamplerCache(t, s, 6*time.Minute, "a-first")
	ageSamplerCache(t, s, 7*time.Minute, "b-second")
	s.sampleUsage()
	s.sampleUsage()
	s.sampleUsage()
	got := host.pathRequests(samplerUsagePath)
	if len(got) != 4 || got[0].key != "first-key" || got[1].key != "second-key" || got[2].key != "second-key" || got[3].key != "first-key" {
		t.Fatalf("failed account did not rotate fairly or ignored per-key backoff: %+v", got)
	}
}

func TestUsageSamplingTargetsChecksAllSiblingsAndCurrentKeys(t *testing.T) {
	s := NewService()
	old := time.Now().Add(-2 * usageTTL)
	for _, id := range []string{"a-first", "b-second"} {
		c := samplerCredential(id, id+"-key")
		c.QuotaIdentity = &QuotaIdentity{AccountHash: "same-account", KeyHash: quotaKeyHash(c)}
		s.creds[id] = c
		s.usageCache[id] = &usageCacheEntry{fingerprint: usageFingerprint(c), value: credentialUsage{Status: "ok", CheckedAt: &old, accountHash: "same-account"}}
	}
	// The preferred first key is idle, but a manually refreshed sibling is busy.
	s.usageCache["b-second"].done = make(chan struct{})
	if targets := s.usageSamplingTargets(time.Now()); len(targets) != 0 {
		t.Fatalf("scheduled an account with an ongoing sibling fetch: %v", targets)
	}
	// A key rotation invalidates both the old verified identity and its cache,
	// including an ongoing fetch of the previous key.
	c := s.creds["b-second"]
	c.APIKey = "replacement-key"
	s.creds[c.ID] = c
	targets := s.usageSamplingTargets(time.Now())
	if len(targets) != 2 || targets[0] != "a-first" || targets[1] != "b-second" {
		t.Fatalf("replacement key inherited the previous key's identity or busy cache: %v", targets)
	}
}

func TestUsageSamplerShutdownStopsLoopAndClosesRead(t *testing.T) {
	host := newSamplerHost()
	host.accounts["k1"] = "acct-a"
	host.blockReads = true
	s := samplerService(t, host, samplerCredential("one", "k1"))
	startSampler(t, s)
	select {
	case <-host.readStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("sampler never reached the blocked stream_read")
	}

	done := make(chan struct{})
	go func() {
		_, _ = s.Handle("plugin.shutdown", nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("plugin.shutdown did not drain the blocked sampler read")
	}
	if host.closedCount() == 0 {
		t.Fatal("shutdown left the in-flight usage stream open")
	}
	stopped := len(host.snapshot())
	s.wakeUsageSampler()
	time.Sleep(50 * time.Millisecond)
	if got := len(host.snapshot()); got != stopped {
		t.Fatalf("usage sampler kept running after shutdown: %d -> %d", stopped, got)
	}
}
