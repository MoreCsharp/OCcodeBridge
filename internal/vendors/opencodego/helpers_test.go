package opencodego

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiao-qiu-qiu/PlanBridge/internal/vendors"
)

// fixture reads an upstream response body from testdata.
func fixture(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(body)
}

// recordingDoer sends the request for real, but every test points it at an
// httptest server: no test reaches opencode.ai. It keeps the request so the test
// can assert the method, URL and vendor headers.
type recordingDoer struct {
	client  http.Client
	request *http.Request
}

func newRecordingDoer() *recordingDoer {
	return &recordingDoer{client: http.Client{Timeout: 5 * time.Second}}
}

func (d *recordingDoer) Do(request *http.Request) (*http.Response, error) {
	d.request = request
	return d.client.Do(request)
}

// serveFixture starts a server that answers every request with one status and
// body, and returns it with the doer that records the outgoing request.
func serveFixture(t *testing.T, status int, body string) (*httptest.Server, *recordingDoer) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server, newRecordingDoer()
}

// failingDoer fails the request without any network access.
type failingDoer struct{ err error }

func (d failingDoer) Do(*http.Request) (*http.Response, error) { return nil, d.err }

// withUsageEndpoint points the usage call at a local server for one test.
func withUsageEndpoint(t *testing.T, endpoint string) {
	t.Helper()
	original := usageEndpoint
	usageEndpoint = endpoint
	t.Cleanup(func() { usageEndpoint = original })
}

// withModelsEndpoint points the catalog call at a local server for one test.
func withModelsEndpoint(t *testing.T, endpoint string) {
	t.Helper()
	original := modelsEndpoint
	modelsEndpoint = endpoint
	t.Cleanup(func() { modelsEndpoint = original })
}

// windowsByKind indexes a usage sample for assertions.
func windowsByKind(t *testing.T, windows []vendors.Window) map[string]vendors.Window {
	t.Helper()
	byKind := make(map[string]vendors.Window, len(windows))
	for _, window := range windows {
		if _, duplicate := byKind[window.Kind]; duplicate {
			t.Fatalf("duplicate %s window", window.Kind)
		}
		byKind[window.Kind] = window
	}
	return byKind
}

// assertFloat compares an optional number, treating nil as unknown.
func assertFloat(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s is nil, want %v", name, want)
	}
	if diff := *got - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("%s = %v, want %v", name, *got, want)
	}
}

// assertNilFloat checks that an unreported field stayed unknown instead of 0.
func assertNilFloat(t *testing.T, name string, got *float64) {
	t.Helper()
	if got != nil {
		t.Fatalf("%s = %v, want nil (unknown, not zero)", name, *got)
	}
}

// assertTime compares an optional timestamp.
func assertTime(t *testing.T, name string, got *time.Time, want time.Time) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s is nil, want %s", name, want.Format(time.RFC3339))
	}
	if !got.Equal(want) {
		t.Fatalf("%s = %s, want %s", name, got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// assertBearer checks the vendor-owned request headers of a recorded request.
func assertBearer(t *testing.T, request *http.Request, wantURL, wantKey string) {
	t.Helper()
	if request == nil {
		t.Fatal("the vendor never called the injected doer")
	}
	if request.Method != http.MethodGet {
		t.Fatalf("method = %s, want GET", request.Method)
	}
	if got := request.URL.String(); got != wantURL {
		t.Fatalf("url = %s, want %s", got, wantURL)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer "+wantKey {
		t.Fatalf("authorization = %q", got)
	}
}
