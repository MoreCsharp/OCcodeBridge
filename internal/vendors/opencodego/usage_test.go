package opencodego

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xiao-qiu-qiu/OCcodeBridge/internal/vendors"
)

// fetchFixture serves one usage fixture and runs FetchUsage against it.
func fetchFixture(t *testing.T, name string) (vendors.Usage, *recordingDoer) {
	t.Helper()
	server, doer := serveFixture(t, http.StatusOK, fixture(t, name))
	withUsageEndpoint(t, server.URL)
	usage, err := Vendor{}.FetchUsage(t.Context(), vendors.Credential{ID: "cred-1", APIKey: "sk-usage"}, doer)
	if err != nil {
		t.Fatalf("FetchUsage(%s): %v", name, err)
	}
	assertBearer(t, doer.request, server.URL, "sk-usage")
	return usage, doer
}

func TestFetchUsageAPIKeyShape(t *testing.T) {
	usage, _ := fetchFixture(t, "usage_new.json")
	if usage.PlanName != "Go Plus" || usage.AccountID != "wrk_fixture" {
		t.Fatalf("plan/account = %q / %q", usage.PlanName, usage.AccountID)
	}
	if usage.FetchedAt.IsZero() {
		t.Fatal("FetchedAt is unset")
	}
	if len(usage.Windows) != 3 {
		t.Fatalf("windows = %#v, want 5h/7d/month", usage.Windows)
	}
	byKind := windowsByKind(t, usage.Windows)
	rolling := byKind[vendors.WindowFiveHour]
	assertFloat(t, "5h percent", rolling.UsedPercent, 12.5)
	assertTime(t, "5h reset", rolling.ResetAt, time.Date(2026, 11, 18, 14, 30, 0, 0, time.UTC))
	if rolling.Status != "ok" {
		t.Fatalf("5h status = %q", rolling.Status)
	}
	assertFloat(t, "7d percent", byKind[vendors.WindowWeekly].UsedPercent, 40.5)
	monthly := byKind[vendors.WindowMonthly]
	assertFloat(t, "month percent", monthly.UsedPercent, 72.4)
	if monthly.Status != "warning" {
		t.Fatalf("month status = %q", monthly.Status)
	}
	assertTime(t, "month reset", monthly.ResetAt, time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))
	if usage.PeriodEnd != nil {
		t.Fatalf("the API-key shape reports no paid period end, got %v", usage.PeriodEnd)
	}
}

func TestFetchUsageDerivesPercentFromUsedLimit(t *testing.T) {
	usage, _ := fetchFixture(t, "usage_used_limit.json")
	byKind := windowsByKind(t, usage.Windows)
	rolling := byKind[vendors.WindowFiveHour]
	assertFloat(t, "5h percent", rolling.UsedPercent, 25)
	assertFloat(t, "5h used", rolling.Used, 2.5)
	assertFloat(t, "5h cap", rolling.Cap, 10)

	weekly := byKind[vendors.WindowWeekly]
	assertFloat(t, "7d percent", weekly.UsedPercent, 15)
	if weekly.ResetAt == nil {
		t.Fatal("the weekly reset must be derived when the response omits it")
	}
	if weekly.ResetAt.Weekday() != time.Monday || weekly.ResetAt.Hour() != 0 || weekly.ResetAt.Minute() != 0 || weekly.ResetAt.Location() != time.UTC {
		t.Fatalf("derived weekly reset = %s, want Monday 00:00 UTC", weekly.ResetAt)
	}
	if !weekly.ResetAt.After(time.Now().Add(-time.Second)) || weekly.ResetAt.Sub(time.Now()) > 7*24*time.Hour {
		t.Fatalf("derived weekly reset %s is not the next Monday", weekly.ResetAt)
	}
	if !strings.Contains(weekly.Status, "derived") {
		t.Fatalf("derived weekly status = %q, want it marked derived", weekly.Status)
	}

	monthly := byKind[vendors.WindowMonthly]
	assertFloat(t, "month percent", monthly.UsedPercent, 100) // 120% clamped
	assertFloat(t, "month used", monthly.Used, 12)
}

func TestFetchUsageReadsMicroCents(t *testing.T) {
	usage, _ := fetchFixture(t, "usage_microcents.json")
	if len(usage.Windows) != 2 {
		t.Fatalf("windows = %#v, want only the reported 5h and month", usage.Windows)
	}
	byKind := windowsByKind(t, usage.Windows)
	rolling := byKind[vendors.WindowFiveHour]
	assertFloat(t, "5h used", rolling.Used, 0.5)
	assertFloat(t, "5h cap", rolling.Cap, 1)
	assertFloat(t, "5h percent", rolling.UsedPercent, 50)

	monthly := byKind[vendors.WindowMonthly]
	assertFloat(t, "month used", monthly.Used, 0)
	assertFloat(t, "month cap", monthly.Cap, 2.5)
	assertFloat(t, "month percent", monthly.UsedPercent, 0)
}

