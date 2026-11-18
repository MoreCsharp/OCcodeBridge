package bridge

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/xiao-qiu-qiu/PlanBridge/internal/vendors"
	// Blank imports register every vendor package this build ships.
	_ "github.com/xiao-qiu-qiu/PlanBridge/internal/vendors/commandcode"
	_ "github.com/xiao-qiu-qiu/PlanBridge/internal/vendors/opencodego"
)

// knownVendor reports whether this build implements the vendor id.
func knownVendor(id string) bool {
	_, ok := vendors.Get(id)
	return ok
}

// vendorFor resolves the vendor a credential belongs to. A credential without a
// known vendor is refused for scheduling instead of being guessed, so old data
// missing the field cannot silently reach the wrong upstream.
func vendorFor(c Credential) (vendors.Vendor, error) {
	if strings.TrimSpace(c.Vendor) == "" {
		return nil, fail(400, "凭据未选择厂商：请在「凭据管理 → 编辑」中补选 OpenCode Go 或 CommandCode（可用："+strings.Join(vendors.IDs(), " / ")+"）")
	}
	v, ok := vendors.Get(c.Vendor)
	if !ok {
		return nil, fail(400, "凭据的厂商不受支持："+c.Vendor)
	}
	return v, nil
}

// inferenceBase is where this credential's requests go: the vendor owns its base
// and Config.BaseURL is only an override for tests and self-hosted proxies.
func (s *Service) inferenceBase(v vendors.Vendor) string {
	if base := strings.TrimRight(s.config().BaseURL, "/"); base != "" {
		return base
	}
	return strings.TrimRight(v.BaseURL(), "/")
}

// vendorHeaders builds the upstream headers for one request. The vendor owns its
// own headers; the bridge supplies the identifying User-Agent and JSON content
// type the vendor does not override.
func (s *Service) vendorHeaders(c Credential, v vendors.Vendor, r vendors.Request) http.Header {
	h := v.Headers(r)
	if h == nil {
		h = http.Header{}
	}
	if h.Get("User-Agent") == "" {
		h.Set("User-Agent", "PlanBridge/"+Version)
	}
	if h.Get("Content-Type") == "" {
		h.Set("Content-Type", "application/json")
	}
	return h
}

// vendorRequestView narrows a bridge request to the vendor contract.
func vendorRequestView(r ExecutorRequest, c Credential, model string, stream bool, payload []byte) vendors.Request {
	return vendors.Request{
		Credential:  vendors.Credential{ID: c.ID, APIKey: c.APIKey, ProxyURL: c.ProxyURL, ZDR: c.ZDR},
		Model:       model,
		Stream:      stream,
		SessionHint: clientSessionHint(r.Headers),
		Payload:     payload,
		Headers:     r.Headers,
	}
}

// sessionHeaders are the headers clients already use to identify a conversation.
// Forwarding one keeps upstream caching and rate accounting stable.
var sessionHeaders = []string{
	"x-opencode-session",
	"x-claude-code-session-id",
	"x-session-id",
	"session_id",
}

func clientSessionHint(h http.Header) string {
	for _, name := range sessionHeaders {
		if value := strings.TrimSpace(h.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

// hostDoer adapts the bridge's egress layer (host callback or configured proxy
// pool) to vendors.HTTPDoer, so vendors never open their own connections.
type hostDoer struct {
	service    *Service
	credential Credential
	callbackID string
	deadline   time.Time
}

func (d hostDoer) Do(req *http.Request) (*http.Response, error) {
	if d.deadline.IsZero() {
		return nil, requestTimedOut("vendor request has no deadline")
	}
	var body []byte
	if req.Body != nil {
		var err error
		if body, err = io.ReadAll(req.Body); err != nil {
			return nil, err
		}
		_ = req.Body.Close()
	}
	payload := map[string]any{"host_callback_id": d.callbackID, "method": req.Method, "url": req.URL.String(), "headers": req.Header, "body": body}
	// Vendor management calls (usage, catalog) are management-plane requests, so a
	// credential proxy applies even when no management callback id was supplied.
	up, err := d.service.openUpstream(d.service.egressCredentialPayload(payload, d.credential, true), d.deadline)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	readErr := d.service.read(up, func(chunk []byte) error {
		buf.Write(chunk)
		return nil
	})
	d.service.closeUpstream(up.StreamID)
	if readErr != nil {
		return nil, readErr
	}
	headers := up.Headers
	if headers == nil {
		headers = http.Header{}
	}
	return &http.Response{StatusCode: up.StatusCode, Header: headers, Body: io.NopCloser(bytes.NewReader(buf.Bytes())), Request: req}, nil
}

func (s *Service) vendorDoer(c Credential, callbackID string, deadline time.Time) vendors.HTTPDoer {
	if deadline.IsZero() {
		deadline = time.Now().Add(time.Duration(s.config().TimeoutSeconds) * time.Second)
	}
	return hostDoer{service: s, credential: c, callbackID: callbackID, deadline: deadline}
}

// vendorContext keeps the vendor call bounded even when it ignores the doer.
func vendorContext(deadline time.Time) (context.Context, context.CancelFunc) {
	return context.WithDeadline(context.Background(), deadline)
}

// observeVendorFrame records vendor specific facts (such as a reported allowance)
// carried by one response frame. A frame without facts keeps the log unchanged.
func (s *Service) observeVendorFrame(up upstreamStream, entry *LogEntry, frame map[string]any) {
	if entry == nil || up.vendor == nil || frame == nil {
		return
	}
	if observation := up.vendor.ObserveResponse(frame); observation != nil && observation.Allowance != nil {
		entry.Allowance = observation.Allowance
	}
}

// classifyResponseError runs the vendor's own window-limit check first and falls
// back to the generic classifier, which stays vendor agnostic.
func (s *Service) classifyResponseError(up upstreamStream, body map[string]any, raw []byte, now time.Time, fallback ...int) error {
	if err, ok := classifyVendorLimitError(up.vendor, up.StatusCode, raw, now, fallback...); ok {
		return err
	}
	return classifyUpstreamResponseError(up.StatusCode, up.Headers, body, raw, now, fallback...)
}

// classifyVendorLimitError lets the vendor recognize its own window limit before
// the generic classifier runs. Only a returned block changes the outcome; a
// plain 429 stays an ordinary rate limit.
func classifyVendorLimitError(v vendors.Vendor, status int, raw []byte, now time.Time, fallback ...int) (error, bool) {
	if v == nil || status != http.StatusTooManyRequests && status != http.StatusForbidden {
		return nil, false
	}
	block := v.ClassifyLimitError(status, raw, now)
	if block == nil {
		return nil, false
	}
	target := status
	if len(fallback) > 0 {
		target = fallback[0]
	}
	limitType := quotaTypeForWindow(block.Window)
	if limitType == "" {
		limitType = "quota"
	}
	message := strings.TrimSpace(block.Reason)
	if message == "" {
		message = "upstream usage window exhausted"
	}
	kind, scope := "upstream_quota_exhausted", "account"
	if block.Permanent {
		// A permanent limit cannot be cleared by polling usage, so it must not
		// become a timed cooldown that silently expires.
		kind, scope = "upstream_plan_unsupported", "account"
	}
	return &upstreamError{
		APIError:     &APIError{Status: target, Kind: kind, Message: quotaBlockMarker + " " + message},
		HTTPStatus:   status,
		QuotaType:    limitType,
		QuotaResetAt: block.ResetAt,
		Scope:        scope,
	}, true
}
