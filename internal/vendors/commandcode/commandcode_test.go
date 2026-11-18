package commandcode

import (
	"net/http"
	"testing"

	"github.com/MoreCsharp/OCcodeBridge/internal/vendors"
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
	if _, set := h[http.CanonicalHeaderKey(SessionHeader)]; set {
		t.Fatal("no session header without a client session hint")
	}

	h = v.Headers(vendors.Request{Credential: vendors.Credential{APIKey: "sk-fixture", ZDR: true}})
	if h.Get(ZDRHeader) != "1" {
		t.Fatalf("zdr header = %q", h.Get(ZDRHeader))
	}

	h = v.Headers(vendors.Request{Credential: vendors.Credential{APIKey: "sk-fixture"}, SessionHint: "session-1"})
	if h.Get(SessionHeader) != "session-1" {
		t.Fatalf("session header = %q", h.Get(SessionHeader))
	}
}

func TestObserveResponseReadsNoVendorFacts(t *testing.T) {
	if observation := (Vendor{}).ObserveResponse(map[string]any{"id": "x", "usage": map[string]any{"input_tokens": 1}}); observation != nil {
		t.Fatalf("observation = %+v, want nil", observation)
	}
}

func TestPlansResolveByLongestPrefix(t *testing.T) {
	cases := map[string]string{
		"individual-go":     "Go",
		"individual_go_v1":  "Go",
		"individual_goat":   "GOAT",
		"individual-pro":    "Pro",
		"individual-pro-v1": "Pro",
		"individual-max":    "Max",
		"teams-pro":         "Teams Pro",
		"unknown-plan":      "",
	}
	for planID, name := range cases {
		resolved, ok := matchPlan(planID)
		if name == "" {
			if ok {
				t.Fatalf("plan %q resolved to %+v, want unknown", planID, resolved)
			}
			continue
		}
		if !ok || resolved.Name != name {
			t.Fatalf("plan %q resolved to %+v (found %v), want %q", planID, resolved, ok, name)
		}
	}
	if pro, _ := matchPlan("individual-pro-v1"); pro.MonthlyCredits != 80 {
		t.Fatalf("individual-pro-v1 monthly credits = %v, want 80", pro.MonthlyCredits)
	}
}
