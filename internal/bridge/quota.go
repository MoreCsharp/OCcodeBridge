package bridge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"
)

const quotaBlockMarker = "[occodebridge:quota_block]"

// A quota block is either a real window exhaustion, which usage polling can
// clear, or a plan without API access, which it cannot.
const (
	quotaReasonExhausted = "quota_exhausted"
	// quotaPlanLimit is the limit entry a permanent block carries, so the existing
	// grouping and persistence code keeps it without inventing a usage window.
	quotaPlanLimit = "plan"
	// quotaPlanUnsupportedReason is shown when the vendor named no reason of its own.
	quotaPlanUnsupportedReason = "套餐不支持 API 访问：上游拒绝为该套餐提供接口访问"
)

// Identity is verified by the authenticated plan API, never inferred from labels,
// a proxy, an error message, or an identical percentage. No upstream IDs or keys
// are stored here. The key hash prevents an old account surviving key rotation.
type QuotaIdentity struct {
	AccountHash string `json:"account_hash"`
	KeyHash     string `json:"key_hash"`
}

type QuotaBlockLimit struct {
	Type     string     `json:"type"`
	ResetsAt *time.Time `json:"resets_at,omitempty"`
}

// QuotaBlock is independent from Credential.Disabled, which always retains the
// operator's choice. Expiry permits another usage check; it does not enable auth.
type QuotaBlock struct {
	AccountHash string `json:"account_hash,omitempty"`
	KeyHash     string `json:"key_hash"`
	// CPA persists effective Auth.Disabled back into the top-level disabled field.
	// Keep the operator's choice separately for the duration of the quota block.
	ManualDisabled *bool  `json:"manual_disabled,omitempty"`
	Source         string `json:"source"`
	Reason         string `json:"reason"`
	// Permanent marks a plan that has no API access. Usage polling cannot clear
	// it; only an explicit re-check or a replaced key does.
	Permanent  bool              `json:"permanent,omitempty"`
	RetryAt    *time.Time        `json:"retry_at,omitempty"`
	BlockedAt  time.Time         `json:"blocked_at"`
	ObservedAt time.Time         `json:"observed_at"`
	Limits     []QuotaBlockLimit `json:"limits"`
}

func quotaKeyHash(c Credential) string { return usageHash("quota-key", c.APIKey) }

// usageHash keeps upstream account and key identifiers out of stored state: the
// hash identifies a key across restarts without ever storing it.
func usageHash(kind, value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(kind + "\x00" + value))
	return hex.EncodeToString(sum[:16])
}

func normalizeQuotaCredential(c Credential) Credential {
	key := quotaKeyHash(c)
	if c.QuotaIdentity != nil && (key == "" || c.QuotaIdentity.KeyHash != key || c.QuotaIdentity.AccountHash == "") {
		c.QuotaIdentity = nil
	}
	if c.QuotaBlock != nil && (key == "" || c.QuotaBlock.KeyHash != key || len(c.QuotaBlock.Limits) == 0) {
		c.QuotaBlock = nil
	}
	if c.QuotaBlock != nil {
		block, manualDisabled := *c.QuotaBlock, c.Disabled
		block.ManualDisabled = &manualDisabled
		c.QuotaBlock = &block
	}
	return c
}

func quotaStateEqual(a, b Credential) bool {
	return bytes.Equal(jsonBytes(a.QuotaBlock), jsonBytes(b.QuotaBlock)) &&
		bytes.Equal(jsonBytes(a.QuotaIdentity), jsonBytes(b.QuotaIdentity))
}

// Caller holds mu. A persisted verified identity can keep sampling and grouping
// working after a reload; unknown credentials remain distinct.
func (s *Service) quotaAccountHashLocked(c Credential) string {
	c = normalizeQuotaCredential(c)
	if c.QuotaIdentity != nil {
		return c.QuotaIdentity.AccountHash
	}
	if entry := s.usageCache[c.ID]; entry != nil && entry.fingerprint == usageFingerprint(c) {
		return entry.value.accountHash
	}
	return ""
}

