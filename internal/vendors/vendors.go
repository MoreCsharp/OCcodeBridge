// Package vendors defines the contract between the PlanBridge request path and a
// subscription vendor (OpenCode Go, CommandCode). It deliberately does not import
// internal/bridge: the bridge adapts its own types to these, so a vendor package
// can be developed and tested without touching executor, quota or UI code.
//
// See README.md in this directory for the full contract.
package vendors

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// ErrNotImplemented is returned by a vendor method that its stage does not cover yet.
var ErrNotImplemented = errors.New("vendor operation is not implemented yet")

// Model is one upstream model as listed by the vendor.
type Model struct {
	// ID is the vendor's own model id, without the vendor prefix.
	ID string
	// UpstreamID is what the bridge stores in a model mapping: "<vendor>/<ID>".
	UpstreamID string
	// Name is the human readable model name reported by the vendor.
	Name string
	// ContextLength is the vendor's context window in tokens; 0 means unknown.
	ContextLength int
	// Endpoints lists the request paths the vendor accepts for this model,
	// for example "/chat/completions". Empty means unknown, not "none".
	Endpoints []string
}

// Window kinds shared by every vendor. Windows a vendor does not report stay absent.
const (
	WindowFiveHour = "5h"
	WindowWeekly   = "7d"
	WindowDaily    = "daily"
	WindowMonthly  = "month"
)

// Window is one usage window. A nil number means "the vendor did not report it";
// callers must show that as unknown rather than as zero.
type Window struct {
	Kind        string
	UsedPercent *float64
	Used        *float64
	Cap         *float64
	ResetAt     *time.Time
	Status      string
}

// Usage is one usage sample for a credential.
type Usage struct {
	PlanName        string
	PeriodStart     *time.Time
	PeriodEnd       *time.Time
	Windows         []Window
	Balance         *float64
	BalanceCurrency string
	FetchedAt       time.Time
	// AccountID identifies the upstream subscription, so several keys of one
	// account share one usage sample. Empty means "unknown".
	AccountID string
}

// LimitBlock reports that a usage window is exhausted. ClassifyLimitError returns
// it only for a window limit, never for an ordinary throttle.
type LimitBlock struct {
	// Window is the exhausted window kind, one of the Window* constants, or ""
	// when the vendor names no window.
	Window string
	// ResetAt is when the window reopens; nil when the vendor does not say.
	ResetAt *time.Time
	// Permanent marks a limit that usage polling cannot clear, such as a plan
	// without API access. The bridge then stops scheduling instead of cooling down.
	Permanent bool
	// Reason is a short, non-secret explanation for the console.
	Reason string
}

// Observation collects vendor specific facts read from one upstream response frame.
type Observation struct {
	// Allowance is the raw allowance object reported for this request, if any.
	Allowance map[string]any
	// Raw preserves the frame fragment the allowance was read from.
	Raw json.RawMessage
}

// Credential is the subset of a stored credential a vendor may see.
type Credential struct {
	ID       string
	APIKey   string
	ProxyURL string
	// ZDR asks the vendor for zero data retention when it supports the switch.
	ZDR bool
}

// Request is the minimum a vendor needs to build request headers.
type Request struct {
	Credential Credential
	// Model is the upstream model id with the vendor prefix already removed.
	Model string
	// Stream reports whether the response will be streamed.
	Stream bool
	// SessionHint is the client's own session identity, when it sent one.
	SessionHint string
	// Payload is the request body the bridge is about to send.
	Payload []byte
	// Headers are the client's inbound headers.
	Headers http.Header
}

// HTTPDoer performs one HTTP request. The bridge injects its egress layer here so
// vendor packages never create their own client and stay testable offline.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Vendor is one subscription provider. Implementations live in their own package.
type Vendor interface {
	// ID is the stable vendor id stored on credentials, e.g. "opencode-go".
	ID() string
	DisplayName() string
	// BaseURL is the inference base; paths are appended by the caller.
	BaseURL() string
	// Headers returns the vendor owned request headers. The bridge adds
	// User-Agent and Content-Type only when absent.
	Headers(r Request) http.Header
	// ListModels returns the vendor catalog with prefixed UpstreamIDs.
	ListModels(ctx context.Context, c Credential, doer HTTPDoer) ([]Model, error)
	// FetchUsage reads the subscription usage. Unknown fields stay nil.
	FetchUsage(ctx context.Context, c Credential, doer HTTPDoer) (Usage, error)
	// ClassifyLimitError reports a window limit, or nil for anything else
	// (ordinary throttling included).
	ClassifyLimitError(status int, body []byte, now time.Time) *LimitBlock
	// ObserveResponse reads vendor specific response facts, or nil when the
	// frame carries none.
	ObserveResponse(frame map[string]any) *Observation
}

// UpstreamID builds the prefixed model id stored in a model mapping.
func UpstreamID(vendorID, model string) string {
	if vendorID == "" || model == "" {
		return model
	}
	return vendorID + "/" + model
}

// SplitUpstreamID separates the leading vendor segment from an upstream model id.
// CommandCode model ids may contain "/" themselves, so only the first segment is
// treated as the vendor.
func SplitUpstreamID(upstreamID string) (vendorID, model string) {
	vendorID, model, found := strings.Cut(upstreamID, "/")
	if !found {
		return "", upstreamID
	}
	return vendorID, model
}
