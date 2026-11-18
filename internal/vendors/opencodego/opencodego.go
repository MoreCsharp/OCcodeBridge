// Package opencodego implements the OpenCode Go vendor: identity, request
// headers (including the per-conversation session id), the model catalog, the
// usage sample, the Go usage-window 429 and the per-response allowance.
//
// The usage and limit-error shapes here were derived from the public Go docs and
// the community usage extension, not from a real API key. The spec (§5) still
// lists both as unverified, so parsing stays tolerant and never turns a missing
// field into a zero.
package opencodego

import (
	"net/http"

	"github.com/xiao-qiu-qiu/PlanBridge/internal/vendors"
)

// ID is the vendor id stored on credentials and used as the model prefix.
const ID = "opencode-go"

// SessionHeader names the per-conversation session id OpenCode Go expects.
const SessionHeader = "x-opencode-session"

// BaseURL is the documented inference base. The legacy zen path is still online,
// but new traffic uses this one.
const BaseURL = "https://opencode.ai/inference/go/v1"

// Vendor is the OpenCode Go implementation of vendors.Vendor.
type Vendor struct{}

func init() { vendors.Register(Vendor{}) }

func (Vendor) ID() string          { return ID }
func (Vendor) DisplayName() string { return "OpenCode Go" }
func (Vendor) BaseURL() string     { return BaseURL }

// Headers sends the bearer key and a stable per-conversation session id. The
// client's own session header wins; otherwise the id is derived from the
// credential and the first system/user messages (see requestSession).
func (Vendor) Headers(r vendors.Request) http.Header {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+r.Credential.APIKey)
	header.Set(SessionHeader, requestSession(r))
	return header
}

// FetchUsage lives in usage.go, ListModels in models.go, ClassifyLimitError in
// limit.go and ObserveResponse in observe.go.
var _ vendors.Vendor = Vendor{}
