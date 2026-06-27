// MINST-05: Vulos-provisioned cloud instance — Fly Machine launch from OS dashboard.
//
// Provisioner allows the OS to request the Vulos cloud control plane to spin up
// a new Fly Machine, enroll it under the current account, and register it in
// the local instance Registry.
//
// Note: the OS never talks to the Fly API directly. It forwards provision
// requests to the Vulos cloud control plane (api.vulos.org), which owns the
// FLY_API_TOKEN and performs the actual POST /v1/apps/{app}/machines call
// against https://api.machines.dev. This keeps the cloud-provider credential
// out of the OS and avoids any Go-module dependency from vulos OS on
// vulos-cloud.
//
// Flow:
//
//  1. Dashboard calls POST /api/instances/provision (handled here).
//  2. Provisioner POSTs to https://api.vulos.org/api/instances/provision with
//     the device credential and the requested region + plan.
//  3. Cloud returns a provision request ID and the new instance ULID.
//  4. Provisioner stores a provision_requests row and upserts the new instance
//     (status=unknown) into the Registry so it is immediately available for
//     routing.
//  5. Dashboard polls GET /api/instances/:ulid/status until status == "ready".
//     The provisioner's status endpoint queries the cloud and updates the local
//     record.
//
// Cloud calls degrade gracefully: if the control plane is unreachable the
// provision endpoint returns 503 with a descriptive error; the registry is
// never left in an inconsistent state.
//
// Wiring: call RegisterProvisionHandlers(mux, provisioner) from a routes_*.go
// file in cmd/server — do NOT edit main.go.
//
// Override VULOS_CLOUD_URL for dev/self-hosted deployments.
package multiinstance

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"vulos/backend/internal/cpbilling"
)

