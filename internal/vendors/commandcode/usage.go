package commandcode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/xiao-qiu-qiu/OCcodeBridge/internal/vendors"
)

// APIBase is the host root of the CLI's management endpoints. They are not part
// of the documented Provider API: the paths and bodies below mirror what the CLI
// /usage command calls (fetchUsageData in cli.mjs).
const APIBase = "https://api.commandcode.ai"

// apiBase is where the management calls go. Tests point it at a fake host.
var apiBase = APIBase

const (
	whoamiPath        = "/alpha/whoami"
	creditsPath       = "/alpha/billing/credits"
	subscriptionsPath = "/alpha/billing/subscriptions"
)

// whoamiResponse is GET /alpha/whoami?limits=1. An account without an
// organization reports org as null.
type whoamiResponse struct {
	User *struct {
		ID       string `json:"id"`
		UserName string `json:"userName"`
	} `json:"user"`
	Org *struct {
		ID    string `json:"id"`
		Login string `json:"login"`
	} `json:"org"`
}

// creditsResponse is GET /alpha/billing/credits: credits is a nested object while
// windowLimits sits at the top level of the same body.
type creditsResponse struct {
	Credits      *creditsBlock `json:"credits"`
	WindowLimits *windowLimits `json:"windowLimits"`
}

// creditsBlock is the credit balance of the current period, in credits (one
// credit is roughly one dollar of model usage).
type creditsBlock struct {
	MonthlyCredits   *float64 `json:"monthlyCredits"`
	PurchasedCredits *float64 `json:"purchasedCredits"`
	FreeCredits      *float64 `json:"freeCredits"`
	PlanID           string   `json:"planId"`
}

// windowLimits lists the rolling windows. The CLI renders them only when limited
// is true: a pay-as-you-go account has no rolling window at all.
type windowLimits struct {
	Limited  *bool        `json:"limited"`
	FiveHour *windowLimit `json:"fiveHour"`
	Weekly   *windowLimit `json:"weekly"`
	Daily    *windowLimit `json:"daily"`
}

// windowLimit is one rolling window, measured in credits.
type windowLimit struct {
	Used    *float64        `json:"used"`
	Cap     *float64        `json:"cap"`
	ResetAt json.RawMessage `json:"resetAt"`
}

// subscriptionResponse is GET /alpha/billing/subscriptions.
type subscriptionResponse struct {
	Data *subscription `json:"data"`
}

type subscription struct {
	PlanID             string          `json:"planId"`
	Status             string          `json:"status"`
	CurrentPeriodStart json.RawMessage `json:"currentPeriodStart"`
	CurrentPeriodEnd   json.RawMessage `json:"currentPeriodEnd"`
}

// FetchUsage aggregates the CLI's three usage calls. whoami scopes the billing
// calls to the credential's organization. A key rejected with 401 is an
// authentication failure the caller must see, and a failing credits call leaves
// nothing to report; a failing whoami or subscriptions call only leaves the
// fields it could have filled nil.
func (Vendor) FetchUsage(ctx context.Context, c vendors.Credential, doer vendors.HTTPDoer) (vendors.Usage, error) {
	var whoami whoamiResponse
	whoamiErr := getJSON(ctx, c, doer, apiBase+whoamiPath+"?limits=1", &whoami)
	if unauthorized(whoamiErr) {
		return vendors.Usage{}, whoamiErr
	}
	orgID := ""
	if whoamiErr == nil && whoami.Org != nil {
		orgID = strings.TrimSpace(whoami.Org.ID)
	}

	var credits creditsResponse
	creditsErr := getJSON(ctx, c, doer, scopedPath(creditsPath, orgID), &credits)
	if unauthorized(creditsErr) {
		return vendors.Usage{}, creditsErr
	}
	if creditsErr != nil {
		return vendors.Usage{}, creditsErr
	}

	var subscriptions subscriptionResponse
	subscriptionErr := getJSON(ctx, c, doer, scopedPath(subscriptionsPath, orgID), &subscriptions)
	if unauthorized(subscriptionErr) {
		return vendors.Usage{}, subscriptionErr
	}
	var current *subscription
	if subscriptionErr == nil {
		current = subscriptions.Data
	}

	usage := vendors.Usage{FetchedAt: time.Now().UTC(), AccountID: accountID(c, whoami)}
	planID := ""
	if current != nil {
		usage.PeriodStart = parseFlexibleTime(current.CurrentPeriodStart)
		usage.PeriodEnd = parseFlexibleTime(current.CurrentPeriodEnd)
		planID = current.PlanID
	}
	if planID == "" && credits.Credits != nil {
		planID = credits.Credits.PlanID
	}
	if resolved, ok := matchPlan(planID); ok {
		usage.PlanName = resolved.Name
	}
	if credits.Credits != nil {
		// Extra credits are the pay-as-you-go balance, billed in dollars.
		balance := maxCredit(credits.Credits.PurchasedCredits) + maxCredit(credits.Credits.FreeCredits)
		usage.Balance, usage.BalanceCurrency = &balance, "USD"
	}
	usage.Windows = append(usage.Windows, limitWindows(credits.WindowLimits)...)
	if monthly, ok := monthlyWindow(credits.Credits, current); ok {
		usage.Windows = append(usage.Windows, monthly)
	}
	return usage, nil
}

