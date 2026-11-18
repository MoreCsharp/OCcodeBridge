// Package commandcode implements the CommandCode vendor: the documented Provider
// API for inference and the CLI's /alpha endpoints for subscription usage.
package commandcode

import (
	"net/http"

	"github.com/MoreCsharp/OCcodeBridge/internal/vendors"
)

// ID is the vendor id stored on credentials and used as the model prefix.
const ID = "commandcode"

// ZDRHeader switches the upstream to zero data retention providers.
const ZDRHeader = "x-cmd-zdr"

// SessionHeader carries the session id. The CLI sends it on every request named
// x-session-id (buildCommandAuthHeaders in cli.mjs), and forwarding the client's
// own id keeps upstream caching and rate accounting stable.
const SessionHeader = "x-session-id"

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
	if r.SessionHint != "" {
		h.Set(SessionHeader, r.SessionHint)
	}
	return h
}

// ObserveResponse reports nothing: CommandCode response frames carry no vendor
// owned usage facts. Window limits come from the billing endpoints.
func (Vendor) ObserveResponse(map[string]any) *vendors.Observation { return nil }
