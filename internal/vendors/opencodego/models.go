package opencodego

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/xiao-qiu-qiu/OCcodeBridge/internal/vendors"
)

// modelEndpoint is the catalog path appended to BaseURL.
const modelEndpoint = "/models"

// modelsEndpoint is the catalog URL. It is a variable so tests can point it at a
// local server instead of opencode.ai.
var modelsEndpoint = BaseURL + modelEndpoint

// chatEndpoint is the only endpoint this vendor advertises: every Go text model
// also answers on /chat/completions because the Console translates between the
// native APIs (opencode.ai/docs/console/go).
const chatEndpoint = "/chat/completions"

// ListModels reads the OpenAI-style catalog "{data:[{id,name,context_length}]}".
// Unknown fields are ignored; entries without an id are dropped.
func (Vendor) ListModels(ctx context.Context, c vendors.Credential, doer vendors.HTTPDoer) ([]vendors.Model, error) {
	if doer == nil {
		return nil, errors.New("opencode-go: ListModels needs an HTTP doer")
	}
	request, err := newRequest(ctx, modelsEndpoint, c)
	if err != nil {
		return nil, err
	}
	response, err := doer.Do(request)
	if err != nil {
		return nil, fmt.Errorf("opencode-go: models request failed: %w", err)
	}
	body, err := readBody(response)
	if err != nil {
		return nil, fmt.Errorf("opencode-go: models response unreadable: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &upstreamStatusError{op: "models", status: response.StatusCode, detail: bodySnippet(body, c.APIKey)}
	}
	var payload struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			ContextLength int    `json:"context_length"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("opencode-go: models response is not valid JSON: %w", err)
	}
	models := make([]vendors.Model, 0, len(payload.Data))
	for _, entry := range payload.Data {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			continue
		}
		models = append(models, vendors.Model{
			ID:            id,
			UpstreamID:    vendors.UpstreamID(ID, id),
			Name:          entry.Name,
			ContextLength: entry.ContextLength,
			Endpoints:     []string{chatEndpoint},
		})
	}
	return models, nil
}
