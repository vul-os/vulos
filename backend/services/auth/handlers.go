package auth

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Handler wires OAuth routes into an http.ServeMux.
type Handler struct {
	store         *Store
	providers     map[string]*OAuthProvider
	states        *stateStore
	limiter       *RateLimiter
	OnUserCreated func(username, password, role string) // called after a new user is registered
	OnUserLogin   func(username, password, role string) // called on every successful login to sync credentials
	OnRoleChanged func(username, role string)           // called when a user's role changes
}

func NewHandler(store *Store) *Handler {
	return &Handler{
		store:     store,
		providers: Providers(),
		states:    newStateStore(),
		limiter:   DefaultRateLimiter(),
	}
}

// Register adds all auth routes to the mux.
func (h *Handler) Register(mux *http.ServeMux) {
	// Local auth (primary — username + password)
	mux.HandleFunc("POST /api/auth/register", h.handleRegister)
	mux.HandleFunc("POST /api/auth/login", h.handleLocalLogin)
	mux.HandleFunc("GET /api/auth/status", h.handleAuthStatus)
	mux.HandleFunc("POST /api/auth/change-password", h.handleChangePassword)

	// OAuth (for connecting services, not primary login)
	mux.HandleFunc("GET /api/auth/providers", h.handleProviders)
	mux.HandleFunc("GET /api/auth/login/{provider}", h.handleOAuthLogin)
	mux.HandleFunc("GET /api/auth/callback/{provider}", h.handleCallback)
	mux.HandleFunc("GET /api/auth/me", h.handleMe)
	mux.HandleFunc("POST /api/auth/logout", h.handleLogout)

	// Profile management
	mux.HandleFunc("GET /api/profiles", h.handleListProfiles)
	mux.HandleFunc("GET /api/profiles/{userId}", h.handleGetProfile)
	mux.HandleFunc("PUT /api/profiles/{userId}", h.handleUpdateProfile)
	mux.HandleFunc("DELETE /api/profiles/{userId}", h.handleDeleteProfile)
	mux.HandleFunc("PUT /api/profiles/{userId}/role", h.handleSetRole)
	mux.HandleFunc("POST /api/auth/pin/set", h.handleSetPIN)
	mux.HandleFunc("POST /api/auth/pin/validate", h.handleValidatePIN)

	// Security portal
	mux.HandleFunc("GET /api/auth/security/bans", h.handleListBans)
	mux.HandleFunc("POST /api/auth/security/unban", h.handleUnban)
	mux.HandleFunc("GET /api/auth/security/stats", h.handleSecurityStats)

	log.Printf("[auth] registered providers: %s", strings.Join(providerNames(h.providers), ", "))
}

// publicPaths are endpoints that don't require authentication.
var publicPaths = map[string]bool{
	"/health":                true,
	"/api/auth/providers":    true,
	"/api/auth/me":           true,
	"/api/auth/logout":       true,
	"/api/auth/register":     true,
	"/api/auth/login":        true,
	"/api/auth/status":       true,
	"/api/setup/status":      true,
	"/api/browser/status":    true,
	"/api/open":              true,
	"/manifest.json":         true,
}

// publicPrefixes are path prefixes that don't require authentication.
var publicPrefixes = []string{
	"/api/auth/login/",
	"/api/auth/callback/",
	"/assets/",
}

