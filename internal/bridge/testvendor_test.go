package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/MoreCsharp/OCcodeBridge/internal/vendors"
	"github.com/MoreCsharp/OCcodeBridge/internal/vendors/opencodego"
)

// The bridge tests exercise the request/usage path against this fixture vendor
// instead of a real subscription. Its JSON contract is deliberately minimal, so a
// test can describe a usage sample or a window limit in a few lines, and P1/P2
// keep their own vendor tests inside their own packages.
const testVendorID = "fixture"

// requestVendorID is the vendor the request-path fixtures use: the real OpenCode
// Go package, whose headers are implemented and whose base URL is a real endpoint.
const requestVendorID = opencodego.ID

type fixtureVendor struct{}

func init() { vendors.Register(fixtureVendor{}) }

func (fixtureVendor) ID() string          { return testVendorID }
func (fixtureVendor) DisplayName() string { return "Fixture Vendor" }
func (fixtureVendor) BaseURL() string     { return "https://fixture.invalid/v1" }

func (fixtureVendor) Headers(r vendors.Request) http.Header {
	return http.Header{"Authorization": []string{"Bearer " + r.Credential.APIKey}}
}

func (fixtureVendor) ListModels(context.Context, vendors.Credential, vendors.HTTPDoer) ([]vendors.Model, error) {
	return []vendors.Model{{ID: "fixture-model", UpstreamID: testVendorID + "/fixture-model", Name: "Fixture Model", ContextLength: 8192, Endpoints: []string{"/chat/completions"}}}, nil
}

// FetchUsage GETs "<base>/usage" and expects
// {"accountId","planName","windows":[{"kind","percent","used","cap","resetAt"}],"balance"}.
func (fixtureVendor) FetchUsage(ctx context.Context, c vendors.Credential, doer vendors.HTTPDoer) (vendors.Usage, error) {
	request, err := http.NewRequestWithContext(ctx, "GET", fixtureBase+"/usage", nil)
	if err != nil {
		return vendors.Usage{}, err
	}
	request.Header = fixtureVendor{}.Headers(vendors.Request{Credential: c})
	response, err := doer.Do(request)
	if err != nil {
		return vendors.Usage{}, err
	}
	defer response.Body.Close()
	var body struct {
		AccountID string   `json:"accountId"`
		PlanName  string   `json:"planName"`
		Balance   *float64 `json:"balance"`
		Windows   []struct {
			Kind    string   `json:"kind"`
			Percent *float64 `json:"percent"`
			Used    *float64 `json:"used"`
			Cap     *float64 `json:"cap"`
			Status  string   `json:"status"`
			ResetAt string   `json:"resetAt"`
		} `json:"windows"`
	}
	decoder := json.NewDecoder(response.Body)
	if err := decoder.Decode(&body); err != nil {
		return vendors.Usage{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return vendors.Usage{}, fail(response.StatusCode, "fixture usage rejected")
	}
	usage := vendors.Usage{PlanName: body.PlanName, AccountID: body.AccountID, Balance: body.Balance, FetchedAt: time.Now().UTC()}
	for _, window := range body.Windows {
		parsed := vendors.Window{Kind: window.Kind, UsedPercent: window.Percent, Used: window.Used, Cap: window.Cap, Status: window.Status}
		if window.ResetAt != "" {
			if at, err := time.Parse(time.RFC3339Nano, window.ResetAt); err == nil {
				parsed.ResetAt = &at
			}
		}
		usage.Windows = append(usage.Windows, parsed)
	}
	return usage, nil
}

// ClassifyLimitError recognizes the fixture's window-limit envelope:
// {"error":{"message":"... five hour ..."},"window":"5h","resetAt":"..."}.
func (fixtureVendor) ClassifyLimitError(status int, body []byte, now time.Time) *vendors.LimitBlock {
	if status != http.StatusTooManyRequests {
		return nil
	}
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Window  string `json:"window"`
		ResetAt string `json:"resetAt"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return nil
	}
	message := strings.ToLower(envelope.Error.Message)
	if !strings.Contains(message, "limit") && !strings.Contains(message, "exhausted") {
		return nil
	}
	block := &vendors.LimitBlock{Window: envelope.Window, Reason: envelope.Error.Message}
	if block.Window == "" {
		if strings.Contains(message, "week") {
			block.Window = vendors.WindowWeekly
		} else {
			block.Window = vendors.WindowFiveHour
		}
	}
	if at, err := time.Parse(time.RFC3339Nano, envelope.ResetAt); err == nil {
		block.ResetAt = &at
	} else {
		value := now.Add(5 * time.Hour)
		block.ResetAt = &value
	}
	return block
}

// ObserveResponse reads the fixture's allowance object:
// {"allowance":{"charged":0.25}}.
func (fixtureVendor) ObserveResponse(frame map[string]any) *vendors.Observation {
	allowance, ok := frame["allowance"].(map[string]any)
	if !ok {
		return nil
	}
	return &vendors.Observation{Allowance: allowance, Raw: jsonBytes(allowance)}
}

// fixtureBase is the inference base the fixture vendor's management calls use. A
// test may repoint it to serve the catalog or usage from a local server.
var fixtureBase = "https://fixture.invalid/v1"
