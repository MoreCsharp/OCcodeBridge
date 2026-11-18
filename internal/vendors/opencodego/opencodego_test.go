package opencodego

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/MoreCsharp/OCcodeBridge/internal/vendors"
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

func TestHeadersSendBearerAndClientSession(t *testing.T) {
	v := Vendor{}
	header := v.Headers(vendors.Request{
		Credential:  vendors.Credential{ID: "cred-1", APIKey: "sk-fixture"},
		SessionHint: "ses_client_supplied",
		Payload:     []byte(`{"messages":[{"role":"user","content":"ignored when the client names its session"}]}`),
	})
	if got := header.Get("Authorization"); got != "Bearer sk-fixture" {
		t.Fatalf("authorization = %q", got)
	}
	if got := header.Get(SessionHeader); got != "ses_client_supplied" {
		t.Fatalf("session = %q, the client's own session must win", got)
	}
	if got := header.Get("User-Agent"); got != "" {
		t.Fatalf("vendor set User-Agent %q; the bridge owns it", got)
	}
}

func TestHeadersDeriveStableSessionPerConversation(t *testing.T) {
	v := Vendor{}
	credential := vendors.Credential{ID: "cred-1", APIKey: "sk-fixture"}
	session := func(payload []byte, c vendors.Credential) string {
		return v.Headers(vendors.Request{Credential: c, Payload: payload}).Get(SessionHeader)
	}
	first := session([]byte(`{"messages":[{"role":"system","content":"you are a coding agent"},{"role":"user","content":"fix the build"}]}`), credential)
	if len(first) != sessionIDLength {
		t.Fatalf("derived session = %q, want %d hex characters", first, sessionIDLength)
	}
	// A later turn repeats the first system/user messages, so the id must hold.
	nextTurn := []byte(`{"messages":[{"role":"system","content":"you are a coding agent"},{"role":"user","content":"fix the build"},{"role":"assistant","content":"done"},{"role":"user","content":"now ship it"}]}`)
	if got := session(nextTurn, credential); got != first {
		t.Fatalf("second turn session = %q, want the first turn's %q", got, first)
	}
	otherConversation := []byte(`{"messages":[{"role":"system","content":"you are a coding agent"},{"role":"user","content":"write the docs"}]}`)
	if got := session(otherConversation, credential); got == first {
		t.Fatal("two conversations share one session id")
	}
	otherCredential := vendors.Credential{ID: "cred-2", APIKey: "sk-fixture"}
	if got := session(otherConversation, otherCredential); got == session(otherConversation, credential) {
		t.Fatal("two credentials share one session id")
	}
	// Only the first 4 KiB of each excerpt is hashed, so a long shared opening is
	// still one conversation.
	longOpening := strings.Repeat("context ", 1024) // 8 KiB, well over the excerpt cap
	firstLong := session([]byte(`{"messages":[{"role":"user","content":"`+longOpening+`one"}]}`), credential)
	secondLong := session([]byte(`{"messages":[{"role":"user","content":"`+longOpening+`two"}]}`), credential)
	if firstLong != secondLong {
		t.Fatal("the session id depends on prompt text beyond the excerpt cap")
	}
}

func TestHeadersSessionReadsPartsArray(t *testing.T) {
	v := Vendor{}
	credential := vendors.Credential{ID: "cred-1", APIKey: "sk-fixture"}
	plain := v.Headers(vendors.Request{Credential: credential, Payload: []byte(`{"messages":[{"role":"user","content":"hello there"}]}`)}).Get(SessionHeader)
	parts := v.Headers(vendors.Request{Credential: credential, Payload: []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello "},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}},{"type":"text","text":"there"}]}]}`)}).Get(SessionHeader)
	if plain != parts {
		t.Fatalf("parts array session = %q, plain string session = %q; text parts must hash alike", parts, plain)
	}
}

func TestHeadersSessionFallsBackToCredentialAndModel(t *testing.T) {
	v := Vendor{}
	credential := vendors.Credential{ID: "cred-1", APIKey: "sk-fixture"}
	for _, payload := range [][]byte{nil, []byte("not json"), []byte(`{"messages":[]}`), []byte(`{"input":"responses style"}`)} {
		got := v.Headers(vendors.Request{Credential: credential, Model: "kimi-k3", Payload: payload}).Get(SessionHeader)
		if want := hashSession("cred-1", "kimi-k3"); got != want {
			t.Fatalf("fallback session for %q = %q, want %q", payload, got, want)
		}
	}
	kimi := v.Headers(vendors.Request{Credential: credential, Model: "kimi-k3"}).Get(SessionHeader)
	other := v.Headers(vendors.Request{Credential: credential, Model: "glm-5.2"}).Get(SessionHeader)
	if kimi == other {
		t.Fatal("two models share one fallback session id")
	}
}

func TestListModelsReadsCatalog(t *testing.T) {
	server, doer := serveFixture(t, http.StatusOK, fixture(t, "models.json"))
	withModelsEndpoint(t, server.URL)
	models, err := Vendor{}.ListModels(t.Context(), vendors.Credential{ID: "cred-1", APIKey: "sk-models"}, doer)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	assertBearer(t, doer.request, server.URL, "sk-models")
	want := []vendors.Model{
		{ID: "kimi-k3", UpstreamID: "opencode-go/kimi-k3", Name: "Kimi K3", ContextLength: 262144, Endpoints: []string{"/chat/completions"}},
		{ID: "deepseek-v4.1-flash", UpstreamID: "opencode-go/deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash", ContextLength: 131072, Endpoints: []string{"/chat/completions"}},
		{ID: "glm-5.2", UpstreamID: "opencode-go/glm-5.2", Name: "GLM 5.2", Endpoints: []string{"/chat/completions"}},
	}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("models = %#v, want %#v", models, want)
	}
}

func TestListModelsReportsAuthFailureWithoutTheKey(t *testing.T) {
	const secret = "sk-secret-models"
	server, doer := serveFixture(t, http.StatusUnauthorized, `{"error":{"message":"invalid api key `+secret+`"}}`)
	withModelsEndpoint(t, server.URL)
	_, err := Vendor{}.ListModels(t.Context(), vendors.Credential{ID: "cred-1", APIKey: secret}, doer)
	if err == nil {
		t.Fatal("401 catalog must be an error")
	}
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("error %v is not recognizable as an auth failure", err)
	}
	if status, ok := err.(interface{ StatusCode() int }); !ok || status.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("401 status not carried through: %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaks the API key: %v", err)
	}
}

func TestListModelsRejectsMalformedCatalog(t *testing.T) {
	server, doer := serveFixture(t, http.StatusOK, `{"data":[`)
	withModelsEndpoint(t, server.URL)
	if _, err := (Vendor{}).ListModels(t.Context(), vendors.Credential{APIKey: "sk-models"}, doer); err == nil {
		t.Fatal("malformed catalog JSON must be an error")
	}
}

func TestListModelsPropagatesDoerFailure(t *testing.T) {
	doer := failingDoer{err: errors.New("offline")}
	if _, err := (Vendor{}).ListModels(t.Context(), vendors.Credential{APIKey: "sk-models"}, doer); err == nil {
		t.Fatal("doer failure must be an error")
	}
}
