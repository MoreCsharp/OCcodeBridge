package commandcode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/xiao-qiu-qiu/PlanBridge/internal/vendors"
)

// userAgent is sent on every vendor call. api.commandcode.ai answers 403 to a
// request without a User-Agent (observed on the public /models endpoint), so the
// vendor sets its own instead of leaving the header to the egress layer.
const userAgent = "PlanBridge"

// statusError carries the upstream HTTP status. The bridge reads it through its
// own StatusCode interface, so a rejected key shows as unauthorized instead of a
// generic failure; the message never repeats the request headers.
type statusError struct {
	status  int
	message string
}

func (e *statusError) Error() string   { return e.message }
func (e *statusError) StatusCode() int { return e.status }

// getJSON performs one vendor GET and decodes a JSON body into out. A transport
// error is returned unwrapped so the bridge's own status mapping still sees it.
func getJSON(ctx context.Context, c vendors.Credential, doer vendors.HTTPDoer, endpoint string, out any) error {
	if doer == nil {
		return fmt.Errorf("commandcode: no HTTP transport")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("commandcode: build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	if c.ZDR {
		req.Header.Set(ZDRHeader, "1")
	}
	resp, err := doer.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("commandcode: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &statusError{
			status:  resp.StatusCode,
			message: fmt.Sprintf("commandcode: %s returned HTTP %d", req.URL.Path, resp.StatusCode),
		}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("commandcode: %s returned an unreadable body", req.URL.Path)
	}
	return nil
}

// unauthorized reports whether a vendor call was rejected as an authentication
// failure, which the caller must surface instead of degrading.
func unauthorized(err error) bool {
	rejected, ok := err.(*statusError)
	return ok && rejected.status == http.StatusUnauthorized
}
