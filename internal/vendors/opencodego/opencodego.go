// Package opencodego implements the OpenCode Go vendor. This stage only carries
// the identity, inference base and request headers; the catalog, usage and
// limit-error work belongs to the OpenCode Go stage.
package opencodego

import (
	"context"
	"net/http"
	"time"

	"github.com/xiao-qiu-qiu/PlanBridge/internal/vendors"
)

// ID is the vendor id stored on credentials and used as the model prefix.
const ID = "opencode-go"

// SessionHeader names the per-conversation session id OpenCode expects.
const SessionHeader = "x-opencode-session"

// BaseURL is the documented inference endpoint. The legacy zen path stays
// reachable, but new traffic uses this one.
const BaseURL = "https://opencode.ai/inference/go/v1"

// Vendor is the OpenCode Go implementation of vendors.Vendor.
type Vendor struct{}

func init() { vendors.Register(Vendor{}) }

func (Vendor) ID() string          { return ID }
func (Vendor) DisplayName() string { return "OpenCode Go" }
func (Vendor) BaseURL() string     { return BaseURL }

// Headers sends the bearer key and forwards the client's own session id when it
// has one. Deriving a stable session id from the credential and prompt is the
// OpenCode Go stage's job.
func (Vendor) Headers(r vendors.Request) http.Header {
	h := http.Header{"Authorization": []string{"Bearer " + r.Credential.APIKey}}
	if r.SessionHint != "" {
		h.Set(SessionHeader, r.SessionHint)
	}
	return h
}

func (Vendor) ListModels(context.Context, vendors.Credential, vendors.HTTPDoer) ([]vendors.Model, error) {
	return nil, vendors.ErrNotImplemented
}

func (Vendor) FetchUsage(context.Context, vendors.Credential, vendors.HTTPDoer) (vendors.Usage, error) {
	return vendors.Usage{}, vendors.ErrNotImplemented
}

// ClassifyLimitError reports nothing yet: an unclassified 429 must stay a
// transient rate limit rather than pausing an account.
func (Vendor) ClassifyLimitError(int, []byte, time.Time) *vendors.LimitBlock { return nil }

func (Vendor) ObserveResponse(map[string]any) *vendors.Observation { return nil }
