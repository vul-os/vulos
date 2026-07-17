package relayscale

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// kubernetes.go — KubernetesProvisioner: a REAL, lightweight relay provisioner
// that scales a relay workload's replicas through the Kubernetes REST API. It
// deliberately avoids the heavy client-go dependency — it speaks the apiserver
// over net/http with an in-cluster service-account token (the same credential a
// pod already has), so the OSS control-plane binary stays small and CGO-free.
//
// MODEL. In Kubernetes the workload's replica count IS the desired relay count,
// and the scheduler + ReplicaSet/StatefulSet controller own placement. So:
//
//   - Provision → read the workload's /scale, set replicas = current + 1.
//   - Destroy   → set replicas = current - 1 (never below 0); the controller
//     terminates a pod. For a StatefulSet the highest-ordinal pod is removed
//     deterministically; for a Deployment the controller chooses.
//   - List      → enumerate the workload's pods by label selector, each a relay
//     Instance (Ready = the pod's Ready condition).
//
// This is single-region: one KubernetesProvisioner is bound to ONE cluster/region
// (a cluster is a region). Multi-region Kubernetes runs one relay workload per
// regional cluster and is driven either by the managed multi-provider (cloud) or
// by "external" mode with a per-cluster HPA reading the demand API — see docs.
//
// ALTERNATIVE (documented, not code): instead of the CP holding a token and
// pushing replicas, publish the per-region desired count on the demand API and
// let a Kubernetes HPA scale on it as a custom/external metric. That inverts the
// control (the cluster pulls) and needs no CP→apiserver credential. Pick this
// when you do not want the control plane to hold cluster-write access — set
// RELAY_PROVISIONER=external. The Deployment scaling here is for operators who DO
// want the CP to actuate directly.

const (
	k8sTokenPath  = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	k8sCAPath     = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	k8sNSPath     = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
	k8sHTTPClient = 15 * time.Second
)

// KubernetesConfig configures the KubernetesProvisioner. Empty fields fall back to
// the in-cluster service-account defaults where sensible.
type KubernetesConfig struct {
	// APIServer is the apiserver base URL (e.g. "https://10.0.0.1:6443"). Empty =
	// derive "https://$KUBERNETES_SERVICE_HOST:$KUBERNETES_SERVICE_PORT".
	APIServer string
	// Token is the bearer token. Empty = read the in-cluster SA token file.
	Token string
	// CACertPEM is the apiserver CA bundle. Empty = read the in-cluster CA file.
	CACertPEM []byte
	// InsecureSkipVerify disables TLS verification (test clusters only).
	InsecureSkipVerify bool
	// Namespace of the relay workload. Empty = read the in-cluster namespace file.
	Namespace string
	// Workload is the workload name (e.g. "vulos-relayd").
	Workload string
	// WorkloadKind is "deployments" (default) or "statefulsets".
	WorkloadKind string
	// LabelSelector enumerates the workload's pods (e.g. "app=vulos-relayd").
	LabelSelector string
	// Region is the single region this cluster serves.
	Region string
}

// KubernetesProvisioner scales a relay workload via the Kubernetes REST API.
type KubernetesProvisioner struct {
	cfg  KubernetesConfig
	http *http.Client
	base string
	tok  string
}

// NewKubernetesProvisioner builds the provisioner, resolving in-cluster defaults.
// It returns ErrNotConfigured when required pieces (apiserver, token, workload,
// region) cannot be resolved — fail closed so a half-configured cluster never
// silently no-ops.
func NewKubernetesProvisioner(cfg KubernetesConfig) (*KubernetesProvisioner, error) {
	if cfg.WorkloadKind == "" {
		cfg.WorkloadKind = "deployments"
	}
	if cfg.WorkloadKind != "deployments" && cfg.WorkloadKind != "statefulsets" {
		return nil, fmt.Errorf("%w: WorkloadKind must be deployments|statefulsets", ErrNotConfigured)
	}
	base := cfg.APIServer
	if base == "" {
		host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
		if host != "" && port != "" {
			base = "https://" + host + ":" + port
		}
	}
	tok := cfg.Token
	if tok == "" {
		if b, err := os.ReadFile(k8sTokenPath); err == nil {
			tok = strings.TrimSpace(string(b))
		}
	}
	if cfg.Namespace == "" {
		if b, err := os.ReadFile(k8sNSPath); err == nil {
			cfg.Namespace = strings.TrimSpace(string(b))
		}
	}
	if base == "" || tok == "" || cfg.Workload == "" || cfg.Namespace == "" || cfg.Region == "" {
		return nil, fmt.Errorf("%w: need apiserver+token+namespace+workload+region", ErrNotConfigured)
	}

	caPEM := cfg.CACertPEM
	if len(caPEM) == 0 && !cfg.InsecureSkipVerify {
		caPEM, _ = os.ReadFile(k8sCAPath)
	}
	tlsCfg := &tls.Config{InsecureSkipVerify: cfg.InsecureSkipVerify} //nolint:gosec // opt-in for test clusters
	if len(caPEM) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("%w: invalid CA cert PEM", ErrNotConfigured)
		}
		tlsCfg.RootCAs = pool
	}
	return &KubernetesProvisioner{
		cfg:  cfg,
		base: strings.TrimRight(base, "/"),
		tok:  tok,
		http: &http.Client{Timeout: k8sHTTPClient, Transport: &http.Transport{TLSClientConfig: tlsCfg}},
	}, nil
}

