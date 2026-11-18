// Package commandcode implements the CommandCode vendors. This stage only carries
// the identity, inference base and request headers; the catalog, usage and
// limit-error work belongs to the CommandCode stage.
package commandcode

import (
	"context"
	"net/http"
	"time"

	"github.com/xiao-qiu-qiu/PlanBridge/internal/vendors"
)

// ID is the vendor id stored on credentials and used as the model prefix.
const ID = "commandcode"

// ZDRHeader switches the upstream to zero data retention providers.
const ZDRHeader = "x-cmd-zdr"

// BaseURL is the inference endpoint; the /alpha usage calls live on the host root.
const BaseURL = "https://api.commandcode.ai/provider/v1"

// Vendor is the CommandCode implementation of vendors.Vendor.
type Vendor struct{}

func init() { vendors.Register(Vendor{}) }

func (Vendor) ID() string          { return ID }
func (Vendor) DisplayName() string { return "CommandCode" }
func (Vendor) BaseURL() string     { return BaseURL }

// Headers sends the bearer key; the CLI and the API share one key. The ZDR
// switch is per credential and off by default.
func (Vendor) Headers(r vendors.Request) http.Header {
	h := http.Header{"Authorization": []string{"Bearer " + r.Credential.APIKey}}
	if r.Credential.ZDR {
		h.Set(ZDRHeader, "1")
	}
	return h
}

func (Vendor) ListModels(context.Context, vendors.Credential, vendors.HTTPDoer) ([]vendors.Model, error) {
	return nil, vendors.ErrNotImplemented
}

func (Vendor) FetchUsage(context.Context, vendors.Credential, vendors.HTTPDoer) (vendors.Usage, error) {
	return vendors.Usage{}, vendors.ErrNotImplemented
}

// ClassifyLimitError reports nothing yet. CommandCode also returns 403 for plans
// without API access, which is a vendor classification, not a transient limit.
func (Vendor) ClassifyLimitError(int, []byte, time.Time) *vendors.LimitBlock { return nil }

func (Vendor) ObserveResponse(map[string]any) *vendors.Observation { return nil }