func (s *Service) quotaStatus(credentialID string) map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := normalizeQuotaCredential(s.creds[credentialID])
	paused := c.QuotaBlock != nil
	// Copy via JSON so callers never receive mutable live state.
	var block map[string]any
	if c.QuotaBlock != nil {
		_ = json.Unmarshal(jsonBytes(c.QuotaBlock), &block)
		for _, field := range []string{"key_hash", "account_hash", "manual_disabled", "reason", "retry_at"} {
			delete(block, field)
		}
	}
	status := map[string]any{
		"quota_auto_pause": true, "manual_disabled": c.Disabled,
		"quota_paused": paused, "effective_disabled": c.Disabled || paused,
		"quota_block": block, "persistence_error": s.quotaWriteErrors[credentialID],
		"plan_unsupported": c.QuotaBlock != nil && c.QuotaBlock.Permanent,
	}
	if status["plan_unsupported"] == true {
		status["quota_reason"] = c.QuotaBlock.Reason
	}
	return status
}

func (s *Service) quotaPersistenceError(next Credential, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.creds[next.ID]
	if !exists || current.APIKey != next.APIKey || current.QuotaRevision != next.QuotaRevision {
		return // A late write failure must not contaminate a replaced key.
	}
	if s.quotaWriteErrors == nil {
		s.quotaWriteErrors = map[string]string{}
	}
	if err == nil {
		delete(s.quotaWriteErrors, next.ID)
	} else {
		s.quotaWriteErrors[next.ID] = safeError(err)
	}
}

// Preserve the manual choice alongside quota state and restore disabled from it
// when recovering from CPA's effective runtime flag. Let the watcher reparse the file:
// host.auth.save would also create a generic registration for rotated auths.
func (s *Service) persistQuotaCredential(next Credential) (err error) {
	s.authFileMu.Lock()
	defer s.authFileMu.Unlock()
	s.mu.Lock()
	current, exists := s.creds[next.ID]
	if !exists || current.APIKey != next.APIKey {
		s.mu.Unlock()
		return fail(409, "quota credential changed before persistence")
	}
	next.Disabled = current.Disabled
	next = normalizeQuotaCredential(next)
	next.QuotaRevision = id()
	current.QuotaRevision = next.QuotaRevision
	if next.QuotaBlock != nil {
		// Even direct callers must fail closed before touching the filesystem.
		current.QuotaBlock, current.QuotaIdentity = next.QuotaBlock, next.QuotaIdentity
	}
	s.creds[next.ID] = current
	s.mu.Unlock()
	defer func() { s.quotaPersistenceError(next, err) }()
	file, err := s.affinityFile(current)
	if err != nil {
		return err
	}
	for _, key := range []string{"quota_block", "quota_identity", "quota_revision"} {
		delete(file.updated, key)
	}
	if next.QuotaBlock != nil {
		file.updated["quota_block"] = jsonBytes(next.QuotaBlock)
	}
	if next.QuotaIdentity != nil {
		file.updated["quota_identity"] = jsonBytes(next.QuotaIdentity)
	}
	file.updated["disabled"] = jsonBytes(current.Disabled)
	file.updated["quota_revision"] = jsonBytes(next.QuotaRevision)
	// Preserve an external edit rather than replacing its fields with a stale copy.
	latest, err := os.ReadFile(file.path)
	if err != nil || !bytes.Equal(latest, file.original) {
		return fail(409, "quota credential file changed before persistence")
	}
	if err = atomicJSON(file.path, file.updated); err != nil {
		return fmt.Errorf("persist quota state: %w", err)
	}
	s.mu.Lock()
	if c, ok := s.creds[next.ID]; ok && c.APIKey == next.APIKey && c.QuotaRevision == next.QuotaRevision {
		c.QuotaBlock, c.QuotaIdentity, c.QuotaRevision = next.QuotaBlock, next.QuotaIdentity, next.QuotaRevision
		s.creds[next.ID] = c
	}
	s.mu.Unlock()
	return nil
}

