package commandcode

import (
	"net/http"
	"testing"

	"github.com/MoreCsharp/OCcodeBridge/internal/vendors"
)

// modelsPath is the catalog endpoint the fixtures answer.
const modelsFixturePath = "/provider/v1/models"

func TestListModelsKeepsOnlyChatCompletionsModels(t *testing.T) {
	doer := &fakeDoer{responses: map[string]fakeResponse{
		modelsFixturePath: {body: readFixture(t, "models.json")},
	}}
	models, err := Vendor{}.ListModels(t.Context(), vendors.Credential{APIKey: "sk-fixture"}, doer)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	// The recorded catalog holds 104 models, 92 of them on /chat/completions.
	if len(models) != 92 {
		t.Fatalf("model count = %d, want 92", len(models))
	}
	for _, model := range models {
		if model.ID == "claude-sonnet-5-5" {
			t.Fatal("Claude models answer on /messages only and must not be offered")
		}
		if model.UpstreamID != "commandcode/"+model.ID {
			t.Fatalf("upstream id = %q for model %q", model.UpstreamID, model.ID)
		}
	}
	request := doer.requestTo(t, modelsFixturePath)
	if request.URL.String() != "https://api.commandcode.ai/provider/v1/models" {
		t.Fatalf("catalog URL = %s", request.URL)
	}
	if request.Header.Get("Authorization") != "Bearer sk-fixture" {
		t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
	}
	if request.Header.Get("User-Agent") == "" {
		t.Fatal("the catalog requires a User-Agent")
	}
}

func TestListModelsKeepsModelIDsContainingSlash(t *testing.T) {
	doer := &fakeDoer{responses: map[string]fakeResponse{
		modelsFixturePath: {body: readFixture(t, "models.json")},
	}}
	models, err := Vendor{}.ListModels(t.Context(), vendors.Credential{}, doer)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	var found *vendors.Model
	for i := range models {
		if models[i].ID == "deepseek/deepseek-v4-flash" {
			found = &models[i]
		}
	}
	if found == nil {
		t.Fatal("deepseek/deepseek-v4-flash is missing from the catalog")
	}
	if found.UpstreamID != "commandcode/deepseek/deepseek-v4-flash" {
		t.Fatalf("upstream id = %q", found.UpstreamID)
	}
	if found.ContextLength != 1000000 || found.Name != "DeepSeek V4 Flash (latest)" {
		t.Fatalf("model = %+v", *found)
	}
	if len(found.Endpoints) != 2 || found.Endpoints[0] != "/chat/completions" {
		t.Fatalf("endpoints = %v", found.Endpoints)
	}
}

func TestListModelsAllowsAnonymousCatalog(t *testing.T) {
	doer := &fakeDoer{responses: map[string]fakeResponse{
		modelsFixturePath: {body: `{"object":"list","data":[{"id":"public-model","name":"Public"}]}`},
	}}
	models, err := Vendor{}.ListModels(t.Context(), vendors.Credential{}, doer)
	if err != nil {
		t.Fatalf("ListModels without a key: %v", err)
	}
	if len(models) != 1 || models[0].UpstreamID != "commandcode/public-model" {
		t.Fatalf("models = %+v", models)
	}
	if len(models[0].Endpoints) != 0 {
		t.Fatalf("endpoints = %v, want unknown", models[0].Endpoints)
	}
	if _, set := doer.requestTo(t, modelsFixturePath).Header["Authorization"]; set {
		t.Fatal("an anonymous catalog request must not send Authorization")
	}
}

func TestListModelsKeepsUnknownEndpointsAndNormalizesThem(t *testing.T) {
	doer := &fakeDoer{responses: map[string]fakeResponse{
		modelsFixturePath: {body: `{"data":[
			{"id":"mystery-model","name":"Mystery"},
			{"id":"legacy-model","name":"Legacy","supported_endpoints":["chat/completions"]},
			{"id":"messages-model","name":"Messages","supported_endpoints":["/messages"]},
			{"id":"","name":"Blank"}
		]}`},
	}}
	models, err := Vendor{}.ListModels(t.Context(), vendors.Credential{}, doer)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	if len(models) != 2 || ids[0] != "mystery-model" || ids[1] != "legacy-model" {
		t.Fatalf("models = %v", ids)
	}
}

func TestListModelsReportsUnauthorized(t *testing.T) {
	doer := &fakeDoer{responses: map[string]fakeResponse{
		modelsFixturePath: {status: http.StatusUnauthorized, body: `{"error":{"type":"authentication_error","message":"Missing or invalid auth"}}`},
	}}
	_, err := Vendor{}.ListModels(t.Context(), vendors.Credential{APIKey: "sk-secret-fixture"}, doer)
	if err == nil {
		t.Fatal("a 401 must not be reported as an empty catalog")
	}
	if statusCodeOf(err) != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", statusCodeOf(err))
	}
	if err.Error() != "commandcode: /provider/v1/models returned HTTP 401" {
		t.Fatalf("error = %q, want the status only", err.Error())
	}
}
