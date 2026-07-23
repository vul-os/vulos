package cproutes

import (
	"context"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cloudhome"
)

// TestCloudhomePeerServe_SSRFBlocked is the CLOUDHOME-SSRF regression: the
// peer-share puller dials cap.OwnerAddr, which is fully attacker-controlled via
// the (effectively unauthenticated) POST /api/files/peer/inbound. Internal /
// reserved / metadata hosts and non-https schemes must be refused BEFORE any dial.
func TestCloudhomePeerServe_SSRFBlocked(t *testing.T) {
	h := &httpPeerServeClient{client: peerServeSSRFClient(2 * 1e9)}
	ctx := context.Background()
	req := cloudhome.PeerFetchRequest{RequesterID: "r", Timestamp: 1, Proof: "p"}

	blocked := []string{
		"http://169.254.169.254/latest/meta-data/", // AWS metadata + http scheme
		"https://169.254.169.254/",                 // link-local metadata
		"http://metadata.google.internal/",         // GCP metadata + http scheme
		"https://127.0.0.1/",                       // loopback
		"https://localhost/",                       // loopback name
		"https://10.0.0.5/",                        // RFC1918
		"https://192.168.1.10/",                    // RFC1918
		"https://172.16.9.9/",                      // RFC1918
		"https://100.64.0.1/",                      // CGNAT
		"http://internal-service:8080/",            // http scheme rejected
		"",                                         // empty
	}
	for _, addr := range blocked {
		rc, err := h.Serve(ctx, addr, req)
		if err == nil {
			if rc != nil {
				rc.Close()
			}
			t.Fatalf("Serve(%q): expected SSRF/scheme rejection, got nil error", addr)
		}
	}
}

// TestCloudhomeValidateExternalHost pins the deny-list directly.
func TestCloudhomeValidateExternalHost(t *testing.T) {
	blocked := []string{"127.0.0.1", "169.254.169.254", "10.1.2.3", "192.168.0.1",
		"172.31.255.1", "100.64.0.9", "::1", ""}
	for _, host := range blocked {
		if err := cloudhomeValidateExternalHost(host); err == nil {
			t.Errorf("validateExternalHost(%q): expected block, got nil", host)
		}
	}
	// A public IP literal must pass (no DNS dependency).
	if err := cloudhomeValidateExternalHost("93.184.216.34"); err != nil {
		t.Errorf("validateExternalHost(public IP): unexpected error %v", err)
	}
}