// Retry failed quota writes without changing any operator-owned credential fields.
func (s *Service) reconcileQuotaScheduling() error {
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	s.mu.RLock()
	var creds []Credential
	for _, c := range s.creds {
		if s.quotaWriteErrors[c.ID] != "" {
			creds = append(creds, normalizeQuotaCredential(c))
		}
	}
	s.mu.RUnlock()
	var errs []error
	for _, c := range creds {
		if err := s.persistQuotaCredential(c); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func canonicalQuotaType(value string) string {
	switch strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(value), "-", "_"), " ", "_")) {
	case "weekly", "week":
		return "weekly"
	case "five_hour", "five_hours", "5_hour", "5_hours":
		return "five_hour"
	case "monthly", "month":
		return "monthly"
	case "daily", "day":
		return "daily"
	case "quota":
		return "quota"
	}
	return ""
}

func mergeQuotaBlocks(c Credential, blocks ...*QuotaBlock) *QuotaBlock {
	var result *QuotaBlock
	limits := map[string]QuotaBlockLimit{}
	permanent := false
	permanentReason := ""
	for _, block := range blocks {
		if block == nil {
			continue
		}
		if result == nil {
			copy := *block
			result = &copy
		}
		if block.BlockedAt.Before(result.BlockedAt) {
			result.BlockedAt = block.BlockedAt
		}
		if block.ObservedAt.After(result.ObservedAt) {
			result.ObservedAt, result.Source = block.ObservedAt, block.Source
		}
		if block.Permanent {
			permanent = true
			if reason := strings.TrimSpace(block.Reason); reason != "" {
				permanentReason = reason
			}
		}
		for _, limit := range block.Limits {
			old, exists := limits[limit.Type]
			if !exists || old.ResetsAt == nil || limit.ResetsAt != nil && limit.ResetsAt.After(*old.ResetsAt) {
				limits[limit.Type] = limit
			}
		}
	}
	if result == nil {
		return nil
	}
	result.KeyHash = quotaKeyHash(c)
	manualDisabled := c.Disabled
	result.ManualDisabled = &manualDisabled
	result.Reason = quotaReasonExhausted
	if c.QuotaIdentity != nil {
		result.AccountHash = c.QuotaIdentity.AccountHash
	}
	if permanent || result.Permanent {
		// A plan without API access replaces any window limit: it has no reset
		// time and must never be cleared by a usage sample.
		result.Permanent = true
		result.Reason = quotaPlanUnsupportedReason
		if permanentReason != "" {
			result.Reason = permanentReason
		}
		result.RetryAt = nil
		result.Limits = []QuotaBlockLimit{{Type: quotaPlanLimit}}
		sort.Slice(result.Limits, func(i, j int) bool { return result.Limits[i].Type < result.Limits[j].Type })
		return result
	}
	result.RetryAt = nil
	result.Limits = nil
	for _, limit := range limits {
		result.Limits = append(result.Limits, limit)
		if limit.ResetsAt != nil && (result.RetryAt == nil || limit.ResetsAt.After(*result.RetryAt)) {
			value := *limit.ResetsAt
			result.RetryAt = &value
		}
	}
	sort.Slice(result.Limits, func(i, j int) bool { return result.Limits[i].Type < result.Limits[j].Type })
	return result
}

// Caller holds mu. Only source and independently verified sibling keys join.
func (s *Service) quotaGroupLocked(source Credential) []Credential {
	account := s.quotaAccountHashLocked(source)
	var group []Credential
	for _, c := range s.creds {
		if s.revoked[c.ID] || c.ID != source.ID && (account == "" || s.quotaAccountHashLocked(c) != account) {
			continue
		}
		c = normalizeQuotaCredential(c)
		if account != "" {
			c.QuotaIdentity = &QuotaIdentity{account, quotaKeyHash(c)}
		}
		group = append(group, c)
	}
	sort.Slice(group, func(i, j int) bool { return group[i].ID < group[j].ID })
	return group
}

