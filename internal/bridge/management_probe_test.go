package bridge

import (
	"bytes"
	"encoding/json"
	"testing"
)

// An explicitly selected management credential may be manually disabled or quota
// paused; normal traffic must still refuse it.
func TestManagementProbeExplicitCredentialEligibility(t *testing.T) {
	for _, tc := range []struct {
		name     string
		disabled bool
		paused   bool
	}{
		{"manual-disabled", true, false},
		{"quota-paused", false, true},
		{"manual-disabled-and-quota-paused", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := probeService(t)
			t.Cleanup(func() { observabilityShutdown(t, s) })
			c := s.creds["probe-account"]
			c.Disabled = tc.disabled
			if tc.paused {
				c.QuotaBlock = &QuotaBlock{KeyHash: quotaKeyHash(c), Limits: []QuotaBlockLimit{{Type: "weekly"}}}
			}
			stored := quotaTestAuth(t, s, c)
			before := jsonBytes(s.creds[c.ID])
			h := newFakeHost(ssePlan(simpleSSE()))
			s.SetHost(h.call)
			result, err := s.Handle("management.handle", jsonBytes(ManagementRequest{
				Method: "POST", Path: apiBase + "/models/test", HostCallbackID: "probe-callback",
				Body: jsonBytes(map[string]any{"model": "deepseek-flash", "upstream_id": "opencode-go/deepseek-v4.1-flash", "credential_id": c.ID}),
			}))
			if err != nil {
				t.Fatal(err)
			}
			response := result.(ManagementResponse)
			var body struct {
				OK           bool   `json:"ok"`
				Status       int    `json:"status"`
				CredentialID string `json:"credential_id"`
			}
			if err := json.Unmarshal(response.Body, &body); err != nil {
				t.Fatal(err)
			}
			if tc.paused {
				status := response.StatusCode
				if status == 200 {
					status = body.Status
				}
				if body.OK || status != 400 || len(h.opened) != 0 {
					t.Fatalf("quota-paused probe admitted: response=%s, upstream calls=%d", response.Body, len(h.opened))
				}
			} else if response.StatusCode != 200 || !body.OK || body.Status != 200 || body.CredentialID != c.ID || len(h.opened) != 1 {
				t.Fatalf("explicit disabled probe failed: response=%s, upstream calls=%d", response.Body, len(h.opened))
			}

			// Normal traffic must still reject the same live credential, even
			// with stale enabled storage and management egress metadata.
			stale := c
			stale.Disabled, stale.QuotaBlock = false, nil
			opened := len(h.opened)
			wantStatus := 401
			if !tc.disabled {
				wantStatus = 429
			}
			for _, method := range []string{"executor.execute", "executor.execute_stream"} {
				_, err := s.Handle(method, jsonBytes(ExecutorRequest{
					AuthID: c.ID, Model: "deepseek-flash", StreamID: "client",
					StorageJSON: jsonBytes(stale), Payload: []byte(`{"messages":[{"role":"user","content":"ok"}]}`),
					Metadata: map[string]any{"egress_management": true},
				}))
				if statusOf(err) != wantStatus || len(h.opened) != opened {
					t.Fatalf("%s admitted credential: error=%v, upstream calls=%d", method, err, len(h.opened))
				}
			}
			if !bytes.Equal(before, jsonBytes(s.creds[c.ID])) || !bytes.Equal(stored, quotaTestRead(t, s, c.ID)) {
				t.Fatal("probe or executor changed credential state or persisted manual disablement")
			}
		})
	}
}

// A credential without a vendor cannot be scheduled, so an explicit probe must
// say so instead of picking an upstream.
func TestManagementProbeRequiresCredentialVendor(t *testing.T) {
	s := probeService(t)
	t.Cleanup(func() { observabilityShutdown(t, s) })
	c := s.creds["probe-account"]
	c.Vendor = ""
	s.creds[c.ID] = c
	h := newFakeHost()
	s.SetHost(h.call)
	result, err := s.Handle("management.handle", jsonBytes(ManagementRequest{
		Method: "POST", Path: apiBase + "/models/test", HostCallbackID: "probe-callback",
		Body: jsonBytes(map[string]any{"model": "deepseek-flash", "upstream_id": "opencode-go/deepseek-v4.1-flash", "credential_id": c.ID}),
	}))
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(result.(ManagementResponse).Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.OK || body.Error == "" || len(h.opened) != 0 {
		t.Fatalf("vendor-less probe admitted: %s", result.(ManagementResponse).Body)
	}
}
