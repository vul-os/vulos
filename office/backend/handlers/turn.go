package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// TURNHandler issues short-lived ICE server lists for the Vulos Spaces call layer
// (OFFICE-63). Mirrors the path the OS fabric uses for TURN cred minting:
//
//   GET /api/turn/credentials → { iceServers: [...] }
//
// Behavior:
//   * If env VULOS_TURN_URLS is set (comma-separated, e.g.
//     "turn:turn.vulos.org:3478,turns:turn.vulos.org:5349") and
//     VULOS_TURN_SECRET is set, returns coturn-style time-limited credentials
//     (username = "<expiry-unix>:<userId>", password = base64(HMAC-SHA256(secret, username))).
//   * Otherwise returns a public-STUN-only configuration suitable for
//     same-NAT / dev use; the client treats this as "no TURN" and will
//     surface direct-only connectivity.
//
// This keeps the office build self-contained while OFFICE-20 (fabric client)
// lands in parallel; once OFFICE-20 ships, the cloud relay will replace the
// env-driven config without changing the wire shape.

type TURNHandler struct{}

func NewTURNHandler() *TURNHandler { return &TURNHandler{} }

type iceServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

type turnResponse struct {
	IceServers []iceServer `json:"iceServers"`
	TTLSeconds int         `json:"ttlSeconds"`
}

func (h *TURNHandler) Credentials(c *gin.Context) {
	servers := []iceServer{
		{URLs: []string{"stun:stun.l.google.com:19302"}},
	}
	ttl := 600

	turnURLs := strings.TrimSpace(os.Getenv("VULOS_TURN_URLS"))
	secret := strings.TrimSpace(os.Getenv("VULOS_TURN_SECRET"))
	if turnURLs != "" && secret != "" {
		urls := splitCSV(turnURLs)
		expiry := time.Now().Add(time.Duration(ttl) * time.Second).Unix()
		userID := c.GetString("userID")
		if userID == "" {
			userID = c.ClientIP()
		}
		username := fmt.Sprintf("%d:%s", expiry, userID)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(username))
		cred := base64.StdEncoding.EncodeToString(mac.Sum(nil))
		servers = append(servers, iceServer{
			URLs:       urls,
			Username:   username,
			Credential: cred,
		})
	}

	c.JSON(http.StatusOK, turnResponse{IceServers: servers, TTLSeconds: ttl})
}

func splitCSV(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
