package commandcode

import (
	"context"
	"strings"

	"github.com/xiao-qiu-qiu/PlanBridge/internal/vendors"
)

// modelsPath is appended to BaseURL for the catalog.
const modelsPath = "/models"

// chatCompletionsEndpoint is the only endpoint P0 sends.
const chatCompletionsEndpoint = "/chat/completions"

// modelEntry is one row of GET /provider/v1/models.
type modelEntry struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	ContextLength      int      `json:"context_length"`
	SupportedEndpoints []string `json:"supported_endpoints"`
}

type modelList struct {
	Data []modelEntry `json:"data"`
}

// ListModels returns the models that answer on /chat/completions: CommandCode
// lists Claude models for /messages only, and those stay out until that endpoint
// is wired. A model whose supported_endpoints is absent is kept with unknown
// endpoints, because the vendor did not say the model is unusable.
func (Vendor) ListModels(ctx context.Context, c vendors.Credential, doer vendors.HTTPDoer) ([]vendors.Model, error) {
	var list modelList
	// The catalog is public: without a key the request goes out anonymously.
	if err := getJSON(ctx, c, doer, BaseURL+modelsPath, &list); err != nil {
		return nil, err
	}
	models := make([]vendors.Model, 0, len(list.Data))
	for _, entry := range list.Data {
		id := strings.TrimSpace(entry.ID)
		if id == "" || !servesChatCompletions(entry.SupportedEndpoints) {
			continue
		}
		models = append(models, vendors.Model{
			ID:            id,
			UpstreamID:    vendors.UpstreamID(ID, id),
			Name:          entry.Name,
			ContextLength: entry.ContextLength,
			Endpoints:     entry.SupportedEndpoints,
		})
	}
	return models, nil
}

// servesChatCompletions reports whether a model lists /chat/completions. An empty
// list means the vendor did not report endpoints, not that the model has none.
func servesChatCompletions(endpoints []string) bool {
	if len(endpoints) == 0 {
		return true
	}
	for _, endpoint := range endpoints {
		if normalizeEndpoint(endpoint) == chatCompletionsEndpoint {
			return true
		}
	}
	return false
}

func normalizeEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" || strings.HasPrefix(endpoint, "/") {
		return endpoint
	}
	return "/" + endpoint
}