func TestFetchUsageLegacyConsoleShape(t *testing.T) {
	usage, _ := fetchFixture(t, "usage_legacy_console.json")
	if len(usage.Windows) != 3 {
		t.Fatalf("windows = %#v, want fiveHour/week/month", usage.Windows)
	}
	byKind := windowsByKind(t, usage.Windows)
	assertFloat(t, "5h percent", byKind[vendors.WindowFiveHour].UsedPercent, 25)
	assertFloat(t, "7d percent", byKind[vendors.WindowWeekly].UsedPercent, 20)
	monthly := byKind[vendors.WindowMonthly]
	assertFloat(t, "month percent", monthly.UsedPercent, 40)
	assertTime(t, "month reset", monthly.ResetAt, time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))
	if strings.Contains(monthly.Status, "derived") {
		t.Fatalf("the console period end is reported, not derived: %q", monthly.Status)
	}
	assertTime(t, "period end", usage.PeriodEnd, time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))
	if usage.PlanName != "" {
		t.Fatalf("plan name = %q, the console fixture reports none", usage.PlanName)
	}
	if !strings.HasPrefix(usage.AccountID, "key-") || strings.Contains(usage.AccountID, "sk-usage") {
		t.Fatalf("account id = %q, want a key hash prefix", usage.AccountID)
	}
}

func TestFetchUsageAccountIDIsStableKeyHash(t *testing.T) {
	first, _ := fetchFixture(t, "usage_legacy_console.json")
	second, _ := fetchFixture(t, "usage_legacy_console.json")
	if first.AccountID != second.AccountID {
		t.Fatalf("account id changed between samples: %q vs %q", first.AccountID, second.AccountID)
	}
}

func TestFetchUsageKeepsMissingFieldsUnknown(t *testing.T) {
	usage, _ := fetchFixture(t, "usage_partial.json")
	if len(usage.Windows) != 2 {
		t.Fatalf("windows = %#v, want only rolling and monthly", usage.Windows)
	}
	byKind := windowsByKind(t, usage.Windows)
	rolling := byKind[vendors.WindowFiveHour]
	assertNilFloat(t, "5h percent", rolling.UsedPercent)
	assertNilFloat(t, "5h used", rolling.Used)
	assertNilFloat(t, "5h cap", rolling.Cap)
	assertTime(t, "5h reset", rolling.ResetAt, time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC))

	monthly := byKind[vendors.WindowMonthly]
	assertNilFloat(t, "month percent", monthly.UsedPercent)
	assertNilFloat(t, "month used", monthly.Used)
	assertNilFloat(t, "month cap", monthly.Cap)
	if monthly.ResetAt != nil {
		t.Fatalf("month reset = %v, want nil", monthly.ResetAt)
	}
	if monthly.Status != "ok" {
		t.Fatalf("month status = %q", monthly.Status)
	}
}

func TestFetchUsageReadsEveryResetFormat(t *testing.T) {
	usage, _ := fetchFixture(t, "usage_reset_formats.json")
	byKind := windowsByKind(t, usage.Windows)
	assertTime(t, "5h reset (ISO string)", byKind[vendors.WindowFiveHour].ResetAt, time.Date(2026, 11, 18, 14, 30, 0, 0, time.UTC))
	assertTime(t, "7d reset (unix seconds)", byKind[vendors.WindowWeekly].ResetAt, time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC))
	assertTime(t, "month reset (unix milliseconds)", byKind[vendors.WindowMonthly].ResetAt, time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))
}

func TestFetchUsageRejectsUnrecognisedPayload(t *testing.T) {
	server, doer := serveFixture(t, http.StatusOK, fixture(t, "usage_unrecognised.json"))
	withUsageEndpoint(t, server.URL)
	_, err := Vendor{}.FetchUsage(t.Context(), vendors.Credential{APIKey: "sk-usage"}, doer)
	if !errors.Is(err, errUsageUnrecognised) {
		t.Fatalf("error = %v, want the unrecognised-usage error", err)
	}
}

func TestFetchUsageReportsAuthFailureWithoutTheKey(t *testing.T) {
	const secret = "sk-secret-usage"
	server, doer := serveFixture(t, http.StatusUnauthorized, `{"error":{"message":"invalid api key `+secret+`"}}`)
	withUsageEndpoint(t, server.URL)
	_, err := Vendor{}.FetchUsage(t.Context(), vendors.Credential{ID: "cred-1", APIKey: secret}, doer)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("error = %v, want an auth failure", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaks the API key: %v", err)
	}
}

func TestFetchUsageReportsUpstreamAndMalformedResponses(t *testing.T) {
	t.Run("upstream 500", func(t *testing.T) {
		server, doer := serveFixture(t, http.StatusInternalServerError, "boom")
		withUsageEndpoint(t, server.URL)
		_, err := Vendor{}.FetchUsage(t.Context(), vendors.Credential{APIKey: "sk-usage"}, doer)
		if err == nil || errors.Is(err, ErrUnauthorized) {
			t.Fatalf("error = %v, want a plain upstream failure", err)
		}
		if status, ok := err.(interface{ StatusCode() int }); !ok || status.StatusCode() != http.StatusInternalServerError {
			t.Fatalf("500 status not carried through: %v", err)
		}
	})
	t.Run("malformed JSON", func(t *testing.T) {
		server, doer := serveFixture(t, http.StatusOK, `{"usage":`)
		withUsageEndpoint(t, server.URL)
		if _, err := (Vendor{}).FetchUsage(t.Context(), vendors.Credential{APIKey: "sk-usage"}, doer); err == nil {
			t.Fatal("malformed usage JSON must be an error")
		}
	})
}
