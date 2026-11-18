package commandcode

import (
	"sort"
	"strings"

	"github.com/MoreCsharp/OCcodeBridge/internal/vendors"
)

// plan is one row of the CLI's built-in plan table (Rr/Or in cli.mjs): the API's
// planId, the name the console shows and the monthly credits the plan includes.
type plan struct {
	ID              string
	Name            string
	MonthlyCredits  float64
	PerModelCredits bool
}

// planTable mirrors the CLI plan table. planIds are normalized before matching,
// so the hyphenated ids below are what the API's snake_case values become.
//
// PerModelCredits marks the plans whose credits are usage-value units (Go, GOAT
// and Pro, per commandcode.ai/docs/resources/usage-limits): a request draws the
// slice it takes of that model's monthly allowance, so one credit is worth a
// different dollar amount on every model. On the other plans 1 credit = $1.
var planTable = []plan{
	{ID: "individual-go", Name: "Go", MonthlyCredits: 10, PerModelCredits: true},
	{ID: "individual-go-v1", Name: "Go", MonthlyCredits: 10, PerModelCredits: true},
	{ID: "individual-goat", Name: "GOAT", MonthlyCredits: 70, PerModelCredits: true},
	{ID: "individual-pro", Name: "Pro", MonthlyCredits: 30, PerModelCredits: true},
	{ID: "individual-pro-v1", Name: "Pro", MonthlyCredits: 80, PerModelCredits: true},
	{ID: "individual-provider", Name: "Provider", MonthlyCredits: 15},
	{ID: "individual-max", Name: "Max", MonthlyCredits: 150},
	{ID: "individual-ultra", Name: "Ultra", MonthlyCredits: 300},
	{ID: "teams-pro", Name: "Teams Pro", MonthlyCredits: 40},
}

// plansBySpecificity is planTable with the longest id first, the order the CLI
// matches in, so "individual-pro-v1" resolves to Pro-v1 rather than to a shorter
// prefix.
var plansBySpecificity = func() []plan {
	ordered := append([]plan(nil), planTable...)
	sort.SliceStable(ordered, func(i, j int) bool { return len(ordered[i].ID) > len(ordered[j].ID) })
	return ordered
}()

// matchPlan normalizes a planId (lowercase, "_" to "-") and resolves it by longest
// matching prefix. An unknown id reports false so callers keep the plan unknown
// instead of inventing a credit cap.
func matchPlan(planID string) (plan, bool) {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(planID), "_", "-"))
	if normalized == "" {
		return plan{}, false
	}
	for _, candidate := range plansBySpecificity {
		if strings.HasPrefix(normalized, candidate.ID) {
			return candidate, true
		}
	}
	return plan{}, false
}

// creditUnit reports the unit of a plan's credit amounts. An unknown plan keeps
// the unit unknown rather than claiming dollars.
func creditUnit(planID string) string {
	resolved, ok := matchPlan(planID)
	switch {
	case !ok:
		return ""
	case resolved.PerModelCredits:
		return vendors.UnitCredit
	default:
		return vendors.UnitUSD
	}
}
