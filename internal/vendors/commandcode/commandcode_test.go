package commandcode

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/xiao-qiu-qiu/PlanBridge/internal/vendors"
)

func TestVendorIdentity(t *testing.T) {
	v := Vendor{}
	if v.ID() != "commandcode" || v.DisplayName() != "CommandCode" || v.BaseURL() != "https://api.commandcode.ai/provider/v1" {
		t.Fatalf("unexpected identity: %s / %s / %s", v.ID(), v.DisplayName(), v.BaseURL())
	}
	if registered, ok := vendors.Get(v.ID()); !ok || registered.ID() != v.ID() {
		t.Fatal("CommandCode did not register itself")
	}
}

func TestHeadersSendBearerAndOptionalZDR(t *testing.T) {
	v := Vendor{}
	h := v.Headers(vendors.Request{Credential: vendors.Credential{APIKey: "sk-fixture"}})
	if h.Get("Authorization") != "Bearer sk-fixture" {
		t.Fatalf("authorization = %q", h.Get("Authorization"))
	}
	if _, set := h[http.CanonicalHeaderKey(ZDRHeader)]; set {
		t.Fatal("the ZDR switch is off by default")
	}
	h = v.Headers(vendors.Request{Credential: vendors.Credential{APIKey: "sk-fixture", ZDR: true}})
	if h.Get(ZDRHeader) != "1" {
		t.Fatalf("zdr header = %q", h.Get(ZDRHeader))
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
	if v.ClassifyLimitError(403, []byte(`{"error":{"code":"upgrade_required"}}`), time.Now()) != nil {
		t.Fatal("no limit classification is implemented yet")
	}
	if v.ObserveResponse(map[string]any{"id": "x"}) != nil {
		t.Fatal("CommandCode reports no observation yet")
	}
}
