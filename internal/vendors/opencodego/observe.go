package opencodego

import (
	"encoding/json"

	"github.com/xiao-qiu-qiu/OCcodeBridge/internal/vendors"
)

// ObserveResponse reads the allowance Go reports for one request: how much of the
// subscription that request counted. The docs name "opencode.allowance"; the
// frame position and unit are unverified (spec §5), so a number, an object, and a
// few likely nesting places are all accepted. A frame without an allowance keeps
// the log unchanged.
func (Vendor) ObserveResponse(frame map[string]any) *vendors.Observation {
	allowance, ok := findAllowance(frame)
	if !ok {
		return nil
	}
	observation := &vendors.Observation{Allowance: allowance}
	if raw, err := json.Marshal(allowance); err == nil {
		observation.Raw = raw
	}
	return observation
}

// findAllowance looks for the allowance fragment: "opencode.allowance" in the
// documented place, plus the usage envelope and a bare number an SDK-shaped frame
// may carry instead.
func findAllowance(frame map[string]any) (map[string]any, bool) {
	if value, present := frame["opencode"]; present {
		if opencode := object(value); opencode != nil {
			// The documented place: a collector object carrying the allowance.
			return allowanceObject(opencode["allowance"])
		}
		return allowanceObject(value)
	}
	if usage := object(frame["usage"]); usage != nil {
		if opencode := object(usage["opencode"]); opencode != nil {
			if allowance, ok := allowanceObject(opencode["allowance"]); ok {
				return allowance, true
			}
			return allowanceObject(opencode)
		}
		if allowance, ok := allowanceObject(usage["opencode"]); ok {
			return allowance, true
		}
		return allowanceObject(usage["allowance"])
	}
	return nil, false
}

// allowanceObject normalises the reported allowance to an object: a bare number
// becomes {"value": n}, as the Observation type documents.
func allowanceObject(value any) (map[string]any, bool) {
	if m := object(value); m != nil {
		if len(m) == 0 {
			return nil, false
		}
		return m, true
	}
	if n, ok := toFloat(value); ok {
		return map[string]any{"value": n}, true
	}
	return nil, false
}