// scopedPath adds the org scope the CLI sends and omits the parameter entirely
// for an account without an organization (buildUsageEndpoint drops null params).
func scopedPath(path, orgID string) string {
	if orgID == "" {
		return apiBase + path
	}
	return apiBase + path + "?orgId=" + url.QueryEscape(orgID)
}

// accountID identifies the subscription so several keys of one account share one
// usage sample. A credential with neither an organization nor a user id falls back
// to a hash prefix, so the plain key never leaves the plugin.
func accountID(c vendors.Credential, whoami whoamiResponse) string {
	if whoami.Org != nil {
		if id := strings.TrimSpace(whoami.Org.ID); id != "" {
			return id
		}
	}
	if whoami.User != nil {
		if id := strings.TrimSpace(whoami.User.ID); id != "" {
			return id
		}
	}
	if c.APIKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(c.APIKey))
	return "key-sha256:" + hex.EncodeToString(sum[:8])
}

// maxCredit treats a missing or negative credit field as zero, as the CLI does.
func maxCredit(value *float64) float64 {
	if value == nil || *value <= 0 {
		return 0
	}
	return *value
}

// limitWindows maps the rolling windows of the credits response. Only a response
// that reports limited generates windows: without it the account is either
// pay-as-you-go or the vendor did not report limits, and an empty bar would be a
// claim the data does not support.
func limitWindows(limits *windowLimits) []vendors.Window {
	if limits == nil || limits.Limited == nil || !*limits.Limited {
		return nil
	}
	reported := []struct {
		kind  string
		limit *windowLimit
	}{
		{vendors.WindowFiveHour, limits.FiveHour},
		{vendors.WindowWeekly, limits.Weekly},
		{vendors.WindowDaily, limits.Daily},
	}
	windows := make([]vendors.Window, 0, len(reported))
	for _, entry := range reported {
		if entry.limit == nil {
			continue
		}
		if window, ok := entry.limit.window(entry.kind); ok {
			windows = append(windows, window)
		}
	}
	return windows
}

// window converts one credit window. A used/cap pair gives the exact percentage;
// a non-positive cap leaves the percentage unknown rather than dividing by zero.
func (l *windowLimit) window(kind string) (vendors.Window, bool) {
	if l.Used == nil && l.Cap == nil {
		return vendors.Window{}, false
	}
	window := vendors.Window{Kind: kind, Used: l.Used, Cap: l.Cap, ResetAt: parseFlexibleTime(l.ResetAt)}
	if l.Used != nil && l.Cap != nil && *l.Cap > 0 {
		percent := clampPercent(*l.Used / *l.Cap * 100)
		window.UsedPercent = &percent
	}
	return window, true
}

// monthlyWindow projects the monthly credit pool the way the CLI's
// projectUsageView does: an active plan adds its own monthly credits to the
// purchased and free credits, so the bar shows how much of the pool is spent. The
// spend reported by /alpha/usage/summary is not queried yet, so without an active
// plan the pool is just what is left.
func monthlyWindow(credits *creditsBlock, current *subscription) (vendors.Window, bool) {
	if credits == nil {
		return vendors.Window{}, false
	}
	monthly := maxCredit(credits.MonthlyCredits)
	purchased := maxCredit(credits.PurchasedCredits)
	free := maxCredit(credits.FreeCredits)
	remaining := monthly + purchased + free
	pool := remaining
	if current != nil && strings.EqualFold(strings.TrimSpace(current.Status), "active") {
		if active, ok := matchPlan(current.PlanID); ok {
			pool = math.Max(active.MonthlyCredits, monthly) + purchased + free
		}
	}
	if pool <= 0 {
		return vendors.Window{}, false
	}
	used := pool - remaining
	percent := clampPercent(used / pool * 100)
	window := vendors.Window{Kind: vendors.WindowMonthly, Used: &used, Cap: &pool, UsedPercent: &percent}
	if current != nil {
		window.ResetAt = parseFlexibleTime(current.CurrentPeriodEnd)
	}
	return window, true
}

// clampPercent keeps a computed percentage in the range the console renders, as
// the CLI's getUsagePercent does.
func clampPercent(percent float64) float64 {
	if math.IsNaN(percent) || percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}