func isPublicPath(path string) bool {
	if publicPaths[path] {
		return true
	}
	for _, prefix := range publicPrefixes {
		if len(path) >= len(prefix) && path[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// Middleware extracts the session, enforces auth on protected endpoints, and rate limits.
func (h *Handler) Middleware(next http.Handler) http.Handler {
	return h.limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Extract session if present
		token := extractToken(r)
		if token != "" {
			if sess, ok := h.store.ValidateToken(token); ok {
				r.Header.Set("X-User-ID", sess.UserID)
				r.Header.Set("X-User-Email", sess.Email)
			}
		}

		// Enforce auth on all non-public endpoints
		if !isPublicPath(r.URL.Path) && r.Header.Get("X-User-ID") == "" {
			// Allow only frontend static assets without auth (HTML/JS/CSS/images)
			// The React app has its own auth gate in the UI
			p := r.URL.Path
			if p == "/" || p == "/index.html" ||
				strings.HasPrefix(p, "/assets/") ||
				strings.HasSuffix(p, ".js") || strings.HasSuffix(p, ".css") ||
				strings.HasSuffix(p, ".svg") || strings.HasSuffix(p, ".png") ||
				strings.HasSuffix(p, ".ico") || strings.HasSuffix(p, ".woff2") ||
				p == "/sw-proxy.js" {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			fmt.Fprintf(w, `{"error":"unauthorized"}`)
			return
		}

		next.ServeHTTP(w, r)
	}))
}

// --- Local username/password auth ---

func (h *Handler) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"has_users":   h.store.HasAnyUsers(),
		"oauth_providers": providerNames(h.providers),
	})
}

func (h *Handler) handleRegister(w http.ResponseWriter, r *http.Request) {
	ip := extractIP(r)
	if h.limiter.IsBanned(ip) {
		writeErr(w, 429, "too many attempts")
		return
	}

	// Allow unauthenticated registration ONLY when no users exist (first-time setup).
	// Otherwise require an authenticated admin.
	if h.store.HasAnyUsers() {
		reqUserID := r.Header.Get("X-User-ID")
		if reqUserID == "" {
			writeErr(w, 401, "unauthorized")
			return
		}
		reqProfile, _ := h.store.GetProfile(reqUserID)
		if reqProfile == nil || reqProfile.Role != RoleAdmin {
			writeErr(w, 403, "only admins can create users")
			return
		}
	}

	var req struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}

	user, err := h.store.Register(req.Username, req.Password, req.DisplayName)
	if err != nil {
		h.limiter.RecordFailure(ip)
		writeErr(w, 400, err.Error())
		return
	}

	// Create corresponding Linux user with appropriate permissions
	if h.OnUserCreated != nil {
		profile, _ := h.store.GetProfile(user.ID)
		role := "user"
		if profile != nil {
			role = string(profile.Role)
		}
		h.OnUserCreated(req.Username, req.Password, role)
	}

	h.store.Flush()

	// If caller is not authenticated (first-user setup), log them in
	if r.Header.Get("X-User-ID") == "" {
		sess := h.store.CreateSession(user, "")
		h.store.Flush()
		http.SetCookie(w, sessionCookie(r, sess.Token))
		writeJSON(w, map[string]any{"user": user.Safe(), "session": sess})
		return
	}

	// Admin creating a user — just return the new user info
	writeJSON(w, map[string]any{"user": user.Safe()})
}

func (h *Handler) handleLocalLogin(w http.ResponseWriter, r *http.Request) {
	ip := extractIP(r)
	if h.limiter.IsBanned(ip) {
		writeErr(w, 429, "too many attempts")
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}

	user, err := h.store.Login(req.Username, req.Password)
	if err != nil {
		h.limiter.RecordFailure(ip)
		writeErr(w, 401, err.Error())
		return
	}

	h.limiter.RecordSuccess(ip)

	// Sync Linux user password + role on every login
	if h.OnUserLogin != nil {
		profile, _ := h.store.GetProfile(user.ID)
		role := "user"
		if profile != nil {
			role = string(profile.Role)
		}
		h.OnUserLogin(req.Username, req.Password, role)
	}

	sess := h.store.CreateSession(user, "")
	h.store.Flush()

	http.SetCookie(w, sessionCookie(r, sess.Token))

	writeJSON(w, map[string]any{"user": user.Safe(), "session": sess})
}

func (h *Handler) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeErr(w, 401, "unauthorized")
		return
	}
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if err := h.store.ChangePassword(userID, req.OldPassword, req.NewPassword); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	h.store.Flush()
	writeJSON(w, map[string]string{"status": "password changed"})
}

func (h *Handler) handleProviders(w http.ResponseWriter, r *http.Request) {
	var names []string
	for k := range h.providers {
		names = append(names, k)
	}
	writeJSON(w, map[string]any{"providers": names})
}