// ProvisionRequest describes one pending or completed Fly Machine provisioning job.
type ProvisionRequest struct {
	ID           string    `json:"id"`
	Region       string    `json:"region"`
	Plan         string    `json:"plan"`
	Status       string    `json:"status"` // pending|provisioning|ready|failed
	InstanceULID string    `json:"instance_ulid,omitempty"`
	EndpointURL  string    `json:"endpoint_url,omitempty"`
	ErrorMessage string    `json:"error_message,omitempty"`
	RequestedAt  time.Time `json:"requested_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// cloudProvisionResponse is the wire type returned by the Vulos cloud
// POST /api/instances/provision endpoint.
type cloudProvisionResponse struct {
	RequestID    string `json:"request_id"`
	InstanceULID string `json:"instance_ulid"`
	Status       string `json:"status"`
	EndpointURL  string `json:"endpoint_url,omitempty"`
	Error        string `json:"error,omitempty"`
}

// cloudStatusResponse is the wire type returned by the cloud
// GET /api/instances/:ulid/status endpoint.
type cloudStatusResponse struct {
	InstanceULID string `json:"instance_ulid"`
	Status       string `json:"status"` // provisioning|ready|failed
	EndpointURL  string `json:"endpoint_url,omitempty"`
	Error        string `json:"error,omitempty"`
}

// Provisioner manages Fly Machine cloud instance provisioning requests.
// It is safe for concurrent use.
type Provisioner struct {
	reg         *Registry
	httpClient  *http.Client
	deviceToken string

	// billing GATES (entitlement + suspension) and METERS compute provisioning
	// against the cp control plane. Nil or disabled (CP_URL unset) = standalone
	// OS: provisioning is ungated/unmetered exactly as before.
	billing *cpbilling.Client
}

// NewProvisioner creates a Provisioner backed by reg.
// deviceToken is the OS device credential forwarded to the cloud control plane.
func NewProvisioner(reg *Registry, deviceToken string) *Provisioner {
	return &Provisioner{
		reg:         reg,
		deviceToken: deviceToken,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// WithBilling wires a cp billing client so compute provisioning is gated on
// compute_enabled, suspended, and compute_box_cap (enforced locally against
// Registry.List()) and metered. A nil/disabled client is a no-op. Returns p
// for chaining.
//
// ENFORCEMENT STATUS:
//   - compute_enabled + suspended: ENFORCED (cp authoritative)
//   - compute_box_cap: ENFORCED locally (Registry.List() count before provisioning)
//   - compute_storage_gb: NOT enforced by OS — storage quotas live in the cloud
//     control plane (it owns the Fly API token and enforces disk limits).
func (p *Provisioner) WithBilling(b *cpbilling.Client) *Provisioner {
	p.billing = b
	return p
}

// Provision requests the cloud control plane to create a new Fly Machine
// in the given region with the given plan.  It stores the provision request
// locally and upserts the new instance into the Registry immediately so it is
// visible to routing — even before the machine is fully ready.
//
// If the cloud is unreachable or returns an error, Provision returns a wrapped
// error.  The local Registry is not modified in that case.
func (p *Provisioner) Provision(ctx context.Context, account, region, plan string) (*ProvisionRequest, error) {
	if region == "" {
		return nil, fmt.Errorf("provision: region must not be empty")
	}
	if plan == "" {
		return nil, fmt.Errorf("provision: plan must not be empty")
	}

	// BILLING GATE (surface 2: compute). Enforces compute_enabled, suspended,
	// and compute_box_cap. The active box count is read from the local Registry
	// (authoritative for this OS instance) before calling the cloud endpoint.
	// Fail-open + degraded on a cold cp outage. No-op when billing is disabled
	// (standalone OS).
	//
	// NOTE: compute_storage_gb is not enforced here — storage quota enforcement
	// lives in the cloud control plane (it owns the Fly API token).
	if p.billing.Enabled() {
		var activeBoxes int
		if instances, err := p.reg.List(); err == nil {
			activeBoxes = len(instances)
		}
		if d := p.billing.GateCompute(ctx, account, activeBoxes); !d.Allowed {
			return nil, fmt.Errorf("provision: refused: %s", d.Reason)
		}
	}

	cloudResp, err := p.callProvisionEndpoint(ctx, region, plan)
	if err != nil {
		return nil, fmt.Errorf("provision: cloud call failed: %w", err)
	}

	now := time.Now().UTC()
	req := &ProvisionRequest{
		ID:           cloudResp.RequestID,
		Region:       region,
		Plan:         plan,
		Status:       cloudResp.Status,
		InstanceULID: cloudResp.InstanceULID,
		EndpointURL:  cloudResp.EndpointURL,
		ErrorMessage: cloudResp.Error,
		RequestedAt:  now,
		UpdatedAt:    now,
	}
	if req.Status == "" {
		req.Status = "pending"
	}

	// Persist to provision_requests table.
	if err := p.saveRequest(req); err != nil {
		log.Printf("[provisioner] saveRequest: %v", err)
		// Non-fatal: the cloud call succeeded; keep going.
	}

	// Immediately upsert the new instance into the Registry so routing/dashboard
	// can display it (status=unknown until PollStatus confirms ready).
	if cloudResp.InstanceULID != "" {
		inst := Instance{
			ULID:        cloudResp.InstanceULID,
			DisplayName: fmt.Sprintf("Cloud (%s/%s)", region, plan),
			Kind:        KindCloud,
			Role:        RolePeer,
			Status:      StatusUnknown,
			EndpointURL: cloudResp.EndpointURL,
		}
		if uErr := p.reg.Upsert(inst); uErr != nil {
			log.Printf("[provisioner] upsert new instance %s: %v", cloudResp.InstanceULID, uErr)
		}
	}

	// METER (surface 3: compute). One billable machine provisioned. No-op when
	// billing is disabled.
	p.billing.MeterAsync(cpbilling.UsageEvent{
		Product:   cpbilling.ProductCompute,
		AccountID: account,
		Kind:      cpbilling.KindComputeMachine,
		Count:     1,
	})

	return req, nil
}

// PollStatus queries the cloud for the current provisioning status of
// instanceULID and updates the local Registry and provision_requests row.
// Returns the current status string ("provisioning", "ready", "failed").
//
// If the cloud is unreachable, PollStatus returns ("", err) and leaves the
// local state unchanged (graceful degradation).
func (p *Provisioner) PollStatus(ctx context.Context, instanceULID string) (string, error) {
	if instanceULID == "" {
		return "", fmt.Errorf("pollstatus: instanceULID must not be empty")
	}

	cloudResp, err := p.callStatusEndpoint(ctx, instanceULID)
	if err != nil {
		return "", fmt.Errorf("pollstatus: cloud call failed: %w", err)
	}

	// Update Registry status.
	inst, ok := p.reg.Get(instanceULID)
	if ok {
		switch cloudResp.Status {
		case "ready":
			inst.Status = StatusOnline
		case "failed":
			inst.Status = StatusOffline
		default:
			inst.Status = StatusUnknown
		}
		if cloudResp.EndpointURL != "" {
			inst.EndpointURL = cloudResp.EndpointURL
		}
		if uErr := p.reg.Upsert(inst); uErr != nil {
			log.Printf("[provisioner] update instance status %s: %v", instanceULID, uErr)
		}
		if cloudResp.Status == "ready" {
			if msErr := p.reg.MarkSeen(instanceULID); msErr != nil {
				log.Printf("[provisioner] MarkSeen %s: %v", instanceULID, msErr)
			}
		}
	}

	// Update provision_requests row.
	if prErr := p.updateRequestStatus(instanceULID, cloudResp.Status, cloudResp.EndpointURL, cloudResp.Error); prErr != nil {
		log.Printf("[provisioner] updateRequestStatus %s: %v", instanceULID, prErr)
	}

	return cloudResp.Status, nil
}

// callProvisionEndpoint POSTs to the cloud provision endpoint and returns the
// decoded response.
func (p *Provisioner) callProvisionEndpoint(ctx context.Context, region, plan string) (*cloudProvisionResponse, error) {
	base, err := requireSecureCloudBase()
	if err != nil {
		return nil, err
	}
	url := base + "/api/instances/provision"

	body, err := json.Marshal(map[string]string{
		"region": region,
		"plan":   plan,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.deviceToken != "" {
		req.Header.Set("Authorization", "VulosDevice "+p.deviceToken)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("POST %s: status %d: %s", url, resp.StatusCode, b)
	}

	var cloudResp cloudProvisionResponse
	if err := json.NewDecoder(resp.Body).Decode(&cloudResp); err != nil {
		return nil, fmt.Errorf("decode provision response: %w", err)
	}
	return &cloudResp, nil
}

// callStatusEndpoint GETs the cloud status for instanceULID.
func (p *Provisioner) callStatusEndpoint(ctx context.Context, instanceULID string) (*cloudStatusResponse, error) {
	base, err := requireSecureCloudBase()
	if err != nil {
		return nil, err
	}
	url := base + "/api/instances/" + instanceULID + "/status"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if p.deviceToken != "" {
		req.Header.Set("Authorization", "VulosDevice "+p.deviceToken)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("GET %s: status %d: %s", url, resp.StatusCode, b)
	}

	var cloudResp cloudStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&cloudResp); err != nil {
		return nil, fmt.Errorf("decode status response: %w", err)
	}
	return &cloudResp, nil
}

// saveRequest persists a ProvisionRequest to the provision_requests table.
func (p *Provisioner) saveRequest(req *ProvisionRequest) error {
	_, err := p.reg.db.Exec(`
		INSERT INTO provision_requests
			(id, region, plan, status, instance_ulid, endpoint_url, error_message, requested_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			status        = excluded.status,
			instance_ulid = excluded.instance_ulid,
			endpoint_url  = excluded.endpoint_url,
			error_message = excluded.error_message,
			updated_at    = excluded.updated_at`,
		req.ID,
		req.Region,
		req.Plan,
		req.Status,
		req.InstanceULID,
		req.EndpointURL,
		req.ErrorMessage,
		formatTime(req.RequestedAt),
		formatTime(req.UpdatedAt),
	)
	return err
}

// updateRequestStatus updates the status, endpoint_url, and error_message of
// the provision_requests row identified by instanceULID.
func (p *Provisioner) updateRequestStatus(instanceULID, status, endpointURL, errMsg string) error {
	_, err := p.reg.db.Exec(`
		UPDATE provision_requests
		SET status = ?, endpoint_url = ?, error_message = ?, updated_at = ?
		WHERE instance_ulid = ?`,
		status, endpointURL, errMsg,
		formatTime(time.Now().UTC()),
		instanceULID,
	)
	return err
}

// GetProvisionRequest returns the ProvisionRequest for the given request ID, or
// (nil, false) if not found.
func (p *Provisioner) GetProvisionRequest(id string) (*ProvisionRequest, bool) {
	row := p.reg.db.QueryRow(`
		SELECT id, region, plan, status, instance_ulid, endpoint_url, error_message, requested_at, updated_at
		FROM provision_requests WHERE id = ?`, id)

	var req ProvisionRequest
	var requestedAt, updatedAt string
	err := row.Scan(
		&req.ID, &req.Region, &req.Plan, &req.Status,
		&req.InstanceULID, &req.EndpointURL, &req.ErrorMessage,
		&requestedAt, &updatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		log.Printf("[provisioner] GetProvisionRequest %s: %v", id, err)
		return nil, false
	}
	if t, err := time.Parse(time.RFC3339Nano, requestedAt); err == nil {
		req.RequestedAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil {
		req.UpdatedAt = t
	}
	return &req, true
}

// RegisterProvisionHandlers wires the provisioning endpoints into mux.
//
// Routes registered:
//
//	POST /api/instances/provision       — request a new Fly Machine
//	GET  /api/instances/{ulid}/status   — poll provisioning status
//
// Usage (from a routes_*.go in cmd/server — never from main.go):
//
//	multiinstance.RegisterProvisionHandlers(mux, provisioner)
func RegisterProvisionHandlers(mux *http.ServeMux, p *Provisioner) {
	// POST /api/instances/provision
	mux.HandleFunc("POST /api/instances/provision", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Region string `json:"region"`
			Plan   string `json:"plan"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
			return
		}
		if body.Region == "" || body.Plan == "" {
			http.Error(w, `{"error":"region and plan are required"}`, http.StatusUnprocessableEntity)
			return
		}

		account := r.Header.Get("X-User-ID")
		req, err := p.Provision(r.Context(), account, body.Region, body.Plan)
		if err != nil {
			log.Printf("[provisioner] Provision %s/%s: %v", body.Region, body.Plan, err)
			w.Header().Set("Content-Type", "application/json")
			// A billing refusal (suspended / not entitled) is the caller's fault:
			// surface it as 402 so the dashboard can show a billing prompt. Cloud
			// outages stay 503.
			status := http.StatusServiceUnavailable
			if strings.HasPrefix(err.Error(), "provision: refused:") {
				status = http.StatusPaymentRequired
			}
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(req)
	})

	// GET /api/instances/{ulid}/status
	// Note: Go 1.22+ ServeMux supports path parameters via {name} syntax.
	mux.HandleFunc("GET /api/instances/{ulid}/status", func(w http.ResponseWriter, r *http.Request) {
		ulid := r.PathValue("ulid")
		if ulid == "" {
			http.Error(w, `{"error":"ulid required"}`, http.StatusBadRequest)
			return
		}

		status, err := p.PollStatus(r.Context(), ulid)
		if err != nil {
			log.Printf("[provisioner] PollStatus %s: %v", ulid, err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		// Return the updated instance from the registry.
		inst, ok := p.reg.Get(ulid)
		w.Header().Set("Content-Type", "application/json")
		if ok {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"instance_ulid": ulid,
				"status":        status,
				"instance":      inst,
			})
		} else {
			_ = json.NewEncoder(w).Encode(map[string]string{
				"instance_ulid": ulid,
				"status":        status,
			})
		}
	})
}
