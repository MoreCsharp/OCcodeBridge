package opencodego

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/xiao-qiu-qiu/PlanBridge/internal/vendors"
)

func TestVendorIdentity(t *testing.T) {
	v := Vendor{}
	if v.ID() != "opencode-go" || v.DisplayName() != "OpenCode Go" || v.BaseURL() != "https://opencode.ai/inference/go/v1" {
		t.Fatalf("unexpected identity: %s / %s / %s", v.ID(), v.DisplayName(), v.BaseURL())
	}
	if registered, ok := vendors.Get(v.ID()); !ok || registered.ID() != v.ID() {
		t.Fatal("OpenCode Go did not register itself")
	}
}

func TestHeadersSendBearerAndForwardSessionHint(t *testing.T) {
	v := Vendor{}
	h := v.Headers(vendors.Request{Credential: vendors.Credential{APIKey: "sk-fixture"}})
	if h.Get("Authorization") != "Bearer sk-fixture" {
		t.Fatalf("authorization = %q", h.Get("Authorization"))
	}
	if _, set := h[http.CanonicalHeaderKey(SessionHeader)]; set {
		t.Fatal("a request without a client session must not invent one")
	}
	h = v.Headers(vendors.Request{Credential: vendors.Credential{APIKey: "sk-fixture"}, SessionHint: "sess-1"})
	if h.Get(SessionHeader) != "sess-1" {
		t.Fatalf("session header = %q", h.Get(SessionHeader))
	}
}

func TestUnimplementedCallsReportItInsteadOfGuessing(t *testing.T) {
	v := Vendor{}
	if _, err := v.ListModels(t.Context(), vendors.Credential{}, nil); !errors.Is(err, vendors.ErrNotImplemented) {
		t.Fatalf("ListModels error = %v", err)
	}
	if _, err := v.FetchUsage(t.Context(), vendors.Credential{}, nil); !errors.Is(err, vendors.ErrNotImplemented) {
		t.Fatalf("FetchUsage error = %v", err)
	}
	if v.ClassifyLimitError(429, []byte(`{"error":{"message":"usage limit reached"}}`), time.Now()) != nil {
		t.Fatal("an unclassified 429 must stay a transient rate limit")
	}
	if v.ObserveResponse(map[string]any{"opencode": map[string]any{"allowance": 1}}) != nil {
		t.Fatal("no allowance observation is implemented yet")
	}
}
