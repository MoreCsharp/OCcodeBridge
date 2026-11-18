package opencodego

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/MoreCsharp/OCcodeBridge/internal/vendors"
)

// ErrUnauthorized reports that the upstream rejected the API key. Callers can
// tell it apart from an ordinary upstream failure with errors.Is, and the message
// never carries the key itself.
var ErrUnauthorized = errors.New("opencode-go: upstream rejected the API key")

// responseLimit bounds how much of an upstream body is read: catalogs and usage
// payloads are small, and a stuck stream must not be buffered forever.
const responseLimit = 1 << 20

// upstreamStatusError carries the upstream HTTP status through the vendor
// boundary (the bridge reads it via StatusCode) without leaking the credential.
type upstreamStatusError struct {
	op     string
	status int
	detail string
}

func (e *upstreamStatusError) Error() string {
	if e.detail == "" {
		return fmt.Sprintf("opencode-go: %s: upstream returned HTTP %d", e.op, e.status)
	}
	return fmt.Sprintf("opencode-go: %s: upstream returned HTTP %d: %s", e.op, e.status, e.detail)
}

func (e *upstreamStatusError) StatusCode() int { return e.status }

// The bridge reads the status through vendors.StatusCoder, even when the error is
// wrapped on its way out of this package.
var _ vendors.StatusCoder = (*upstreamStatusError)(nil)

func (e *upstreamStatusError) Is(target error) bool {
	if target != ErrUnauthorized {
		return false
	}
	return e.status == http.StatusUnauthorized || e.status == http.StatusForbidden
}

// newRequest builds an authenticated GET for a management call.
func newRequest(ctx context.Context, url string, c vendors.Credential) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.APIKey)
	request.Header.Set("Accept", "application/json")
	return request, nil
}

// readBody consumes and closes the response body.
func readBody(response *http.Response) ([]byte, error) {
	if response == nil || response.Body == nil {
		return nil, errors.New("opencode-go: upstream sent no response body")
	}
	defer response.Body.Close()
	return io.ReadAll(io.LimitReader(response.Body, responseLimit))
}

// bodySnippet keeps an upstream error readable in the console while scrubbing the
// API key out of it.
func bodySnippet(body []byte, secret string) string {
	snippet := strings.TrimSpace(string(body))
	snippet = strings.Map(func(r rune) rune {
		if r < ' ' {
			return ' '
		}
		return r
	}, snippet)
	if secret = strings.TrimSpace(secret); secret != "" {
		snippet = strings.ReplaceAll(snippet, secret, "***")
	}
	return limitBytes(snippet, 200)
}