func (h *Handler) handleOAuthLogin(w http.ResponseWriter, r *http.Request) {
	providerName := r.PathValue("provider")
	provider, ok := h.providers[providerName]
	if !ok {
		writeErr(w, 404, fmt.Sprintf("unknown provider: %s", providerName))
		return
	}

	state := h.states.create()
	deviceID := r.URL.Query().Get("device_id")
	if deviceID != "" {
		h.states.setMeta(state, "device_id", deviceID)
	}

	http.Redirect(w, r, provider.AuthorizeURL(state), http.StatusTemporaryRedirect)
}

func (h *Handler) handleCallback(w http.ResponseWriter, r *http.Request) {
	providerName := r.PathValue("provider")
	provider, ok := h.providers[providerName]
	if !ok {
		writeErr(w, 404, "unknown provider")
		return
	}

	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	ip := extractIP(r)
	if !h.states.validate(state) {
		h.limiter.RecordFailure(ip)
		writeErr(w, 400, "invalid or expired state")
		return
	}

	deviceID := h.states.getMeta(state, "device_id")
	h.states.delete(state)

	// Exchange code for token
	token, err := provider.Exchange(r.Context(), code)
	if err != nil {
		h.limiter.RecordFailure(ip)
		writeErr(w, 500, fmt.Sprintf("token exchange failed: %v", err))
		return
	}

	// Fetch user info
	info, err := provider.FetchUserInfo(r.Context(), token.AccessToken)
	if err != nil {
		h.limiter.RecordFailure(ip)
		writeErr(w, 500, fmt.Sprintf("failed to get user info: %v", err))
		return
	}

	h.limiter.RecordSuccess(ip)

	// Find or create user, create session
	isNew := h.store.GetUserByEmail(info.Email) == nil
	user := h.store.FindOrCreateUser(providerName, info.ProviderID, info.Email, info.Name, info.Picture)
	if isNew && h.OnUserCreated != nil {
		profile, _ := h.store.GetProfile(user.ID)
		role := "user"
		if profile != nil {
			role = string(profile.Role)
		}
		h.OnUserCreated(user.Username, generateRandomPassword(), role)
	}
	sess := h.store.CreateSession(user, deviceID)
	h.store.Flush()

	// Set cookie (long-lived, httponly)
	http.SetCookie(w, sessionCookie(r, sess.Token))

	// Redirect to frontend
	http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
}

func (h *Handler) handleMe(w http.ResponseWriter, r *http.Request) {
	token := extractToken(r)
	if token == "" {
		writeErr(w, 401, "not authenticated")
		return
	}

	sess, ok := h.store.ValidateToken(token)
	if !ok {
		writeErr(w, 401, "invalid or expired session")
		return
	}

	user, ok := h.store.GetUser(sess.UserID)
	if !ok {
		writeErr(w, 404, "user not found")
		return
	}

	profile, _ := h.store.GetProfile(sess.UserID)
	writeJSON(w, map[string]any{
		"user":    user.Safe(),
		"session": map[string]any{"id": sess.ID, "user_id": sess.UserID, "expires_at": sess.ExpiresAt},
		"profile": profile,
	})
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	token := extractToken(r)
	if token != "" {
		h.store.RevokeSession(token)
		h.store.Flush()
	}

	http.SetCookie(w, &http.Cookie{
		Name:   "vulos_session",
		Value:  "",
		Path:   "/",
		Domain: cookieDomain(r),
		MaxAge: -1,
	})

	writeJSON(w, map[string]string{"status": "logged out"})
}

func extractToken(r *http.Request) string {
	// Check Authorization header first
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	// Fall back to cookie
	if c, err := r.Cookie("vulos_session"); err == nil {
		return c.Value
	}
	return ""
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}

