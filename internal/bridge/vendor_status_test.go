package bridge

import (
	"fmt"
	"strings"
	"testing"

	"github.com/MoreCsharp/OCcodeBridge/internal/vendors"
)

// vendorStatusFixture stands in for a vendor error type: it carries the upstream
// status through the exported contract and is returned wrapped, the way a vendor
// package adds its own context around a rejected management call.
type vendorStatusFixture struct{ status int }

func (e vendorStatusFixture) Error() string   { return "fixture vendor call rejected" }
func (e vendorStatusFixture) StatusCode() int { return e.status }

var _ vendors.StatusCoder = vendorStatusFixture{}

func TestStatusOfReadsWrappedVendorStatus(t *testing.T) {
	for _, status := range []int{401, 403, 429, 504} {
		err := fmt.Errorf("fixture: usage request failed: %w", fmt.Errorf("usage: %w", vendorStatusFixture{status}))
		if got := statusOf(err); got != status {
			t.Fatalf("statusOf(wrapped %d) = %d", status, got)
		}
	}
	if statusOf(fmt.Errorf("no status here")) != 502 {
		t.Fatal("an error without a status must stay a generic upstream failure")
	}
	if statusOf(nil) != 200 {
		t.Fatal("no error is a success status")
	}
}

func TestUsageFailureLabelsPermissionFailureWithoutLoginPrompt(t *testing.T) {
	for _, status := range []int{401, 403} {
		state, message := usageFailure(fmt.Errorf("vendor: usage: %w", vendorStatusFixture{status}))
		if state != "unauthorized" || !strings.Contains(message, "权限") || !strings.Contains(message, "API Key") {
			t.Fatalf("HTTP %d became %q / %q", status, state, message)
		}
	}
	state, _ := usageFailure(fmt.Errorf("vendor: usage: %w", vendorStatusFixture{429}))
	if state != "rate_limited" {
		t.Fatalf("wrapped 429 state = %q", state)
	}
}
