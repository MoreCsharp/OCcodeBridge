package bridge

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/MoreCsharp/OCcodeBridge/internal/vendors"
)

const usageTTL = time.Minute

type usageLimit struct {
	Type        string     `json:"type"`
	PercentUsed *float64   `json:"percent_used"`
	Used        *float64   `json:"used,omitempty"`
	Cap         *float64   `json:"cap,omitempty"`
	Unit        string     `json:"unit,omitempty"`
	ResetsAt    *time.Time `json:"resets_at"`
	Status      string     `json:"status,omitempty"`
}

type usagePlan struct {
	Name             string     `json:"name"`
	CurrentPeriodEnd *time.Time `json:"current_period_end"`
}

// Only display fields cross the management API; upstream account/stripe IDs do not.
type credentialUsage struct {
	accountHash     string
	ID              string       `json:"id"`
	Limits          []usageLimit `json:"limits"`
	Plan            *usagePlan   `json:"plan"`
	Vendor          string       `json:"vendor"`
	Balance         *float64     `json:"balance,omitempty"`
	BalanceCurrency string       `json:"balance_currency,omitempty"`
	UpdatedAt       *time.Time   `json:"updated_at"`
	PlanUpdatedAt   *time.Time   `json:"plan_updated_at"`
	CheckedAt       *time.Time   `json:"checked_at"`
	Status          string       `json:"status"`
	Error           string       `json:"error,omitempty"`
	PlanError       string       `json:"plan_error,omitempty"`
}

type usageCacheEntry struct {
	fingerprint [32]byte
	value       credentialUsage
	done        chan struct{}
}

func usageFingerprint(c Credential) [32]byte {
	return sha256.Sum256(jsonBytes([]any{c.APIKey, c.ProxyURL, c.Vendor, c.ZDR}))
}

// quotaTypeForWindow maps a vendor window kind to the internal canonical type.
func quotaTypeForWindow(kind string) string {
	switch kind {
	case vendors.WindowFiveHour:
		return "five_hour"
	case vendors.WindowWeekly:
		return "weekly"
	case vendors.WindowMonthly:
		return "monthly"
	}
	return ""
}

func (s *Service) credentialUsage(r ManagementRequest) (credentialUsage, error) {
	if err := s.begin(); err != nil {
		return credentialUsage{}, err
	}
	defer s.active.Done()
	credentialID := r.Query.Get("id")
	deadline := time.Now().Add(15 * time.Second)
	for {
		s.mu.Lock()
		c, ok := s.creds[credentialID]
		if !ok || s.revoked[credentialID] {
			s.mu.Unlock()
			return credentialUsage{}, fail(404, "凭据已删除，请刷新列表")
		}
		fingerprint := usageFingerprint(c)
		entry := s.usageCache[credentialID]
		if entry == nil || entry.fingerprint != fingerprint {
			entry = &usageCacheEntry{fingerprint: fingerprint, value: credentialUsage{ID: credentialID, Vendor: c.Vendor, Limits: []usageLimit{}}}
			s.usageCache[credentialID] = entry
		}
		if done := entry.done; done != nil {
			s.mu.Unlock()
			timer := time.NewTimer(time.Until(deadline))
			select {
			case <-done:
				timer.Stop()
				continue
			case <-s.stopCh:
				timer.Stop()
				return credentialUsage{}, fail(503, "插件正在关闭")
			case <-timer.C:
				return credentialUsage{}, fail(504, "用量查询等待超时")
			}
		}
		ttl := usageTTL
		if entry.value.Status != "ok" {
			ttl = 30 * time.Second
		}
		if r.Query.Get("refresh") == "1" {
			ttl = 10 * time.Second
		}
		if entry.value.CheckedAt != nil && time.Since(*entry.value.CheckedAt) < ttl && !quotaRecoveryCheckDue(c, entry.value, time.Now()) {
			value := entry.value
			s.mu.Unlock()
			return value, nil
		}
		entry.done = make(chan struct{})
		sampleStarted := time.Now().UTC()
		value := entry.value
		s.mu.Unlock()

		// Bound work across tabs/accounts. Quota reads never enter CPA's model scheduler.
		timer := time.NewTimer(time.Until(deadline))
		select {
		case s.usageSlots <- struct{}{}:
			timer.Stop()
			value = s.fetchCredentialUsage(r.HostCallbackID, c, value, deadline)
			<-s.usageSlots
		case <-s.stopCh:
			timer.Stop()
			value.Status, value.Error = "unavailable", "插件正在关闭"
		case <-timer.C:
			value.Status, value.Error = "timeout", "查询繁忙，请稍后刷新"
		}
		now := time.Now().UTC()
		value.CheckedAt = &now
		s.mu.Lock()
		entry.value = value
		current, exists := s.creds[credentialID]
		valid := exists && !s.revoked[credentialID] && usageFingerprint(current) == fingerprint && s.usageCache[credentialID] == entry
		if valid {
			s.shareAccountUsageLocked(credentialID, value)
		}
		s.mu.Unlock()
		if valid {
			_ = s.observeQuotaUsage(c, value, sampleStarted)
		}
		// Waiters see the usage result only once quota pause/recovery is applied.
		s.mu.Lock()
		close(entry.done)
		entry.done = nil
		s.mu.Unlock()
		if !valid {
			return credentialUsage{}, fail(409, "凭据已变更，请重新查询用量")
		}
		return value, nil
	}
}

