package bridge

import (
	"encoding/json"
	"math"
	"net/url"
	"time"

	"github.com/xiao-qiu-qiu/PlanBridge/internal/vendors"
)

// These fields follow CPA's JSON ABI, independently of the plugin console's usage DTO.
type quotaResponse struct {
	Subscription *quotaSubscription `json:"subscription,omitempty"`
	Groups       []quotaGroup       `json:"groups"`
}

type quotaSubscription struct {
	Plan string `json:"plan"`
}

type quotaGroup struct {
	DisplayName string        `json:"displayName"`
	Buckets     []quotaBucket `json:"buckets"`
}

type quotaBucket struct {
	Window            string  `json:"window"`
	RemainingFraction float64 `json:"remainingFraction"`
	ResetTime         string  `json:"resetTime,omitempty"`
}

func (s *Service) fetchQuota(raw json.RawMessage) (quotaResponse, error) {
	var req struct {
		AuthID         string `json:"auth_id"`
		Provider       string `json:"provider"`
		HostCallbackID string `json:"host_callback_id"`
	}
	if json.Unmarshal(raw, &req) != nil || req.Provider != Provider || req.AuthID == "" {
		return quotaResponse{}, fail(400, "PlanBridge quota requires a matching credential")
	}
	// CPA resolves auth_index to its routing identity. Never fall back to the first
	// account, and allow manually disabled or quota-paused accounts to query usage.
	credentialID := ""
	s.mu.RLock()
	for id, c := range s.creds {
		if !s.revoked[id] && routingID(c, s.authFiles[id]) == req.AuthID {
			credentialID = id
			break
		}
	}
	s.mu.RUnlock()
	if credentialID == "" {
		return quotaResponse{}, fail(404, "PlanBridge quota credential is no longer available")
	}
	value, err := s.credentialUsage(ManagementRequest{
		Query: url.Values{"id": {credentialID}, "refresh": {"1"}}, HostCallbackID: req.HostCallbackID,
	})
	if err != nil {
		return quotaResponse{}, err
	}
	if value.Status != "ok" {
		status := 502
		switch value.Status {
		case "unauthorized":
			status = 401
		case "plan_unsupported":
			status = 403
		case "rate_limited":
			status = 429
		case "timeout":
			status = 504
		}
		// The console can label stale values; CPA's normalized response cannot.
		return quotaResponse{}, fail(status, "PlanBridge quota query failed: "+value.Status)
	}
	displayName := value.Vendor
	if v, known := vendors.Get(value.Vendor); known {
		displayName = v.DisplayName()
	}
	group := quotaGroup{DisplayName: displayName, Buckets: []quotaBucket{}}
	for _, limit := range value.Limits {
		used, ok := quotaUsedFraction(limit)
		if !ok {
			continue
		}
		bucket := quotaBucket{Window: quotaWindowName(limit.Type), RemainingFraction: 1 - used}
		if limit.ResetsAt != nil {
			bucket.ResetTime = limit.ResetsAt.Format(time.RFC3339Nano)
		}
		group.Buckets = append(group.Buckets, bucket)
	}
	if len(group.Buckets) == 0 {
		return quotaResponse{}, fail(502, "PlanBridge returned no usable quota windows")
	}
	response := quotaResponse{Groups: []quotaGroup{group}}
	// The plan name is the vendor's own label from the usage sample.
	if value.Plan != nil && value.Plan.Name != "" {
		response.Subscription = &quotaSubscription{Plan: value.Plan.Name}
	}
	return response, nil
}

// quotaUsedFraction prefers the exact money pair a vendor reported (used/cap)
// over its percentage, and caps the result at a fully used window. It reports
// false when neither source is usable, so no bucket claims a zero usage.
func quotaUsedFraction(limit usageLimit) (float64, bool) {
	finite := func(value *float64) bool {
		return value != nil && !math.IsNaN(*value) && !math.IsInf(*value, 0) && *value >= 0
	}
	if finite(limit.Used) && finite(limit.Cap) && *limit.Cap > 0 {
		return math.Min(*limit.Used / *limit.Cap, 1), true
	}
	if finite(limit.PercentUsed) {
		return math.Min(*limit.PercentUsed/100, 1), true
	}
	return 0, false
}

// quotaWindowName maps the internal window type onto the name CPA's quota
// contract accepts. "5h" and "7d" are the documented short windows; a day or a
// calendar month has no dedicated value, so the contract's own spellings "daily"
// and "monthly" are used rather than squeezing them into a fixed-length window.
func quotaWindowName(kind string) string {
	switch kind {
	case "five_hour":
		return "5h"
	case "weekly":
		return "7d"
	case "daily":
		return "daily"
	case "monthly":
		return "monthly"
	}
	return kind
}
