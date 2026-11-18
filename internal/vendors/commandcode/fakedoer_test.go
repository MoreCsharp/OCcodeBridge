package commandcode

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// serverURL is the management host the tests point apiBase at.
const serverURL = "https://commandcode.test"

// fakeResponse is one canned reply, keyed by request path.
type fakeResponse struct {
	status int
	body   string
}

// fakeDoer serves canned replies per request path and records every request, so
// tests can assert the URL, query and headers the vendor built.
type fakeDoer struct {
	responses map[string]fakeResponse
	requests  []*http.Request
}

func (d *fakeDoer) Do(r *http.Request) (*http.Response, error) {
	d.requests = append(d.requests, r)
	reply, ok := d.responses[r.URL.Path]
	if !ok {
		reply = fakeResponse{status: http.StatusNotFound, body: `{"error":{"message":"no fixture"}}`}
	}
	if reply.status == 0 {
		reply.status = http.StatusOK
	}
	return &http.Response{
		StatusCode: reply.status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(reply.body)),
		Request:    r,
	}, nil
}

// requestTo returns the recorded request whose path matches.
func (d *fakeDoer) requestTo(t *testing.T, path string) *http.Request {
	t.Helper()
	for _, r := range d.requests {
		if r.URL.Path == path {
			return r
		}
	}
	t.Fatalf("no request to %s, got %v", path, d.requestPaths())
	return nil
}

func (d *fakeDoer) requestPaths() []string {
	paths := make([]string, 0, len(d.requests))
	for _, r := range d.requests {
		paths = append(paths, r.URL.Path)
	}
	return paths
}

// readFixture loads a testdata file.
func readFixture(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(body)
}

// pointAPIAtFakeHost redirects the management calls and restores them afterwards.
func pointAPIAtFakeHost(t *testing.T) {
	t.Helper()
	previous := apiBase
	apiBase = serverURL
	t.Cleanup(func() { apiBase = previous })
}

// usageFixtures serves the three usage calls from testdata.
func usageFixtures(t *testing.T) map[string]fakeResponse {
	t.Helper()
	return map[string]fakeResponse{
		whoamiPath:        {body: readFixture(t, "whoami.json")},
		creditsPath:       {body: readFixture(t, "credits.json")},
		subscriptionsPath: {body: readFixture(t, "subscriptions.json")},
	}
}

// statusCodeOf reads the status a vendor error reports to the bridge.
func statusCodeOf(err error) int {
	type statusCoder interface{ StatusCode() int }
	if coder, ok := err.(statusCoder); ok {
		return coder.StatusCode()
	}
	return 0
}