// sessionCookie creates a consistent session cookie for all auth endpoints.
// Uses SameSite=None + Secure on HTTPS (required for subdomain iframes).
// Uses SameSite=Lax on HTTP (localhost dev without mkcert).
func sessionCookie(r *http.Request, token string) *http.Cookie {
	isSecure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	sameSite := http.SameSiteLaxMode
	if isSecure {
		sameSite = http.SameSiteNoneMode
	}
	return &http.Cookie{
		Name:     "vulos_session",
		Value:    token,
		Path:     "/",
		Domain:   cookieDomain(r),
		MaxAge:   90 * 24 * 3600,
		HttpOnly: true,
		SameSite: sameSite,
		Secure:   isSecure,
	}
}

// cookieDomain returns the domain for session cookies.
// Uses VULOS_DOMAIN env if set (e.g. "lvh.me" for dev, "vula.example.com" for prod).
// Ensures cookies are shared across app subdomains (cockpit.lvh.me, grafana.lvh.me, etc).
func cookieDomain(r *http.Request) string {
	if d := os.Getenv("VULOS_DOMAIN"); d != "" {
		return d
	}
	host := r.Host
	if idx := strings.Index(host, ":"); idx > 0 {
		host = host[:idx]
	}
	if net.ParseIP(host) != nil {
		return ""
	}
	parts := strings.Split(host, ".")
	if len(parts) >= 2 {
		return strings.Join(parts[len(parts)-2:], ".")
	}
	return ""
}

func providerNames(m map[string]*OAuthProvider) []string {
	var names []string
	for k := range m {
		names = append(names, k)
	}
	return names
}

// stateStore manages CSRF state tokens with expiry.
type stateStore struct {
	mu     sync.Mutex
	states map[string]*stateEntry
}

type stateEntry struct {
	createdAt time.Time
	meta      map[string]string
}

func newStateStore() *stateStore {
	return &stateStore{states: make(map[string]*stateEntry)}
}

func (s *stateStore) create() string {
	b := make([]byte, 16)
	rand.Read(b)
	state := base64.RawURLEncoding.EncodeToString(b)
	s.mu.Lock()
	s.states[state] = &stateEntry{createdAt: time.Now(), meta: map[string]string{}}
	s.mu.Unlock()
	return state
}

func (s *stateStore) validate(state string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.states[state]
	if !ok {
		return false
	}
	return time.Since(entry.createdAt) < 10*time.Minute
}

func (s *stateStore) setMeta(state, key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.states[state]; ok {
		e.meta[key] = value
	}
}

func (s *stateStore) getMeta(state, key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.states[state]; ok {
		return e.meta[key]
	}
	return ""
}

func (s *stateStore) delete(state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.states, state)
}

// --- Profile management handlers ---

func (h *Handler) handleListProfiles(w http.ResponseWriter, r *http.Request) {
	profiles := h.store.ListProfiles()
	writeJSON(w, profiles)
}

func (h *Handler) handleGetProfile(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userId")
	profile, ok := h.store.GetProfile(userID)
	if !ok {
		writeErr(w, 404, "profile not found")
		return
	}
	// Scrub API key from response
	p := *profile
	if p.AIAPIKey != "" {
		p.AIAPIKey = "••••••"
	}
	writeJSON(w, p)
}

func (h *Handler) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userId")

	// Only the user or an admin can update a profile
	reqUserID := r.Header.Get("X-User-ID")
	if reqUserID == "" {
		writeErr(w, 401, "not authenticated")
		return
	}
	if reqUserID != userID {
		reqProfile, _ := h.store.GetProfile(reqUserID)
		if reqProfile == nil || reqProfile.Role != RoleAdmin {
			writeErr(w, 403, "can only update your own profile")
			return
		}
	}

	existing, ok := h.store.GetProfile(userID)
	if !ok {
		writeErr(w, 404, "profile not found")
		return
	}

	var update struct {
		DisplayName *string `json:"display_name"`
		Theme       *string `json:"theme"`
		Locale      *string `json:"locale"`
		Timezone    *string `json:"timezone"`
		AIProvider  *string `json:"ai_provider"`
		AIModel     *string `json:"ai_model"`
		AIAPIKey    *string `json:"ai_api_key"`
		Initiative  *string `json:"initiative"`
	}
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}

	if update.DisplayName != nil { existing.DisplayName = *update.DisplayName }
	if update.Theme != nil { existing.Theme = *update.Theme }
	if update.Locale != nil { existing.Locale = *update.Locale }
	if update.Timezone != nil { existing.Timezone = *update.Timezone }
	if update.AIProvider != nil { existing.AIProvider = *update.AIProvider }
	if update.AIModel != nil { existing.AIModel = *update.AIModel }
	if update.AIAPIKey != nil { existing.AIAPIKey = *update.AIAPIKey }
	if update.Initiative != nil { existing.Initiative = *update.Initiative }

	h.store.SetProfile(existing)
	h.store.Flush()
	writeJSON(w, existing)
}

