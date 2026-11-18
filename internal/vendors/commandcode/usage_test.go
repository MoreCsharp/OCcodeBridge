package commandcode

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/xiao-qiu-qiu/OCcodeBridge/internal/vendors"
)

const usageKey = "sk-commandcode-fixture"

func TestFetchUsageAggregatesWhoamiCreditsAndSubscription(t *testing.T) {
	pointAPIAtFakeHost(t)
	doer := &fakeDoer{responses: usageFixtures(t)}

	usage, err := Vendor{}.FetchUsage(t.Context(), vendors.Credential{APIKey: usageKey}, doer)
	if err != nil {
		t.Fatalf("FetchUsage: %v", err)
	}
	if usage.PlanName != "GOAT" {
		t.Fatalf("plan = %q, want GOAT", usage.PlanName)
	}
	if usage.AccountID != "org_fixture" {
		t.Fatalf("account = %q, want the organization id", usage.AccountID)
	}
	if usage.Balance == nil || *usage.Balance != 15 || usage.BalanceCurrency != "USD" {
		t.Fatalf("balance = %v %q, want 15 USD", usage.Balance, usage.BalanceCurrency)
	}
	if usage.PeriodStart == nil || usage.PeriodStart.Format(time.RFC3339) != "2026-11-01T00:00:00Z" {
		t.Fatalf("period start = %v", usage.PeriodStart)
	}
	if usage.PeriodEnd == nil || usage.PeriodEnd.Format(time.RFC3339) != "2026-12-01T00:00:00Z" {
		t.Fatalf("period end = %v", usage.PeriodEnd)
	}

	if len(usage.Windows) != 3 {
		t.Fatalf("windows = %+v, want 5h, 7d and month", usage.Windows)
	}
	assertWindow(t, usage.Windows[0], vendors.WindowFiveHour, 5, 20, 25)
	if reset := usage.Windows[0].ResetAt; reset == nil || reset.UnixMilli() != 1794992776000 {
		t.Fatalf("5h reset = %v, want the millisecond stamp", reset)
	}
	assertWindow(t, usage.Windows[1], vendors.WindowWeekly, 10, 50, 20)
	if reset := usage.Windows[1].ResetAt; reset == nil || reset.Unix() != 1794993000 {
		t.Fatalf("weekly reset = %v, want the unix-second stamp", reset)
	}
	// An active GOAT plan puts its 70 monthly credits into the pool, on top of the
	// 15 extra credits: 85 total, of which 30 is spent.
	assertWindow(t, usage.Windows[2], vendors.WindowMonthly, 30, 85, 30.0/85*100)
	if reset := usage.Windows[2].ResetAt; reset == nil || reset.Format(time.RFC3339) != "2026-12-01T00:00:00Z" {
		t.Fatalf("monthly reset = %v", reset)
	}

	if len(doer.requests) != 3 {
		t.Fatalf("requests = %v, want one per usage endpoint", doer.requestPaths())
	}
	whoami := doer.requestTo(t, whoamiPath)
	if whoami.URL.String() != serverURL+"/alpha/whoami?limits=1" {
		t.Fatalf("whoami URL = %s", whoami.URL)
	}
	if whoami.Header.Get("Authorization") != "Bearer "+usageKey {
		t.Fatalf("authorization = %q", whoami.Header.Get("Authorization"))
	}
	for _, path := range []string{creditsPath, subscriptionsPath} {
		request := doer.requestTo(t, path)
		if request.URL.Query().Get("orgId") != "org_fixture" {
			t.Fatalf("%s orgId = %q", path, request.URL.Query().Get("orgId"))
		}
	}
}

func TestFetchUsageOmitsOrgIDForPersonalAccounts(t *testing.T) {
	pointAPIAtFakeHost(t)
	responses := usageFixtures(t)
	responses[whoamiPath] = fakeResponse{body: `{"success":true,"user":{"id":"usr_personal","userName":"solo"},"org":null}`}
	doer := &fakeDoer{responses: responses}

	usage, err := Vendor{}.FetchUsage(t.Context(), vendors.Credential{APIKey: usageKey}, doer)
	if err != nil {
		t.Fatalf("FetchUsage: %v", err)
	}
	if usage.AccountID != "usr_personal" {
		t.Fatalf("account = %q, want the user id", usage.AccountID)
	}
	if usage.PlanName != "GOAT" {
		t.Fatalf("plan = %q", usage.PlanName)
	}
	for _, path := range []string{creditsPath, subscriptionsPath} {
		query := doer.requestTo(t, path).URL.Query()
		if _, present := query["orgId"]; present {
			t.Fatalf("%s sent orgId=%q for an account without an organization", path, query.Get("orgId"))
		}
	}
}

