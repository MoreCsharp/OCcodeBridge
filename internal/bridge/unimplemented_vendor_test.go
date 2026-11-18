package bridge

import (
	"context"
	"net/http"
	"time"

	"github.com/xiao-qiu-qiu/PlanBridge/internal/vendors"
)

// unimplementedVendorID is a vendor that exists only to test the bridge's
// "vendor stage not implemented yet" path. The real vendors gain their methods
// stage by stage, so the fixture must stay a stub independently of them.
const unimplementedVendorID = "unimplemented-test"

type unimplementedVendor struct{}

func init() { vendors.Register(unimplementedVendor{}) }

func (unimplementedVendor) ID() string          { return unimplementedVendorID }
func (unimplementedVendor) DisplayName() string { return "Unimplemented Vendor" }
func (unimplementedVendor) BaseURL() string     { return "https://fixture.invalid/v1" }

func (unimplementedVendor) Headers(r vendors.Request) http.Header {
	return http.Header{"Authorization": []string{"Bearer " + r.Credential.APIKey}}
}

func (unimplementedVendor) ListModels(context.Context, vendors.Credential, vendors.HTTPDoer) ([]vendors.Model, error) {
	return nil, vendors.ErrNotImplemented
}

func (unimplementedVendor) FetchUsage(context.Context, vendors.Credential, vendors.HTTPDoer) (vendors.Usage, error) {
	return vendors.Usage{}, vendors.ErrNotImplemented
}

func (unimplementedVendor) ClassifyLimitError(int, []byte, time.Time) *vendors.LimitBlock {
	return nil
}

func (unimplementedVendor) ObserveResponse(map[string]any) *vendors.Observation { return nil }
