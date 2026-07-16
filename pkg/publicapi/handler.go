// handler.go — HTTP handlers for the vulos.cloud public REST API v1.
//
// Routes: see package doc in store.go.
// Auth: Bearer token via Store.AuthenticateToken.
// Rate limit: 60 req/min per token via RateLimiter.
package publicapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Handler is the public API HTTP handler.
type Handler struct {
	store    *Store
	rl       *RateLimiter
	accts    AccountStore
	billing  BillingStore
	topup    PaystackInitializer
	mboxes   MailboxLister
	devices  DeviceLister
	webhooks WebhookManager
}

// NewHandler creates a Handler with all dependencies.
func NewHandler(
	store *Store,
	accts AccountStore,
	billing BillingStore,
	topup PaystackInitializer,
	mboxes MailboxLister,
	devices DeviceLister,
	wh WebhookManager,
) *Handler {
	return &Handler{
		store:    store,
		rl:       NewRateLimiter(),
		accts:    accts,
		billing:  billing,
		topup:    topup,
		mboxes:   mboxes,
		devices:  devices,
		webhooks: wh,
	}
}

// ServeHTTP routes /api/v1/ requests.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	accountID, tokenID, ok := h.authenticate(w, r)
	if !ok {
		return
	}

	// Rate limit: 60 req/min per token.
	if !h.rl.Allow(tokenID) {
		w.Header().Set("Retry-After", "60")
		writeAPIError(w, http.StatusTooManyRequests, "rate limit exceeded: 60 req/min")
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	path = strings.TrimSuffix(path, "/")

	switch {
	case path == "/account/me" && r.Method == http.MethodGet:
		h.getAccountMe(w, r, accountID)
	case path == "/account/usage" && r.Method == http.MethodGet:
		h.getAccountUsage(w, r, accountID)
	case path == "/billing/wallet" && r.Method == http.MethodGet:
		h.getWallet(w, r, accountID)
	case path == "/billing/wallet/topup" && r.Method == http.MethodPost:
		h.postTopup(w, r, accountID)
	case path == "/mailboxes" && r.Method == http.MethodGet:
		h.listMailboxes(w, r, accountID)
	case strings.HasPrefix(path, "/mailboxes/") && strings.HasSuffix(path, "/usage") && r.Method == http.MethodGet:
		mboxID := strings.TrimSuffix(strings.TrimPrefix(path, "/mailboxes/"), "/usage")
		h.getMailboxUsage(w, r, accountID, mboxID)
	case path == "/devices" && r.Method == http.MethodGet:
		h.listDevices(w, r, accountID)
	case path == "/webhooks" && r.Method == http.MethodGet:
		h.listWebhooks(w, r, accountID)
	case path == "/webhooks" && r.Method == http.MethodPost:
		h.createWebhook(w, r, accountID)
	case strings.HasPrefix(path, "/webhooks/") && r.Method == http.MethodDelete:
		whID := strings.TrimPrefix(path, "/webhooks/")
		h.deleteWebhook(w, r, accountID, whID)
	default:
		writeAPIError(w, http.StatusNotFound, "endpoint not found")
	}
}

// authenticate extracts and validates the Bearer token.
// Returns (accountID, tokenID, true) on success.
func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		writeAPIError(w, http.StatusUnauthorized, "missing or invalid Authorization header")
		return "", "", false
	}
	raw := strings.TrimPrefix(auth, "Bearer ")
	accountID, err := h.store.AuthenticateToken(r.Context(), raw)
	if err != nil {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized")
		return "", "", false
	}
	// tokenID is the SHA-256 hash of the raw token — used as the rate-limit key.
	tokenID := hashToken(raw)
	return accountID, tokenID, true
}

// ─── Handlers ─────────────────────────────────────────────────────────────────

func (h *Handler) getAccountMe(w http.ResponseWriter, r *http.Request, accountID string) {
	if h.accts == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "account store not configured")
		return
	}
	email, plan, createdAt, err := h.accts.GetAccountByID(r.Context(), accountID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "could not load account")
		return
	}
	writeJSON(w, http.StatusOK, Account{
		AccountID: accountID,
		Email:     email,
		Plan:      plan,
		CreatedAt: createdAt,
	})
}

