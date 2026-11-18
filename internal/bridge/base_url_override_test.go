package bridge

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xiao-qiu-qiu/OCcodeBridge/internal/vendors"
)

func TestInferenceBaseOverridesOnlyItsOwnVendor(t *testing.T) {
	s := NewService()
	opencodeGo, ok := vendors.Get(requestVendorID)
	if !ok {
		t.Fatalf("%s is not registered", requestVendorID)
	}
	if base := s.inferenceBase(opencodeGo); base != opencodeGo.BaseURL() {
		t.Fatalf("default base = %q", base)
	}
	s.cfg.BaseURLOverrides = map[string]string{"commandcode": "http://127.0.0.1:8801/provider/v1/"}
	if base := s.inferenceBase(opencodeGo); base != opencodeGo.BaseURL() {
		t.Fatalf("another vendor's override rewrote %s: %q", opencodeGo.ID(), base)
	}
	commandCode, ok := vendors.Get("commandcode")
	if !ok {
		t.Fatal("commandcode is not registered")
	}
	if base := s.inferenceBase(commandCode); base != "http://127.0.0.1:8801/provider/v1" {
		t.Fatalf("commandcode override = %q", base)
	}
}

func TestBaseURLOverridesDecodingReplacesAndIgnoresLegacyField(t *testing.T) {
	cfg := defaultConfig()
	if err := decodeConfigJSON([]byte(`{"base_url":"https://api.cline.bot/api/v1"}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.BaseURLOverrides) != 0 {
		t.Fatalf("a legacy base_url still redirected every vendor: %v", cfg.BaseURLOverrides)
	}
	if strings.Contains(string(jsonBytes(cfg)), "base_url\\\"") {
		t.Fatal("config persistence still writes the legacy base_url field")
	}
	if err := decodeConfigJSON([]byte(`{"base_url_overrides":{"opencode-go":"http://127.0.0.1:9000/v1"}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURLOverrides["opencode-go"] != "http://127.0.0.1:9000/v1" {
		t.Fatalf("override not stored: %v", cfg.BaseURLOverrides)
	}
	// A settings update without the field keeps the stored map; an explicitly
	// supplied map replaces it, including an empty one.
	if err := decodeConfigJSON([]byte(`{"debug":true}`), &cfg); err != nil || len(cfg.BaseURLOverrides) != 1 {
		t.Fatalf("absent field must keep the override: %v", cfg.BaseURLOverrides)
	}
	if err := decodeConfigJSON([]byte(`{"base_url_overrides":{}}`), &cfg); err != nil || len(cfg.BaseURLOverrides) != 0 {
		t.Fatalf("an explicit empty map must clear the override: %v", cfg.BaseURLOverrides)
	}
}

func TestBaseURLOverridesValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"known vendor", `{"base_url_overrides":{"commandcode":"https://proxy.example/v1"}}`, false},
		{"blank value means default", `{"base_url_overrides":{"commandcode":"  "}}`, false},
		{"unknown vendor", `{"base_url_overrides":{"cline":"https://proxy.example/v1"}}`, true},
		{"credentials in url", `{"base_url_overrides":{"commandcode":"https://user:pass@proxy.example/v1"}}`, true},
		{"not http", `{"base_url_overrides":{"commandcode":"ftp://proxy.example/v1"}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultConfig()
			if err := decodeConfigJSON([]byte(tc.body), &cfg); err != nil {
				t.Fatal(err)
			}
			err := cfg.validate()
			if tc.wantErr && (err == nil || statusOf(err) != 400) {
				t.Fatalf("expected a 400 validation failure, got %v", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected validation failure: %v", err)
			}
		})
	}
}

func TestConfigResponseKeepsPerVendorOverrides(t *testing.T) {
	s := NewService()
	s.cfg.BaseURLOverrides = map[string]string{"opencode-go": "http://127.0.0.1:9001/v1"}
	response := s.config()
	if response.BaseURLOverrides["opencode-go"] != "http://127.0.0.1:9001/v1" {
		t.Fatalf("config() dropped the override: %v", response.BaseURLOverrides)
	}
	response.BaseURLOverrides["opencode-go"] = "http://mutated.invalid"
	if s.cfg.BaseURLOverrides["opencode-go"] != "http://127.0.0.1:9001/v1" {
		t.Fatal("config() handed out the live map")
	}
	var decoded map[string]any
	if json.Unmarshal(jsonBytes(s.config()), &decoded) != nil || decoded["base_url_overrides"] == nil {
		t.Fatal("management config JSON must carry base_url_overrides")
	}
}