func usageFailure(err error) (string, string) {
	if errors.Is(err, vendors.ErrNotImplemented) {
		return "unavailable", "该厂商的用量接口尚未实现"
	}
	switch statusOf(err) {
	case 401, 403:
		return "unauthorized", "上游拒绝查询，请检查 API Key 或账号权限"
	case 429:
		return "rate_limited", "上游查询限流，请稍后刷新"
	case 504:
		return "timeout", "上游查询超时，请稍后刷新"
	default:
		return "unavailable", fmt.Sprintf("暂时未能获取上游用量（HTTP %d）", statusOf(err))
	}
}

// vendorErrorBody is implemented by a vendor error that kept the upstream body.
// It lets a failed management call go through the same ClassifyLimitError rules a
// failed request uses, instead of a second detector in the bridge.
type vendorErrorBody interface {
	ResponseBody() []byte
}

// usageLimitBlock classifies a failed usage call with the vendor's own limit
// rules. Only a 403 or 429 can be a limit; everything else stays a query failure.
func usageLimitBlock(v vendors.Vendor, err error, now time.Time) *vendors.LimitBlock {
	status := statusOf(err)
	if status != 403 && status != 429 {
		return nil
	}
	body := []byte(err.Error())
	var carrier vendorErrorBody
	if errors.As(err, &carrier) {
		if raw := carrier.ResponseBody(); len(raw) > 0 {
			body = raw
		}
	}
	return v.ClassifyLimitError(status, body, now)
}

// fetchCredentialUsage delegates to the credential's vendor and maps its sample
// onto the console/quota structure. Fields the vendor leaves nil stay nil.
func (s *Service) fetchCredentialUsage(callbackID string, c Credential, value credentialUsage, deadline time.Time) credentialUsage {
	v, err := vendorFor(c)
	if err != nil {
		value.Status, value.Error = "unavailable", safeError(err)
		return value
	}
	ctx, cancel := vendorContext(deadline)
	defer cancel()
	usage, err := v.FetchUsage(ctx, vendorCredential(c), s.vendorDoer(c, callbackID, deadline))
	if err != nil {
		value.Status, value.Error = usageFailure(err)
		if block := usageLimitBlock(v, err, time.Now().UTC()); block != nil && block.Permanent {
			// A plan without API access rejects every management call too, so polling
			// detects it without waiting for a failed inference request.
			value.Error = block.Reason
			value.Status = "plan_unsupported"
			_ = s.markPlanUnsupported(c, block.Reason)
		}
		return value // Keep last successful values with their original timestamp.
	}
	limits := []usageLimit{}
	for _, window := range usage.Windows {
		kind := quotaTypeForWindow(window.Kind)
		if kind == "" {
			continue
		}
		if window.UsedPercent != nil && *window.UsedPercent < 0 {
			window.UsedPercent = nil
		}
		limits = append(limits, usageLimit{Type: kind, PercentUsed: window.UsedPercent, Used: window.Used, Cap: window.Cap, Unit: window.Unit, ResetsAt: window.ResetAt, Status: window.Status})
	}
	now := time.Now().UTC()
	value.Limits, value.UpdatedAt, value.Status, value.Error = limits, &now, "ok", ""
	value.Vendor = v.ID()
	value.Balance, value.BalanceCurrency = usage.Balance, usage.BalanceCurrency
	value.accountHash, value.PlanError = "", ""
	value.Plan = nil
	if usage.PlanName != "" || usage.PeriodEnd != nil {
		value.Plan = &usagePlan{Name: usage.PlanName, CurrentPeriodEnd: usage.PeriodEnd}
	}
	if usage.AccountID != "" {
		value.accountHash = usageHash("vendor-account", v.ID()+"|"+usage.AccountID)
	}
	value.PlanUpdatedAt = &now
	return value
}

// vendorCredential narrows a stored credential to what a vendor may see.
func vendorCredential(c Credential) vendors.Credential {
	return vendors.Credential{ID: c.ID, APIKey: c.APIKey, ProxyURL: c.ProxyURL, ZDR: c.ZDR}
}