func (s *Service) saveQuotaGroup(group []Credential) error {
	// Publish every sibling's block together before the first potentially slow IO.
	var writes []Credential
	s.mu.Lock()
	for _, next := range group {
		current, exists := s.creds[next.ID]
		if !exists || current.APIKey != next.APIKey {
			continue
		}
		changed := !quotaStateEqual(current, next)
		if changed || s.quotaWriteErrors[next.ID] != "" {
			writes = append(writes, next)
		}
		if changed && next.QuotaBlock != nil {
			current.QuotaBlock, current.QuotaIdentity = next.QuotaBlock, next.QuotaIdentity
			current.QuotaRevision = id()
			s.creds[next.ID] = current
		}
	}
	s.mu.Unlock()
	var errs []error
	for _, next := range writes {
		if err := s.persistQuotaCredential(next); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// applyQuotaBlock publishes one new block to the credential and its verified
// siblings. Stale keys and removed credentials are ignored.
func (s *Service) applyQuotaBlock(source Credential, block *QuotaBlock) error {
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	s.mu.RLock()
	current, exists := s.creds[source.ID]
	if !exists || current.APIKey != source.APIKey || s.revoked[source.ID] {
		s.mu.RUnlock()
		return nil
	}
	group := s.quotaGroupLocked(current)
	s.mu.RUnlock()
	for i := range group {
		group[i].QuotaBlock = mergeQuotaBlocks(group[i], group[i].QuotaBlock, block)
	}
	s.wakeUsageSampler()
	return s.saveQuotaGroup(group)
}

// planUnsupportedBlock is the permanent state for a plan without API access. It
// carries no reset time: only a re-check or a new key clears it.
func planUnsupportedBlock(source, reason string, now time.Time) *QuotaBlock {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = quotaPlanUnsupportedReason
	}
	return &QuotaBlock{Source: source, Reason: reason, Permanent: true, BlockedAt: now, ObservedAt: now, Limits: []QuotaBlockLimit{{Type: quotaPlanLimit}}}
}

// Main executor integrates this hook on an upstream error. No error or usage
// text alone may merge unknown keys into an account, and stale rotated keys are ignored.
func (s *Service) observeQuotaError(source Credential, err error) error {
	var upstream *upstreamError
	if !errors.As(err, &upstream) || upstream.Local {
		return nil
	}
	now := time.Now().UTC()
	switch upstream.Kind {
	case "upstream_quota_exhausted":
		typeName := upstream.QuotaType
		if typeName == "" {
			typeName = "quota"
		}
		return s.applyQuotaBlock(source, &QuotaBlock{Source: "error", BlockedAt: now, ObservedAt: now, Limits: []QuotaBlockLimit{{Type: typeName, ResetsAt: upstream.QuotaResetAt}}})
	case "upstream_plan_unsupported":
		return s.applyQuotaBlock(source, planUnsupportedBlock("error", upstream.Message, now))
	}
	return nil
}

// markPlanUnsupported records the same permanent state from a failed usage call,
// so polling detects a plan without API access even when no request failed.
func (s *Service) markPlanUnsupported(source Credential, reason string) error {
	return s.applyQuotaBlock(source, planUnsupportedBlock("usage", reason, time.Now().UTC()))
}

// clearPermanentQuota removes a plan-unsupported state after a successful
// re-check. Real window blocks keep their own polling-based recovery.
func (s *Service) clearPermanentQuota(credentialID string) error {
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	s.mu.RLock()
	current, exists := s.creds[credentialID]
	if !exists || s.revoked[credentialID] {
		s.mu.RUnlock()
		return fail(404, "凭据已删除，请刷新列表")
	}
	if current.QuotaBlock == nil || !current.QuotaBlock.Permanent {
		s.mu.RUnlock()
		return nil
	}
	group := s.quotaGroupLocked(current)
	s.mu.RUnlock()
	changed := false
	for i := range group {
		if group[i].QuotaBlock != nil && group[i].QuotaBlock.Permanent {
			group[i].QuotaBlock = nil
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.saveQuotaGroup(group)
}

// quotaUnresetRecheck is the fallback interval for a block whose window has no
// reset time: polling still clears it once the window is below 100%, and this
// bound guarantees a fresh sample instead of waiting for a vendor timestamp.
const quotaUnresetRecheck = 10 * time.Minute

// quotaUsageHasRoom reports whether a usage sample may clear a block.
func quotaUsageHasRoom(block *QuotaBlock, value credentialUsage, started time.Time) bool {
	if block == nil || block.Permanent || value.Status != "ok" || value.PlanError != "" || value.UpdatedAt == nil || !started.After(block.ObservedAt) {
		return false
	}
	known := map[string]usageLimit{}
	for _, limit := range value.Limits {
		if kind := canonicalQuotaType(limit.Type); kind != "" {
			if limit.PercentUsed == nil || *limit.PercentUsed < 0 || math.IsNaN(*limit.PercentUsed) || math.IsInf(*limit.PercentUsed, 0) {
				return false
			}
			known[kind] = limit
			if *limit.PercentUsed >= 100 {
				return false
			}
		}
	}
	for _, blocked := range block.Limits {
		if blocked.ResetsAt != nil && started.Before(*blocked.ResetsAt) {
			return false
		}
		if blocked.Type == "quota" {
			if len(known) == 0 {
				return false
			}
		} else if _, present := known[blocked.Type]; !present {
			return false
		}
	}
	return true
}

// Runs only after an actual usage response, outside mu. Manual Disabled and
// quota blocks never stop usage queries; recovery only clears quota-owned state.
func (s *Service) observeQuotaUsage(source Credential, value credentialUsage, started time.Time) error {
	if value.Status != "ok" || value.UpdatedAt == nil {
		return nil
	}
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	s.mu.Lock()
	current, exists := s.creds[source.ID]
	if !exists || s.revoked[source.ID] || usageFingerprint(current) != usageFingerprint(source) {
		s.mu.Unlock()
		return nil
	}
	if value.accountHash != "" {
		current.QuotaIdentity = &QuotaIdentity{value.accountHash, quotaKeyHash(current)}
		// Include the new identity in grouping, but don't mark it durable yet.
		if entry := s.usageCache[source.ID]; entry != nil {
			entry.value.accountHash = value.accountHash
		}
	}
	group := s.quotaGroupLocked(current)
	s.mu.Unlock()
	var blocks []*QuotaBlock
	for _, c := range group {
		blocks = append(blocks, c.QuotaBlock)
	}
	exhausted := &QuotaBlock{Source: "usage", BlockedAt: *value.UpdatedAt, ObservedAt: *value.UpdatedAt}
	if s.config().PauseOnWindowLimit {
		for _, limit := range value.Limits {
			kind := canonicalQuotaType(limit.Type)
			if kind != "" && kind != "quota" && usageWindowExhausted(limit) {
				exhausted.Limits = append(exhausted.Limits, QuotaBlockLimit{kind, limit.ResetsAt})
			}
		}
	}
	if len(exhausted.Limits) > 0 {
		blocks = append(blocks, exhausted)
	}
	for i := range group {
		if group[i].QuotaBlock != nil && group[i].QuotaBlock.Permanent {
			continue // Only an explicit re-check or a replaced key clears this state.
		}
		block := mergeQuotaBlocks(group[i], blocks...)
		if len(exhausted.Limits) == 0 && quotaUsageHasRoom(block, value, started) {
			block = nil
		}
		group[i].QuotaBlock = block
	}
	return s.saveQuotaGroup(group)
}

// usageWindowExhausted reports a window that is fully used, from the reported
// amount when present and from the percentage otherwise.
func usageWindowExhausted(limit usageLimit) bool {
	used, ok := quotaUsedFraction(limit)
	return ok && used >= 1
}

// A reset must trigger a fresh fetch even if the old cached sample's TTL remains.
func quotaRecoveryCheckDue(c Credential, value credentialUsage, now time.Time) bool {
	c = normalizeQuotaCredential(c)
	if c.QuotaBlock == nil || c.QuotaBlock.Permanent {
		return false
	}
	fallback := c.QuotaBlock.ObservedAt.Add(quotaUnresetRecheck)
	for _, limit := range c.QuotaBlock.Limits {
		if limit.ResetsAt != nil {
			if !now.Before(*limit.ResetsAt) && (value.CheckedAt == nil || value.CheckedAt.Before(*limit.ResetsAt)) {
				return true
			}
			continue
		}
		// A window the vendor gave no reset time for must not stay blocked on a
		// stale sample: re-check on the fallback interval and let polling decide.
		if !now.Before(fallback) && (value.CheckedAt == nil || value.CheckedAt.Before(fallback)) {
			return true
		}
	}
	return false
}