func (h *Handler) getAccountUsage(w http.ResponseWriter, r *http.Request, accountID string) {
	var usage AccountUsage
	usage.AccountID = accountID

	if h.mboxes != nil {
		mboxes, _ := h.mboxes.ListMailboxes(r.Context(), accountID)
		usage.MailboxCount = len(mboxes)
	}
	if h.devices != nil {
		devs, _ := h.devices.ListDevices(r.Context(), accountID)
		usage.DeviceCount = len(devs)
	}

	writeJSON(w, http.StatusOK, usage)
}

func (h *Handler) getWallet(w http.ResponseWriter, r *http.Request, accountID string) {
	wallet := Wallet{AccountID: accountID}
	if h.billing != nil {
		balZAR, err := h.billing.WalletBalance(r.Context(), accountID)
		if err == nil {
			wallet.BalanceZAR = balZAR
		}
		rate, err := h.billing.ExchangeRate(r.Context())
		if err == nil && rate > 0 {
			wallet.ExchangeRate = rate
			wallet.BalanceUSD = balZAR / rate
		}
	}
	writeJSON(w, http.StatusOK, wallet)
}

func (h *Handler) postTopup(w http.ResponseWriter, r *http.Request, accountID string) {
	var req TopupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.AmountZAR <= 0 {
		writeAPIError(w, http.StatusBadRequest, "amount_zar must be positive")
		return
	}
	if h.topup == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "billing not configured")
		return
	}
	authURL, reference, err := h.topup.InitTopup(r.Context(), accountID, req.Email, req.AmountZAR)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, fmt.Sprintf("paystack error: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, TopupResponse{
		AuthorizationURL: authURL,
		Reference:        reference,
	})
}

func (h *Handler) listMailboxes(w http.ResponseWriter, r *http.Request, accountID string) {
	if h.mboxes == nil {
		writeJSON(w, http.StatusOK, []Mailbox{})
		return
	}
	mboxes, err := h.mboxes.ListMailboxes(r.Context(), accountID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if mboxes == nil {
		mboxes = []Mailbox{}
	}
	writeJSON(w, http.StatusOK, mboxes)
}

func (h *Handler) getMailboxUsage(w http.ResponseWriter, r *http.Request, accountID, mboxID string) {
	if h.mboxes == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "mailbox store not configured")
		return
	}
	usage, err := h.mboxes.MailboxUsage(r.Context(), accountID, mboxID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, usage)
}

func (h *Handler) listDevices(w http.ResponseWriter, r *http.Request, accountID string) {
	if h.devices == nil {
		writeJSON(w, http.StatusOK, []Device{})
		return
	}
	devs, err := h.devices.ListDevices(r.Context(), accountID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if devs == nil {
		devs = []Device{}
	}
	writeJSON(w, http.StatusOK, devs)
}

func (h *Handler) listWebhooks(w http.ResponseWriter, r *http.Request, accountID string) {
	if h.webhooks == nil {
		writeJSON(w, http.StatusOK, []WebhookSub{})
		return
	}
	subs, err := h.webhooks.ListSubscriptions(r.Context(), accountID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if subs == nil {
		subs = []WebhookSub{}
	}
	writeJSON(w, http.StatusOK, subs)
}

func (h *Handler) createWebhook(w http.ResponseWriter, r *http.Request, accountID string) {
	var body struct {
		URL    string   `json:"url"`
		Topics []string `json:"topics"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.URL == "" {
		writeAPIError(w, http.StatusBadRequest, "url is required")
		return
	}
	if h.webhooks == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "webhook service not configured")
		return
	}
	sub, err := h.webhooks.CreateSubscription(r.Context(), accountID, body.URL, body.Topics)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, sub)
}

func (h *Handler) deleteWebhook(w http.ResponseWriter, r *http.Request, accountID, whID string) {
	if h.webhooks == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "webhook service not configured")
		return
	}
	if err := h.webhooks.DeleteSubscription(r.Context(), accountID, whID); err != nil {
		if errors.Is(err, ErrSubNotFound) {
			writeAPIError(w, http.StatusNotFound, "webhook not found")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Errors ───────────────────────────────────────────────────────────────────

// ErrSubNotFound is returned when a webhook subscription is not found.
// Re-declared here so the handler layer does not need to import the webhooks package.
var ErrSubNotFound = errors.New("webhook not found")

// ─── Wire helpers ─────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAPIError(w http.ResponseWriter, status int, msg string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// stubAccountStore is used when real account data is not needed.
type stubAccountStore struct{}

func (s *stubAccountStore) GetAccountByID(_ context.Context, accountID string) (string, string, time.Time, error) {
	return "user@example.com", "pro", time.Now(), nil
}