func (k *KubernetesProvisioner) Name() string  { return "kubernetes" }
func (k *KubernetesProvisioner) Enabled() bool { return true }

// scaleURL is the workload's scale subresource.
func (k *KubernetesProvisioner) scaleURL() string {
	return fmt.Sprintf("%s/apis/apps/v1/namespaces/%s/%s/%s/scale",
		k.base, k.cfg.Namespace, k.cfg.WorkloadKind, k.cfg.Workload)
}

type k8sScale struct {
	Spec struct {
		Replicas int `json:"replicas"`
	} `json:"spec"`
}

func (k *KubernetesProvisioner) getReplicas(ctx context.Context) (int, error) {
	var s k8sScale
	if err := k.do(ctx, http.MethodGet, k.scaleURL(), "", nil, &s); err != nil {
		return 0, err
	}
	return s.Spec.Replicas, nil
}

// setReplicas patches the workload's replica count via the scale subresource
// using a merge patch (no client-go, no strategic-merge complexity).
func (k *KubernetesProvisioner) setReplicas(ctx context.Context, n int) error {
	if n < 0 {
		n = 0
	}
	body := fmt.Sprintf(`{"spec":{"replicas":%d}}`, n)
	return k.do(ctx, http.MethodPatch, k.scaleURL(), "application/merge-patch+json", []byte(body), nil)
}

// Provision scales the workload up by one. region must match the cluster's region.
func (k *KubernetesProvisioner) Provision(ctx context.Context, region string, _ RelaySpec) (Instance, error) {
	if region != "" && region != k.cfg.Region {
		return Instance{}, fmt.Errorf("relayscale: kubernetes provisioner serves region %q, not %q", k.cfg.Region, region)
	}
	cur, err := k.getReplicas(ctx)
	if err != nil {
		return Instance{}, err
	}
	if err := k.setReplicas(ctx, cur+1); err != nil {
		return Instance{}, err
	}
	// The pod name is not known until scheduled; List surfaces it once it appears.
	return Instance{
		ID:        fmt.Sprintf("%s-replica-%d", k.cfg.Workload, cur), // 0-based new replica index
		Region:    k.cfg.Region,
		Provider:  "kubernetes",
		Ready:     false,
		CreatedAt: time.Now().UTC(),
		Meta:      map[string]string{"workload": k.cfg.Workload, "namespace": k.cfg.Namespace},
	}, nil
}

// Destroy scales the workload down by one. The instance identity is advisory:
// under a Deployment the controller chooses which pod terminates; under a
// StatefulSet the highest-ordinal pod goes.
func (k *KubernetesProvisioner) Destroy(ctx context.Context, _ Instance) error {
	cur, err := k.getReplicas(ctx)
	if err != nil {
		return err
	}
	if cur <= 0 {
		return nil
	}
	return k.setReplicas(ctx, cur-1)
}

type k8sPodList struct {
	Items []struct {
		Metadata struct {
			Name              string    `json:"name"`
			CreationTimestamp time.Time `json:"creationTimestamp"`
		} `json:"metadata"`
		Status struct {
			PodIP      string `json:"podIP"`
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
		} `json:"status"`
	} `json:"items"`
}

// List enumerates the workload's pods as relay Instances.
func (k *KubernetesProvisioner) List(ctx context.Context) ([]Instance, error) {
	u := fmt.Sprintf("%s/api/v1/namespaces/%s/pods", k.base, k.cfg.Namespace)
	if sel := strings.TrimSpace(k.cfg.LabelSelector); sel != "" {
		u += "?labelSelector=" + strings.ReplaceAll(sel, " ", "")
	}
	var pl k8sPodList
	if err := k.do(ctx, http.MethodGet, u, "", nil, &pl); err != nil {
		return nil, err
	}
	out := make([]Instance, 0, len(pl.Items))
	for _, p := range pl.Items {
		ready := false
		for _, c := range p.Status.Conditions {
			if c.Type == "Ready" && c.Status == "True" {
				ready = true
			}
		}
		out = append(out, Instance{
			ID:        p.Metadata.Name,
			Region:    k.cfg.Region,
			Provider:  "kubernetes",
			Addr:      p.Status.PodIP,
			Ready:     ready,
			CreatedAt: p.Metadata.CreationTimestamp,
			Meta:      map[string]string{"workload": k.cfg.Workload, "namespace": k.cfg.Namespace},
		})
	}
	return out, nil
}

// do performs one authenticated apiserver request, decoding JSON into out when
// non-nil. A non-2xx status is an error carrying the response body.
func (k *KubernetesProvisioner) do(ctx context.Context, method, url, contentType string, body []byte, out any) error {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+k.tok)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := k.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("relayscale: kubernetes %s %s: %s: %s", method, url, resp.Status, strings.TrimSpace(string(rb)))
	}
	if out != nil {
		return json.Unmarshal(rb, out)
	}
	return nil
}

var _ RelayProvisioner = (*KubernetesProvisioner)(nil)