func TestFetchUsageSkipsWindowsWhenNotLimited(t *testing.T) {
	pointAPIAtFakeHost(t)
	responses := usageFixtures(t)
	responses[creditsPath] = fakeResponse{body: `{
		"success": true,
		"credits": {"monthlyCredits": 15, "purchasedCredits": 0, "freeCredits": 0, "planId": "individual_provider"},
		"windowLimits": {"limited": false, "fiveHour": {"used": 1, "cap": 5, "resetAt": 1794992776}}
	}`}
	responses[subscriptionsPath] = fakeResponse{body: `{
		"success": true,
		"data": {"planId": "individual_provider", "status": "active", "currentPeriodEnd": "2026-12-01T00:00:00Z"}
	}`}
	doer := &fakeDoer{responses: responses}

	usage, err := Vendor{}.FetchUsage(t.Context(), vendors.Credential{APIKey: usageKey}, doer)
	if err != nil {
		t.Fatalf("FetchUsage: %v", err)
	}
	if usage.PlanName != "Provider" {
		t.Fatalf("plan = %q, want Provider", usage.PlanName)
	}
	if len(usage.Windows) != 1 || usage.Windows[0].Kind != vendors.WindowMonthly {
		t.Fatalf("windows = %+v, want the monthly window only", usage.Windows)
	}
	if usage.Balance == nil || *usage.Balance != 0 {
		t.Fatalf("balance = %v, want 0", usage.Balance)
	}
}

func TestFetchUsageKeepsUnknownCapAndReadsISOReset(t *testing.T) {
	pointAPIAtFakeHost(t)
	responses := usageFixtures(t)
	responses[creditsPath] = fakeResponse{body: `{
		"success": true,
		"credits": {"monthlyCredits": 40},
		"windowLimits": {"limited": true,
			"fiveHour": {"used": 3, "cap": 0},
			"weekly": {"cap": 50},
			"daily": {"used": 1, "cap": 4, "resetAt": "2026-11-20T15:00:00Z"}}
	}`}
	doer := &fakeDoer{responses: responses}

	usage, err := Vendor{}.FetchUsage(t.Context(), vendors.Credential{APIKey: usageKey}, doer)
	if err != nil {
		t.Fatalf("FetchUsage: %v", err)
	}
	if len(usage.Windows) != 4 {
		t.Fatalf("windows = %+v, want 5h, 7d, daily and month", usage.Windows)
	}
	fiveHour := usage.Windows[0]
	if fiveHour.UsedPercent != nil {
		t.Fatalf("5h percent = %v, want unknown for a zero cap", *fiveHour.UsedPercent)
	}
	if fiveHour.Cap == nil || *fiveHour.Cap != 0 || fiveHour.Used == nil || *fiveHour.Used != 3 {
		t.Fatalf("5h window = %+v", fiveHour)
	}
	if usage.Windows[1].UsedPercent != nil || usage.Windows[1].Used != nil {
		t.Fatalf("weekly window = %+v, want an unknown used and percent", usage.Windows[1])
	}
	assertWindow(t, usage.Windows[2], vendors.WindowDaily, 1, 4, 25)
	if reset := usage.Windows[2].ResetAt; reset == nil || reset.Format(time.RFC3339) != "2026-11-20T15:00:00Z" {
		t.Fatalf("daily reset = %v", reset)
	}
}

func TestFetchUsageDegradesWhenSubscriptionsFail(t *testing.T) {
	pointAPIAtFakeHost(t)
	responses := usageFixtures(t)
	responses[subscriptionsPath] = fakeResponse{status: 500, body: `{"error":{"type":"server_error","message":"boom"}}`}
	doer := &fakeDoer{responses: responses}

	usage, err := Vendor{}.FetchUsage(t.Context(), vendors.Credential{APIKey: usageKey}, doer)
	if err != nil {
		t.Fatalf("FetchUsage: %v", err)
	}
	if usage.PeriodStart != nil || usage.PeriodEnd != nil {
		t.Fatalf("period = %v - %v, want unknown", usage.PeriodStart, usage.PeriodEnd)
	}
	if usage.PlanName != "GOAT" {
		t.Fatalf("plan = %q, want the plan named by the credits response", usage.PlanName)
	}
	if len(usage.Windows) != 3 {
		t.Fatalf("windows = %+v, want the two rolling windows and a monthly one", usage.Windows)
	}
	// Without a subscription status there is no active monthly pool, so the month
	// window only reports the credits that are left.
	assertWindow(t, usage.Windows[2], vendors.WindowMonthly, 0, 55, 0)
	if usage.Windows[2].ResetAt != nil {
		t.Fatalf("monthly reset = %v, want unknown", usage.Windows[2].ResetAt)
	}
}

func TestFetchUsageDegradesWhenWhoamiFails(t *testing.T) {
	pointAPIAtFakeHost(t)
	responses := usageFixtures(t)
	responses[whoamiPath] = fakeResponse{status: 500, body: `{"error":{"type":"server_error","message":"boom"}}`}
	doer := &fakeDoer{responses: responses}

	usage, err := Vendor{}.FetchUsage(t.Context(), vendors.Credential{APIKey: usageKey}, doer)
	if err != nil {
		t.Fatalf("FetchUsage: %v", err)
	}
	if !strings.HasPrefix(usage.AccountID, "key-sha256:") {
		t.Fatalf("account = %q, want a key hash prefix", usage.AccountID)
	}
	if strings.Contains(usage.AccountID, usageKey) {
		t.Fatalf("account %q repeats the API key", usage.AccountID)
	}
	if len(usage.Windows) != 3 {
		t.Fatalf("windows = %+v", usage.Windows)
	}
	for _, path := range []string{creditsPath, subscriptionsPath} {
		if _, present := doer.requestTo(t, path).URL.Query()["orgId"]; present {
			t.Fatalf("%s sent an orgId without a resolved organization", path)
		}
	}
}

