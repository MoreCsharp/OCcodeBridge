package bridge

import (
	"strconv"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestWindowUsagePausesUntilLatestResetThenRecovers(t *testing.T) {
	early := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	late := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	percent := 100.0
	s := usageTestService(t, func(map[string]any) (int, string) {
		return 200, `{"accountId":"acct","planName":"Fixture Plan","windows":[
			{"kind":"5h","percent":` + jsonNumber(percent) + `,"resetAt":"` + early.Format(time.RFC3339) + `"},
			{"kind":"7d","percent":` + jsonNumber(percent) + `,"resetAt":"` + late.Format(time.RFC3339) + `"}]}`
	})
	c := s.creds["one"]
	quotaTestAuth(t, s, c)
	value, err := s.credentialUsage(usageTestRequest())
	if err != nil || value.Status != "ok" {
		t.Fatalf("usage query failed: %v %v", value.Status, err)
	}
	block := s.creds[c.ID].QuotaBlock
	if block == nil || block.Permanent || block.RetryAt == nil || !block.RetryAt.Equal(late) {
		t.Fatalf("window pause must wait for the latest reset: %+v", block)
	}
	if len(block.Limits) != 2 || block.Reason != quotaReasonExhausted {
		t.Fatalf("unexpected block contents: %+v", block)
	}
	if _, err := s.selectedCredential(ExecutorRequest{StorageJSON: jsonBytes(c)}); statusOf(err) != 429 {
		t.Fatalf("an exhausted window stayed schedulable: %v", err)
	}
	// Polling recovery: a later sample under 100% clears the pause.
	percent = 2
	past := time.Now().Add(-2 * usageTTL)
	s.usageCache[c.ID].value.CheckedAt = &past
	if _, err := s.credentialUsage(usageTestRequest()); err != nil {
		t.Fatal(err)
	}
	if s.creds[c.ID].QuotaBlock != nil {
		t.Fatalf("a fresh sample under the limit did not resume scheduling: %+v", s.creds[c.ID].QuotaBlock)
	}
}

func jsonNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func TestPauseOnWindowLimitDisabledLeavesUsagePollingPassive(t *testing.T) {
	percent := 100.0
	s := usageTestService(t, func(map[string]any) (int, string) {
		return 200, `{"accountId":"acct","windows":[{"kind":"7d","percent":` + jsonNumber(percent) + `}]}`
	})
	if err := decodeConfigJSON([]byte(`{"pause_on_window_limit":false}`), &s.cfg); err != nil {
		t.Fatal(err)
	}
	c := s.creds["one"]
	quotaTestAuth(t, s, c)
	value, err := s.credentialUsage(usageTestRequest())
	if err != nil || value.Status != "ok" || *value.Limits[0].PercentUsed != 100 {
		t.Fatalf("usage query failed: %+v %v", value, err)
	}
	if s.creds[c.ID].QuotaBlock != nil {
		t.Fatalf("usage polling paused scheduling while the switch was off: %+v", s.creds[c.ID].QuotaBlock)
	}
	if _, err := s.selectedCredential(ExecutorRequest{StorageJSON: jsonBytes(c)}); err != nil {
		t.Fatalf("the account must stay schedulable: %v", err)
	}
	// The switch only stops polling-driven pausing; an upstream window 429 still
	// pauses the account.
	quotaErr := &upstreamError{APIError: &APIError{429, "upstream_quota_exhausted", "usage limit reached"}, QuotaType: "weekly"}
	if err := s.observeQuotaError(c, quotaErr); err != nil {
		t.Fatal(err)
	}
	if s.creds[c.ID].QuotaBlock == nil {
		t.Fatal("an upstream window limit must pause even with the switch off")
	}
}

func TestPauseOnWindowLimitDefaultsOnAndKeepsExplicitValue(t *testing.T) {
	cfg := defaultConfig()
	if !cfg.PauseOnWindowLimit {
		t.Fatal("default config must pause on a window limit")
	}
	cfg = defaultConfig()
	if err := decodeConfigJSON([]byte(`{"timeout_seconds":600}`), &cfg); err != nil || !cfg.PauseOnWindowLimit {
		t.Fatalf("an old settings file without the field must keep the default: %v", cfg.PauseOnWindowLimit)
	}
	cfg = defaultConfig()
	if err := decodeConfigJSON([]byte(`{"pause_on_window_limit":false}`), &cfg); err != nil || cfg.PauseOnWindowLimit {
		t.Fatalf("an explicit false must be kept: %v", cfg.PauseOnWindowLimit)
	}
	// A YAML seed without the field keeps the default too.
	cfg = defaultConfig()
	if err := yamlSeed(`nonstream_mode: native`, &cfg); err != nil || !cfg.PauseOnWindowLimit {
		t.Fatalf("YAML seed lost the default: %v, %v", cfg.PauseOnWindowLimit, err)
	}
}

func TestBlockWithoutResetTimeUsesFallbackRecheck(t *testing.T) {
	s := NewService()
	c := Credential{ID: "one", APIKey: "fixture-key", Vendor: testVendorID}
	quotaTestAuth(t, s, c)
	quotaErr := &upstreamError{APIError: &APIError{429, "upstream_quota_exhausted", "usage limit reached"}, QuotaType: "weekly"}
	if err := s.observeQuotaError(c, quotaErr); err != nil {
		t.Fatal(err)
	}
	block := s.creds[c.ID].QuotaBlock
	if block == nil || block.Permanent || block.RetryAt != nil {
		t.Fatalf("expected a window block without a reset time: %+v", block)
	}
	observed := block.ObservedAt
	empty := credentialUsage{Status: "ok"}
	if quotaRecoveryCheckDue(s.creds[c.ID], empty, observed.Add(quotaUnresetRecheck-time.Minute)) {
		t.Fatal("a fallback check must not run before its interval")
	}
	if !quotaRecoveryCheckDue(s.creds[c.ID], empty, observed.Add(quotaUnresetRecheck)) {
		t.Fatal("a block without a reset time must be re-checked on the fallback interval")
	}
	checked := observed.Add(quotaUnresetRecheck + time.Minute)
	if quotaRecoveryCheckDue(s.creds[c.ID], credentialUsage{Status: "ok", CheckedAt: &checked}, observed.Add(quotaUnresetRecheck+2*time.Minute)) {
		t.Fatal("a fresh sample already covers the fallback check")
	}
	// Polling still recovers it: the window reported below 100% clears the block.
	percent := 12.0
	value := credentialUsage{Status: "ok", UpdatedAt: &checked, CheckedAt: &checked, Limits: []usageLimit{{Type: "weekly", PercentUsed: &percent}}}
	if err := s.observeQuotaUsage(s.creds[c.ID], value, checked); err != nil {
		t.Fatal(err)
	}
	if s.creds[c.ID].QuotaBlock != nil {
		t.Fatalf("a block without a reset time never recovered: %+v", s.creds[c.ID].QuotaBlock)
	}
}

// yamlSeed mirrors what configure() does with CPA's config YAML.
func yamlSeed(raw string, cfg *Config) error {
	next := *cfg
	if err := yaml.Unmarshal([]byte(raw), &next); err != nil {
		return err
	}
	*cfg = next
	return nil
}