func (h *Handler) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	reqUserID := r.Header.Get("X-User-ID")
	reqProfile, _ := h.store.GetProfile(reqUserID)
	if reqProfile == nil || reqProfile.Role != RoleAdmin {
		writeErr(w, 403, "admin only")
		return
	}

	userID := r.PathValue("userId")
	if userID == reqUserID {
		writeErr(w, 400, "cannot delete your own profile")
		return
	}

	if err := h.store.DeleteProfile(userID); err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	h.store.Flush()
	writeJSON(w, map[string]string{"status": "deleted"})
}

func (h *Handler) handleSetRole(w http.ResponseWriter, r *http.Request) {
	reqUserID := r.Header.Get("X-User-ID")
	reqProfile, _ := h.store.GetProfile(reqUserID)
	if reqProfile == nil || reqProfile.Role != RoleAdmin {
		writeErr(w, 403, "admin only")
		return
	}

	userID := r.PathValue("userId")
	var req struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}

	role := Role(req.Role)
	if role != RoleAdmin && role != RoleUser && role != RoleGuest {
		writeErr(w, 400, "invalid role: admin, user, or guest")
		return
	}

	if err := h.store.SetRole(userID, role); err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	h.store.Flush()

	// Sync Linux group membership
	if h.OnRoleChanged != nil {
		if user, ok := h.store.GetUser(userID); ok && user != nil {
			h.OnRoleChanged(user.Username, string(role))
		}
	}

	writeJSON(w, map[string]string{"status": "role updated"})
}

// --- Security portal handlers ---

func (h *Handler) handleListBans(w http.ResponseWriter, r *http.Request) {
	reqProfile, _ := h.store.GetProfile(r.Header.Get("X-User-ID"))
	if reqProfile == nil || reqProfile.Role != RoleAdmin {
		writeErr(w, 403, "admin only")
		return
	}
	writeJSON(w, h.limiter.BannedIPs())
}

func (h *Handler) handleUnban(w http.ResponseWriter, r *http.Request) {
	reqProfile, _ := h.store.GetProfile(r.Header.Get("X-User-ID"))
	if reqProfile == nil || reqProfile.Role != RoleAdmin {
		writeErr(w, 403, "admin only")
		return
	}
	var req struct {
		IP string `json:"ip"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	h.limiter.Unban(req.IP)
	writeJSON(w, map[string]string{"status": "unbanned"})
}

func (h *Handler) handleSecurityStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, h.limiter.Stats())
}

// --- PIN handlers ---

func (h *Handler) handleSetPIN(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeErr(w, 401, "not authenticated")
		return
	}
	var req struct{ PIN string `json:"pin"` }
	json.NewDecoder(r.Body).Decode(&req)
	if err := h.store.SetPIN(userID, req.PIN); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	h.store.Flush()
	writeJSON(w, map[string]string{"status": "pin set"})
}

func (h *Handler) handleValidatePIN(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeErr(w, 401, "not authenticated")
		return
	}
	var req struct{ PIN string `json:"pin"` }
	json.NewDecoder(r.Body).Decode(&req)
	valid := h.store.ValidatePIN(userID, req.PIN)
	writeJSON(w, map[string]any{"valid": valid, "has_pin": h.store.HasPIN(userID)})
}

func generateRandomPassword() string {
	b := make([]byte, 24)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