func TestFetchUsageFailsWhenCreditsFail(t *testing.T) {
	pointAPIAtFakeHost(t)
	responses := usageFixtures(t)
	responses[creditsPath] = fakeResponse{status: 500, body: `{"error":{"type":"server_error","message":"boom"}}`}
	doer := &fakeDoer{responses: responses}

	_, err := Vendor{}.FetchUsage(t.Context(), vendors.Credential{APIKey: usageKey}, doer)
	if err == nil {
		t.Fatal("a failing credits call must not be reported as a successful sample")
	}
	if statusCodeOf(err) != 500 {
		t.Fatalf("status = %d, want 500", statusCodeOf(err))
	}
	if strings.Contains(err.Error(), usageKey) {
		t.Fatalf("error %q repeats the API key", err.Error())
	}
}

func TestFetchUsageReportsRejectedKey(t *testing.T) {
	cases := map[string]string{
		"whoami":        whoamiPath,
		"credits":       creditsPath,
		"subscriptions": subscriptionsPath,
	}
	for name, rejectedPath := range cases {
		t.Run(name, func(t *testing.T) {
			pointAPIAtFakeHost(t)
			responses := usageFixtures(t)
			responses[rejectedPath] = fakeResponse{status: 401, body: `{"error":{"type":"authentication_error","message":"Missing or invalid auth"}}`}
			doer := &fakeDoer{responses: responses}

			_, err := Vendor{}.FetchUsage(t.Context(), vendors.Credential{APIKey: usageKey}, doer)
			if err == nil {
				t.Fatal("a 401 must fail the sample")
			}
			if statusCodeOf(err) != 401 {
				t.Fatalf("status = %d, want 401", statusCodeOf(err))
			}
			if strings.Contains(err.Error(), usageKey) {
				t.Fatalf("error %q repeats the API key", err.Error())
			}
		})
	}
}

// A plan without API access rejects the management calls too. The error keeps the
// upstream body so the bridge can reuse ClassifyLimitError instead of guessing
// the permanent state from the status alone.
type responseBodyCarrier interface{ ResponseBody() []byte }

func TestFetchUsageKeepsBodyForPlanClassification(t *testing.T) {
	pointAPIAtFakeHost(t)
	responses := usageFixtures(t)
	body := `{"success":false,"error":{"code":"upgrade_required","status":403,"message":"Upgrade required"}}`
	responses[creditsPath] = fakeResponse{status: 403, body: body}
	doer := &fakeDoer{responses: responses}

	_, err := Vendor{}.FetchUsage(t.Context(), vendors.Credential{APIKey: usageKey}, doer)
	if err == nil || statusCodeOf(err) != 403 {
		t.Fatalf("credits 403 = %v", err)
	}
	carrier, ok := err.(responseBodyCarrier)
	if !ok || string(carrier.ResponseBody()) != body {
		t.Fatalf("error did not keep the upstream body: %T", err)
	}
	block := (Vendor{}).ClassifyLimitError(statusCodeOf(err), carrier.ResponseBody(), time.Now())
	if block == nil || !block.Permanent {
		t.Fatalf("a plan without API access was not classified as permanent: %+v", block)
	}
	if strings.Contains(err.Error(), usageKey) {
		t.Fatalf("error %q repeats the API key", err.Error())
	}
}

func TestFetchUsageNeedsATransport(t *testing.T) {
	pointAPIAtFakeHost(t)
	if _, err := (Vendor{}).FetchUsage(t.Context(), vendors.Credential{APIKey: usageKey}, nil); err == nil {
		t.Fatal("FetchUsage without a doer must fail")
	}
}

// assertWindow checks the numbers the console renders for one window.
func assertWindow(t *testing.T, window vendors.Window, kind string, used, capacity, percent float64) {
	t.Helper()
	if window.Kind != kind {
		t.Fatalf("window kind = %q, want %q", window.Kind, kind)
	}
	if window.Used == nil || math.Abs(*window.Used-used) > 1e-9 {
		t.Fatalf("%s used = %v, want %v", kind, window.Used, used)
	}
	if window.Cap == nil || math.Abs(*window.Cap-capacity) > 1e-9 {
		t.Fatalf("%s cap = %v, want %v", kind, window.Cap, capacity)
	}
	if window.UsedPercent == nil || math.Abs(*window.UsedPercent-percent) > 1e-6 {
		t.Fatalf("%s percent = %v, want %v", kind, window.UsedPercent, percent)
	}
}
