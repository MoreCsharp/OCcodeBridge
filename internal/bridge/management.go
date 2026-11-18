package bridge

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xiao-qiu-qiu/PlanBridge/internal/vendors"
)

//go:embed ui/*
var ui embed.FS

const apiBase = "/v0/management/planbridge"

func (s *Service) registerManagement(raw json.RawMessage) (any, error) {
	s.startUsageSampler()
	routes := []map[string]string{}
	for _, p := range []string{"status", "logs", "statistics", "models", "config", "credentials", "credentials/usage", "scheduling", "egress", "error-rules", "vendors"} {
		routes = append(routes, map[string]string{"Method": "GET", "Path": apiBase + "/" + p})
	}
	for _, p := range []string{"models/refresh", "models/test", "credentials", "credentials/clear-affinity", "credentials/recheck", "statistics/reset", "scheduling/reset", "error-rules/sync", "error-rules/reset"} {
		routes = append(routes, map[string]string{"Method": "POST", "Path": apiBase + "/" + p})
	}
	for _, p := range []string{"models", "models/reasoning", "config", "credentials", "scheduling/credential", "egress", "error-rules"} {
		routes = append(routes, map[string]string{"Method": "PUT", "Path": apiBase + "/" + p})
	}
	routes = append(routes, map[string]string{"Method": "DELETE", "Path": apiBase + "/credentials"})
	return map[string]any{"routes": routes, "resources": []map[string]string{{"Path": "/console", "Menu": "PlanBridge", "Description": "OpenCode Go / CommandCode 模型、凭据与用量"}}}, nil
}
func managementJSON(code int, v any) (any, error) {
	return ManagementResponse{StatusCode: code, Headers: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}, "Cache-Control": []string{"no-store"}}, Body: jsonBytes(v)}, nil
}
func (s *Service) management(raw json.RawMessage) (any, error) {
	var r ManagementRequest
	if e := json.Unmarshal(raw, &r); e != nil {
		return nil, e
	}
	if r.Method == "GET" && r.Path == "/v0/resource/plugins/"+PluginID+"/console" {
		b, e := ui.ReadFile("ui/index.html")
		if e != nil {
			return nil, e
		}
		b = []byte(strings.ReplaceAll(string(b), "__PLANBRIDGE_API_BASE__", apiBase))
		logo, err := ui.ReadFile("ui/planbridge-logo.png")
		if err != nil {
			return nil, err
		}
		b = []byte(strings.ReplaceAll(string(b), "__PLANBRIDGE_LOGO_DATA__", "data:image/png;base64,"+base64.StdEncoding.EncodeToString(logo)))
		authJS, err := ui.ReadFile("ui/cpa-auth.js")
		if err != nil {
			return nil, err
		}
		b = []byte(strings.ReplaceAll(string(b), "/*__CPA_AUTH_COMPAT__*/", string(authJS)))
		schedulingJS, err := ui.ReadFile("ui/scheduling.js")
		if err != nil {
			return nil, err
		}
		b = []byte(strings.ReplaceAll(string(b), "/*__SCHEDULING_MODULE__*/", string(schedulingJS)))
		if strings.Contains(string(b), "/*__ERROR_RULES_MODULE__*/") {
			errorRulesJS, err := ui.ReadFile("ui/error-rules.js")
			if err != nil {
				return nil, err
			}
			b = []byte(strings.ReplaceAll(string(b), "/*__ERROR_RULES_MODULE__*/", string(errorRulesJS)))
		}
		if strings.Contains(string(b), "/*__RELEASE_NOTICE_MODULE__*/") {
			releaseNoticeJS, err := ui.ReadFile("ui/release-notice.js")
			if err != nil {
				return nil, err
			}
			b = []byte(strings.ReplaceAll(string(b), "/*__RELEASE_NOTICE_MODULE__*/", string(releaseNoticeJS)))
		}
		return ManagementResponse{StatusCode: 200, Headers: http.Header{"Content-Type": []string{"text/html; charset=utf-8"}, "Cache-Control": []string{"no-store"}, "X-Content-Type-Options": []string{"nosniff"}}, Body: b}, nil
	}
	if !strings.HasPrefix(r.Path, apiBase+"/") {
		return managementJSON(404, map[string]any{"error": "not found"})
	}
	p := strings.TrimPrefix(r.Path, apiBase)
	if r.Method == "PUT" && (p == "/config" || p == "/models" || p == "/models/reasoning") {
		s.configMu.Lock()
		defer s.configMu.Unlock()
	}
	switch r.Method + " " + p {
	case "GET /error-rules", "PUT /error-rules", "POST /error-rules/sync", "POST /error-rules/reset":
		return s.errorRulesManagement(r)
	case "GET /scheduling":
		return s.schedulingResponse()
	case "PUT /scheduling/credential":
		return s.schedulingSetCredential(r)
	case "POST /scheduling/reset":
		return s.resetScheduling(r)
	case "GET /egress":
		return s.egressManagementResponse()
	case "PUT /egress":
		if _, err := s.saveEgressConfig(r.Body); err != nil {
			return managementJSON(statusOf(err), map[string]any{"error": safeError(err)})
		}
		return s.egressManagementResponse()
	case "GET /status":
		s.mu.RLock()
		logError := s.logWriteError
		s.mu.RUnlock()
		return managementJSON(200, map[string]any{"version": Version, "log_persistence_error": logError, "credential_count": len(s.credentials()), "model_count": len(s.config().Models)})
	case "GET /logs":
		return s.logsResponse(r)
	case "GET /statistics":
		return s.statisticsResponse(r)
	case "POST /statistics/reset":
		return s.resetStatistics(r)
	case "GET /config":
		return managementJSON(200, s.config())
	case "PUT /config":
		cfg := s.config()
		dataDir := cfg.DataDir
		if e := decodeConfigJSON(r.Body, &cfg); e != nil {
			return managementJSON(400, map[string]any{"error": "invalid config JSON"})
		}
		cfg.DataDir = dataDir
		if e := s.saveConfig(cfg); e != nil {
			return managementJSON(statusOf(e), map[string]any{"error": safeError(e)})
		}
		return managementJSON(200, cfg)
	case "GET /models":
		return managementJSON(200, map[string]any{"models": s.config().Models})
	case "PUT /models/reasoning":
		var in struct {
			Model      string                  `json:"model"`
			UpstreamID string                  `json:"upstream_id"`
			Mapping    *ReasoningMappingConfig `json:"reasoning_mapping"`
		}
		if err := json.Unmarshal(r.Body, &in); err != nil || in.Mapping == nil {
			return managementJSON(400, map[string]any{"error": "invalid model reasoning mapping"})
		}
		cfg := s.config()
		for i := range cfg.Models {
			if cfg.Models[i].ID != in.Model {
				continue
			}
			if cfg.Models[i].UpstreamID != in.UpstreamID {
				return managementJSON(409, map[string]any{"error": "模型已变更，请刷新后重试"})
			}
			cfg.Models[i].ReasoningMapping = in.Mapping
			if err := s.saveConfig(cfg); err != nil {
				return managementJSON(statusOf(err), map[string]any{"error": safeError(err)})
			}
			return managementJSON(200, map[string]any{"models": cfg.Models})
		}
		return managementJSON(404, map[string]any{"error": "模型不存在，请刷新后重试"})
	case "PUT /models":
		var in struct {
			Models *[]Model `json:"models"`
		}
		if e := json.Unmarshal(r.Body, &in); e != nil {
			return managementJSON(400, map[string]any{"error": "invalid model JSON"})
		}
		cfg := s.config()
		if in.Models == nil {
			return managementJSON(400, map[string]any{"error": "models must be an array"})
		}
		cfg.Models = *in.Models
		if e := s.saveConfig(cfg); e != nil {
			return managementJSON(statusOf(e), map[string]any{"error": safeError(e)})
		}
		return managementJSON(200, map[string]any{"models": cfg.Models, "message": "Saved. CPA credential registrations have been refreshed."})
	case "POST /models/refresh":
		models, e := s.refreshModels(r)
		if e != nil {
			return managementJSON(statusOf(e), map[string]any{"error": safeError(e)})
		}
		return managementJSON(200, map[string]any{"models": models})
	case "POST /models/test":
		return s.testModel(r)
	case "GET /vendors":
		items := []map[string]any{}
		for _, id := range vendors.IDs() {
			v, _ := vendors.Get(id)
			items = append(items, map[string]any{"id": v.ID(), "name": v.DisplayName(), "base_url": v.BaseURL(), "supports_zdr": id == "commandcode"})
		}
		return managementJSON(200, map[string]any{"items": items})
	case "POST /credentials/clear-affinity":
		return s.clearAffinity(r)
	case "POST /credentials/recheck":
		return s.recheckCredential(r)
	case "GET /credentials":
		return managementJSON(200, map[string]any{"items": s.credentials()})
	case "GET /credentials/usage":
		usage, err := s.credentialUsage(r)
		if err != nil {
			return managementJSON(statusOf(err), map[string]any{"error": safeError(err)})
		}
		return managementJSON(200, usage)
	case "POST /credentials":
		return s.importCredential(r)
	case "PUT /credentials":
		return s.updateCredential(r)
	case "DELETE /credentials":
		return s.deleteCredential(r.Query.Get("id"))
	default:
		return managementJSON(404, map[string]any{"error": "not found"})
	}
}
func (s *Service) saveConfig(cfg Config) error {
	if e := cfg.validate(); e != nil {
		return e
	}
	if e := saveSettings(cfg); e != nil {
		return e
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	return errors.Join(s.refreshRegistrations(), s.reconcileQuotaScheduling())
}
func (s *Service) logsResponse(r ManagementRequest) (any, error) {
	limit, _ := strconv.Atoi(r.Query.Get("limit"))
	if limit < 1 || limit > 200 {
		limit = 50
	}
	offset, _ := strconv.Atoi(r.Query.Get("offset"))
	if offset < 0 {
		offset = 0
	}
	search := strings.ToLower(r.Query.Get("search"))
	status := r.Query.Get("status")
	provider := r.Query.Get("provider")
	degradedFilter := r.Query.Get("degraded")
	s.mu.RLock()
	defer s.mu.RUnlock()
	filtered := []LogEntry{}
	for i := len(s.logs) - 1; i >= 0; i-- {
		v := s.logs[i]
		label := ""
		if v.UpstreamSkipped {
			label = localBackoffLabel
		}
		reasoning := v.ReasoningEffort
		if v.ReasoningMapping != nil {
			reasoning += " " + v.ReasoningMapping.From + " " + v.ReasoningMapping.To
			if v.ReasoningMapping.From == "unset" || v.ReasoningMapping.To == "unset" {
				reasoning += " 未设置"
			}
		}
		if search != "" && !strings.Contains(strings.ToLower(v.ID+" "+v.LastRequestID+" "+v.Model+" "+v.UpstreamModel+" "+v.ServedModel+" "+v.Provider+" "+v.Error+" "+v.Credential+" "+reasoning+" "+v.ErrorKind+" "+label), search) {
			continue
		}
		if provider != "" && provider != "all" && !matchesProvider(provider, v) {
			continue
		}
		if degradedFilter != "" && degradedFilter != "all" {
			if degradedFilter == "yes" && !v.Degraded || degradedFilter == "no" && v.Degraded {
				continue
			}
		}
		if status != "" && status != "all" {
			if status == "success" && v.Status >= 400 || status == "error" && v.Status < 400 {
				continue
			}
			if n, e := strconv.Atoi(status); e == nil && n != v.Status {
				continue
			}
		}
		v.OutputTokensPerSecond = outputTokenRate(v)
		if !s.cfg.Debug {
			v.StreamDiagnostics = nil
		}
		filtered = append(filtered, v)
	}
	sort.SliceStable(filtered, func(i, j int) bool { return logLastTime(filtered[i]).After(logLastTime(filtered[j])) })
	total := len(filtered)
	var requests, prompt, completion, cached int64
	for _, entry := range filtered {
		requests += logRequestCount(entry)
		prompt += entry.PromptTokens
		completion += entry.CompletionTokens
		cached += entry.CachedTokens
	}
	var cacheRate any
	if prompt > 0 {
		cacheRate = float64(cached) / float64(prompt)
	}
	summary := map[string]any{"requests": requests, "prompt_tokens": prompt, "completion_tokens": completion, "cached_tokens": cached, "cache_rate": cacheRate}
	if offset >= total {
		offset = 0
		if total > 0 {
			offset = ((total - 1) / limit) * limit
		}
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return managementJSON(200, map[string]any{"items": filtered[offset:end], "total": total, "summary": summary, "offset": offset, "debug": s.cfg.Debug})
}

// matchesProvider lets the UI filter by the label it shows for a locally
// suppressed request, which has no upstream provider of its own.
func matchesProvider(filter string, entry LogEntry) bool {
	if filter == localBackoffLabel {
		return entry.UpstreamSkipped
	}
	return strings.EqualFold(filter, entry.Provider)
}

func (s *Service) importCredential(r ManagementRequest) (any, error) {
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	var in struct {
		Label    string `json:"label"`
		APIKey   string `json:"api_key"`
		Vendor   string `json:"vendor"`
		ZDR      bool   `json:"zdr"`
		Priority *int   `json:"priority"`
	}
	if e := json.Unmarshal(r.Body, &in); e != nil {
		return managementJSON(400, map[string]any{"error": "invalid credential JSON"})
	}
	in.APIKey = strings.TrimSpace(in.APIKey)
	if len(in.APIKey) < 8 || strings.ContainsAny(in.APIKey, "\r\n") {
		return managementJSON(400, map[string]any{"error": "invalid API key"})
	}
	if len(in.Label) > 100 {
		return managementJSON(400, map[string]any{"error": "label exceeds 100 characters"})
	}
	// The vendor decides the upstream, so it is required at import time.
	v, err := vendorFor(Credential{Vendor: strings.TrimSpace(in.Vendor)})
	if err != nil {
		return managementJSON(statusOf(err), map[string]any{"error": safeError(err)})
	}
	c := Credential{Type: Provider, ID: PluginID + "-" + id(), Label: strings.TrimSpace(in.Label), APIKey: in.APIKey, Vendor: v.ID(), ZDR: in.ZDR, RequestScopedErrors: s.currentErrorRules(), Priority: normalizedPriority(in.Priority)}
	if c.Label == "" {
		c.Label = v.DisplayName()
	}
	var saved struct {
		Path string `json:"path"`
	}
	if e := s.call("host.auth.save", map[string]any{"name": c.ID + ".json", "json": json.RawMessage(jsonBytes(c))}, &saved); e != nil {
		return managementJSON(500, map[string]any{"error": "credential persistence failed"})
	}
	s.mu.Lock()
	s.creds[c.ID] = c
	s.markErrorRulesSyncedLocked(c.ID, c.RequestScopedErrors, nil)
	s.authFiles[c.ID] = c.ID + ".json"
	if saved.Path != "" {
		s.authDir = filepath.Dir(saved.Path)
	}
	s.mu.Unlock()
	return managementJSON(201, map[string]any{"id": c.ID, "label": c.Label, "enabled": true})
}
func (s *Service) updateCredential(r ManagementRequest) (any, error) {
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	var in struct {
		Label    *string `json:"label"`
		APIKey   *string `json:"api_key"`
		Vendor   *string `json:"vendor"`
		ZDR      *bool   `json:"zdr"`
		Priority *int    `json:"priority"`
	}
	if e := json.Unmarshal(r.Body, &in); e != nil {
		return managementJSON(400, map[string]any{"error": "invalid credential JSON"})
	}
	id := r.Query.Get("id")
	s.mu.RLock()
	c, ok := s.creds[id]
	filename := s.authFiles[id]
	s.mu.RUnlock()
	if !ok {
		return managementJSON(404, map[string]any{"error": "credential not found"})
	}
	baseline := c
	if in.Label != nil {
		c.Label = strings.TrimSpace(*in.Label)
		if len(c.Label) > 100 {
			return managementJSON(400, map[string]any{"error": "label exceeds 100 characters"})
		}
		if c.Label == "" {
			c.Label = c.Vendor
		}
	}
	if in.Vendor != nil {
		// Switching vendor changes the upstream base, headers and model set.
		v, err := vendorFor(Credential{Vendor: strings.TrimSpace(*in.Vendor)})
		if err != nil {
			return managementJSON(statusOf(err), map[string]any{"error": safeError(err)})
		}
		c.Vendor = v.ID()
	}
	if in.ZDR != nil {
		c.ZDR = *in.ZDR
	}
	if in.Priority != nil {
		c.Priority = normalizedPriority(in.Priority)
	}
	if in.APIKey != nil && strings.TrimSpace(*in.APIKey) != "" {
		key := strings.TrimSpace(*in.APIKey)
		if len(key) < 8 || strings.ContainsAny(key, "\r\n") {
			return managementJSON(400, map[string]any{"error": "invalid API key"})
		}
		if key != c.APIKey {
			// A replaced key speaks for another subscription, so an old quota
			// block, including a plan without API access, no longer applies.
			c.QuotaBlock, c.QuotaIdentity, c.QuotaRevision = nil, nil, ""
		}
		c.APIKey = key
	}
	if filename == "" {
		filename = c.ID + ".json"
	}
	if filepath.Base(filename) != filename || strings.ContainsAny(filename, "/\\") {
		return managementJSON(400, map[string]any{"error": "invalid credential filename"})
	}
	c.RequestScopedErrors = s.currentErrorRules()
	if e := s.saveCredential(c, baseline); e != nil {
		return managementJSON(500, map[string]any{"error": "credential persistence failed"})
	}
	return managementJSON(200, map[string]any{"id": c.ID, "label": c.Label, "vendor": c.Vendor, "enabled": !c.Disabled, "priority": c.Priority})
}

// recheckCredential runs one usage query and clears a plan-unsupported state when
// the vendor answers normally. A failing query keeps the state and reports why.
func (s *Service) recheckCredential(r ManagementRequest) (any, error) {
	var in struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(r.Body, &in) != nil || in.ID == "" {
		return managementJSON(400, map[string]any{"error": "请选择要重新检测的凭据"})
	}
	// An operator-initiated check must not reuse a sample taken before it, even a
	// failed one from seconds ago.
	s.mu.Lock()
	if entry := s.usageCache[in.ID]; entry != nil && entry.done == nil {
		entry.value.CheckedAt = nil
	}
	s.mu.Unlock()
	value, err := s.credentialUsage(ManagementRequest{Query: url.Values{"id": {in.ID}, "refresh": {"1"}}, HostCallbackID: r.HostCallbackID})
	if err != nil {
		return managementJSON(statusOf(err), map[string]any{"error": safeError(err)})
	}
	if value.Status != "ok" {
		return managementJSON(200, map[string]any{"cleared": false, "status": value.Status, "error": value.Error})
	}
	if err := s.clearPermanentQuota(in.ID); err != nil {
		return managementJSON(statusOf(err), map[string]any{"error": safeError(err)})
	}
	return managementJSON(200, map[string]any{"cleared": true, "status": value.Status})
}

func (s *Service) deleteCredential(credentialID string) (any, error) {
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	// Serialize removal with watcher reconciliation, which can replace the file.
	s.authFileMu.Lock()
	defer s.authFileMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.creds[credentialID]
	if !ok {
		return managementJSON(404, map[string]any{"error": "credential not found"})
	}
	if s.authDir == "" {
		return managementJSON(409, map[string]any{"error": "credential storage directory not resolved"})
	}
	if filepath.Base(c.ID) != c.ID || strings.ContainsAny(c.ID, "/\\") {
		return managementJSON(400, map[string]any{"error": "invalid credential ID"})
	}
	filename := s.authFiles[c.ID]
	if filename == "" {
		filename = c.ID + ".json"
	}
	if filepath.Base(filename) != filename || strings.ContainsAny(filename, "/\\") {
		return managementJSON(400, map[string]any{"error": "invalid credential filename"})
	}
	path := filepath.Join(s.authDir, filename)
	b, e := os.ReadFile(path)
	if e != nil {
		return managementJSON(409, map[string]any{"error": "credential file missing"})
	}
	var disk Credential
	if json.Unmarshal(b, &disk) != nil || disk.Type != Provider || disk.ID != c.ID {
		return managementJSON(409, map[string]any{"error": "credential file ownership mismatch"})
	}
	if e = os.Remove(path); e != nil {
		return managementJSON(500, map[string]any{"error": "credential removal failed"})
	}
	delete(s.creds, credentialID)
	delete(s.usageCache, credentialID)
	delete(s.authFiles, credentialID)
	s.revoked[credentialID] = true
	return managementJSON(200, map[string]any{"deleted": true})
}

// refreshModels asks one vendor for its catalog. The credential may be named
// explicitly; otherwise the highest-priority enabled credential of that vendor is
// used, because catalogs differ per vendor and some require a key.
func (s *Service) refreshModels(r ManagementRequest) ([]Model, error) {
	if err := s.begin(); err != nil {
		return nil, err
	}
	defer s.active.Done()
	var in struct {
		Vendor       string `json:"vendor"`
		CredentialID string `json:"credential_id"`
	}
	if len(r.Body) > 0 {
		if e := json.Unmarshal(r.Body, &in); e != nil {
			return nil, fail(400, "invalid model refresh JSON")
		}
	}
	v, err := vendorFor(Credential{Vendor: strings.TrimSpace(in.Vendor)})
	if err != nil {
		return nil, err
	}
	credential, err := s.catalogCredential(in.CredentialID, v.ID())
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(15 * time.Second)
	ctx, cancel := vendorContext(deadline)
	defer cancel()
	catalog, err := v.ListModels(ctx, vendorCredential(credential), s.vendorDoer(credential, r.HostCallbackID, deadline))
	if err != nil {
		if errors.Is(err, vendors.ErrNotImplemented) {
			return nil, fail(501, v.DisplayName()+" 的模型目录尚未实现，请在「添加模型」中手动填写上游模型 ID")
		}
		return nil, err
	}
	models := []Model{}
	seen := map[string]bool{}
	for _, m := range catalog {
		upstreamID := m.UpstreamID
		if upstreamID == "" {
			upstreamID = vendors.UpstreamID(v.ID(), m.ID)
		}
		// P0 only sends chat-completions, so a model that names its endpoints
		// without /chat/completions is not offerable yet.
		if len(m.Endpoints) > 0 && !supportsChatCompletions(m.Endpoints) {
			continue
		}
		if seen[upstreamID] || strings.ContainsAny(upstreamID, "\r\n\t") {
			continue
		}
		seen[upstreamID] = true
		alias := m.ID
		if alias == "" {
			alias = strings.TrimPrefix(upstreamID, v.ID()+"/")
		}
		models = append(models, Model{ID: alias, UpstreamID: upstreamID})
	}
	return models, nil
}

func supportsChatCompletions(endpoints []string) bool {
	for _, endpoint := range endpoints {
		if strings.TrimSpace(endpoint) == "/chat/completions" {
			return true
		}
	}
	return false
}

// catalogCredential picks the credential whose key lists a vendor's catalog.
func (s *Service) catalogCredential(explicitID, vendorID string) (Credential, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if explicitID != "" {
		c, ok := s.creds[explicitID]
		if !ok {
			return c, fail(404, "credential not found")
		}
		if c.Vendor != vendorID {
			return c, fail(400, "该凭据不属于所选厂商")
		}
		return c, nil
	}
	candidates := make([]Credential, 0, len(s.creds))
	for _, c := range s.creds {
		if !s.revoked[c.ID] && c.Vendor == vendorID && c.APIKey != "" && !c.Disabled {
			candidates = append(candidates, c)
		}
	}
	if len(candidates) == 0 {
		return Credential{}, fail(400, "请先为该厂商添加一个可用凭据")
	}
	sort.Slice(candidates, func(i, j int) bool {
		left, right := priorityValue(candidates[i]), priorityValue(candidates[j])
		if left != right {
			return left > right
		}
		return candidates[i].ID < candidates[j].ID
	})
	return candidates[0], nil
}
