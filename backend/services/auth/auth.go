package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// bcryptMaxBytes is the maximum password length bcrypt will process.
// Passwords longer than this are silently truncated by bcrypt, which means
// two passwords that share the same first 72 bytes would hash identically.
// We reject inputs longer than this limit at the API layer.
const bcryptMaxBytes = 72

// minPasswordLength is the minimum accepted password length.
const minPasswordLength = 12

// Session represents an authenticated user session.
type Session struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Picture   string    `json:"picture,omitempty"`
	Provider  string    `json:"provider"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	DeviceID  string    `json:"device_id,omitempty"`
}

// User represents a vulos user account.
// Auth is local username + password; external provider links (Providers map) are
// retained for accounts originally created via a social login flow but are not
// used for OS login.
type User struct {
	ID           string            `json:"id"`
	Username     string            `json:"username"`
	PasswordHash string            `json:"password_hash,omitempty"`
	Email        string            `json:"email,omitempty"`
	Name         string            `json:"name"`
	Picture      string            `json:"picture,omitempty"`
	Providers    map[string]string `json:"providers,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	LastLogin    time.Time         `json:"last_login"`
	Preferences  map[string]string `json:"preferences,omitempty"`
}

// SafeUser returns user data without the password hash (for API responses).
func (u *User) Safe() map[string]any {
	return map[string]any{
		"id": u.ID, "username": u.Username, "name": u.Name,
		"email": u.Email, "picture": u.Picture, "created_at": u.CreatedAt,
	}
}

// Store persists users, sessions, and profiles to disk so logins survive reboots.
type Store struct {
	mu       sync.RWMutex
	users    map[string]*User    // user_id -> User
	sessions map[string]*Session // token -> Session
	profiles map[string]*Profile // user_id -> Profile
	path     string
	secret   []byte
	db       *sql.DB // CLUSTER-02: durable write-through (nil => degraded in-memory mode)
}

type storeData struct {
	Users    []*User    `json:"users"`
	Sessions []*Session `json:"sessions"`
	Profiles []*Profile `json:"profiles,omitempty"`
}

// NewStore creates or loads the auth store.
//
// Ordering is load-bearing (D71 regression guard): the SQLite database is
// opened and migrated, then the durable rows are loaded, and only then — if
// the one-time-import sentinel is unset — is the legacy auth.json read into
// the maps and mirrored into SQLite. Persist/delete write-through never runs
// before this sequence completes.
func NewStore(dataDir string) (*Store, error) {
	p := filepath.Join(dataDir, "auth.json")
	s := &Store{
		users:    make(map[string]*User),
		sessions: make(map[string]*Session),
		profiles: make(map[string]*Profile),
		path:     p,
		secret:   loadOrCreateSecret(filepath.Join(dataDir, "auth.key")),
	}

	// 1. Open + migrate the durable store. Failure => degraded in-memory mode.
	db, err := openDB(filepath.Join(dataDir, "auth.db"))
	if err != nil {
		log.Printf("[auth] sqlite unavailable, running in degraded in-memory mode: %v", err)
	} else {
		s.db = db
	}

	// 2. Load the authoritative working set from SQLite.
	if err := s.loadFromDB(); err != nil {
		log.Printf("[auth] sqlite load failed, continuing: %v", err)
	}

	// 3. One-time legacy auth.json -> SQLite import, sentinel-guarded. Only
	//    runs when SQLite has never been seeded. In degraded mode (db == nil)
	//    we still load auth.json into memory so the app keeps working.
	if s.db == nil || !s.legacyImported() {
		if data, err := os.ReadFile(p); err == nil {
			var d storeData
			if json.Unmarshal(data, &d) == nil {
				for _, u := range d.Users {
					s.users[u.ID] = u
				}
				for _, sess := range d.Sessions {
					if sess.ExpiresAt.After(time.Now()) {
						s.sessions[sess.Token] = sess
					}
				}
				for _, p := range d.Profiles {
					s.profiles[p.UserID] = p
				}
			}
		}
		s.importLegacyJSON()
	}
	return s, nil
}

// FindOrCreateUser finds a user by provider+providerID, or creates a new one.
//
// emailVerified MUST reflect whether the identity provider has verified
// ownership of `email`. It gates the email-match linking branch: linking a
// provider identity onto a pre-existing local account by email is an account
// takeover vector if the email is attacker-controlled and unverified, so we
// only merge on a verified email. An unverified email never links to an
// existing account — it falls through to creating a fresh, separate user keyed
// by the provider identity.
func (s *Store) FindOrCreateUser(provider, providerUserID, email, name, picture string, emailVerified bool) *User {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Find existing by provider link
	for _, u := range s.users {
		if u.Providers[provider] == providerUserID {
			u.LastLogin = time.Now()
			u.Name = name
			u.Picture = picture
			s.persistUser(u)
			return u
		}
	}

	// Find by email and link provider — ONLY when the provider has verified
	// the email. Without this check a signed-in identity bearing a victim's
	// (unverified) email would be silently merged into the victim's account.
	if emailVerified && email != "" {
		for _, u := range s.users {
			if u.Email == email {
				u.Providers[provider] = providerUserID
				u.LastLogin = time.Now()
				u.Name = name
				u.Picture = picture
				s.persistUser(u)
				return u
			}
		}
	}

	// Create new user — derive username from email or name
	username := deriveUsername(email, name)
	for s.usernameTaken(username) {
		username += fmt.Sprintf("%d", time.Now().UnixNano()%1000)
	}

	u := &User{
		ID:        generateID(),
		Username:  username,
		Email:     email,
		Name:      name,
		Picture:   picture,
		Providers: map[string]string{provider: providerUserID},
		CreatedAt: time.Now(),
		LastLogin: time.Now(),
	}
	s.users[u.ID] = u

	// Create default profile. Provider/cloud logins are NEVER silently granted
	// admin just for being the first account on the box — that is a privilege
	// escalation if anyone can mint a cloud login. Admin is provisioned
	// explicitly: via native first-run Register(), or by naming a verified
	// bootstrap-admin email in VULOS_BOOTSTRAP_ADMIN_EMAIL.
	role := RoleUser
	if emailVerified && email != "" {
		if want := strings.TrimSpace(os.Getenv("VULOS_BOOTSTRAP_ADMIN_EMAIL")); want != "" &&
			strings.EqualFold(want, email) {
			role = RoleAdmin
			log.Printf("[auth] provisioning bootstrap admin for verified email %s (provider=%s)", email, provider)
		}
	}
	p := DefaultProfile(u.ID, name)
	p.Role = role
	p.Avatar = picture
	s.profiles[u.ID] = p

	s.persistUser(u)
	s.persistProfile(p)

	return u
}

// CreateSession creates a long-lived session token for a user+device.
func (s *Store) CreateSession(user *User, deviceID string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Reuse existing session for same device if still valid
	for _, sess := range s.sessions {
		if sess.UserID == user.ID && sess.DeviceID == deviceID && sess.ExpiresAt.After(time.Now()) {
			sess.ExpiresAt = time.Now().Add(90 * 24 * time.Hour) // extend
			s.persistSession(sess)
			return sess
		}
	}

	token := s.generateToken(user.ID)
	sess := &Session{
		ID:        generateID(),
		UserID:    user.ID,
		Email:     user.Email,
		Name:      user.Name,
		Picture:   user.Picture,
		Provider:  firstProvider(user.Providers),
		Token:     token,
		ExpiresAt: time.Now().Add(90 * 24 * time.Hour), // 90 days
		CreatedAt: time.Now(),
		DeviceID:  deviceID,
	}
	s.sessions[token] = sess
	s.persistSession(sess)
	return sess
}

// ValidateToken checks a session token and returns the session if valid.
func (s *Store) ValidateToken(token string) (*Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sess, ok := s.sessions[token]
	if !ok || sess.ExpiresAt.Before(time.Now()) {
		return nil, false
	}
	return sess, true
}

// GetUser returns a user by ID.
func (s *Store) GetUser(userID string) (*User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[userID]
	return u, ok
}

// RevokeSession removes a session.
func (s *Store) RevokeSession(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, token)
	s.deleteSession(token)
}

// RevokeAllSessions removes all sessions for a user.
func (s *Store) RevokeAllSessions(userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, sess := range s.sessions {
		if sess.UserID == userID {
			delete(s.sessions, token)
		}
	}
	s.deleteUserSessions(userID)
}

// Flush is a no-op. CLUSTER-02 makes every mutating Store method write through
// to SQLite synchronously, so there is nothing to flush. Kept for API
// compatibility — the signature is unchanged and callers need no changes.
func (s *Store) Flush() error {
	return nil
}

func (s *Store) generateToken(userID string) string {
	b := make([]byte, 32)
	rand.Read(b)
	payload := fmt.Sprintf("%s:%s:%d", userID, base64.RawURLEncoding.EncodeToString(b), time.Now().UnixNano())
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(payload))
	sig := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func generateID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func loadOrCreateSecret(path string) []byte {
	if data, err := os.ReadFile(path); err == nil && len(data) >= 32 {
		return data
	}
	secret := make([]byte, 32)
	rand.Read(secret)
	os.MkdirAll(filepath.Dir(path), 0700)
	os.WriteFile(path, secret, 0600)
	return secret
}

// GetUserByUsername returns a user by username (nil if not found).
func (s *Store) GetUserByUsername(username string) *User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if username == "" {
		return nil
	}
	for _, u := range s.users {
		if u.Username == username {
			return u
		}
	}
	return nil
}

// GetUserByEmail returns a user by email (nil if not found).
func (s *Store) GetUserByEmail(email string) *User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if email == "" {
		return nil
	}
	for _, u := range s.users {
		if u.Email == email {
			return u
		}
	}
	return nil
}

func (s *Store) usernameTaken(username string) bool {
	for _, u := range s.users {
		if u.Username == username {
			return true
		}
	}
	return false
}

func deriveUsername(email, name string) string {
	// Try email local part first
	if idx := strings.Index(email, "@"); idx > 0 {
		return strings.ToLower(email[:idx])
	}
	// Fall back to name
	return strings.ToLower(strings.ReplaceAll(name, " ", ""))
}

func firstProvider(m map[string]string) string {
	for k := range m {
		return k
	}
	return ""
}

// --- Local username/password auth ---

func hashPassword(password string) string {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		// bcrypt should never fail for a valid password; panic rather than silently
		// falling back to a weaker scheme that could be subtly exploited.
		panic(fmt.Sprintf("bcrypt.GenerateFromPassword failed: %v", err))
	}
	return string(hash)
}

func verifyPassword(hash, password string) bool {
	// Only accept bcrypt hashes (prefix "$2"). The legacy SHA-256 fallback has
	// been removed: any stored hash that is not bcrypt must be re-set via a
	// password-change flow before the account can log in again.
	if !isbcryptHash(hash) {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// Register creates a new local user with username + password.
// First user gets admin role.
func (s *Store) Register(username, password, displayName string) (*User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check username uniqueness
	for _, u := range s.users {
		if u.Username == username {
			return nil, fmt.Errorf("username already taken")
		}
	}

	if len(username) < 2 {
		return nil, fmt.Errorf("username must be 2+ chars")
	}
	if len(password) < minPasswordLength {
		return nil, fmt.Errorf("password must be %d+ chars", minPasswordLength)
	}
	if len(password) > bcryptMaxBytes {
		return nil, fmt.Errorf("password must be %d chars or fewer", bcryptMaxBytes)
	}

	hash := hashPassword(password)

	// Verify the hash works immediately — catch bcrypt issues at registration time
	if !verifyPassword(hash, password) {
		log.Printf("[auth] CRITICAL: password hash verification failed at registration for %q", username)
		return nil, fmt.Errorf("internal error: password hash verification failed")
	}

	u := &User{
		ID:           generateID(),
		Username:     username,
		PasswordHash: hash,
		Name:         displayName,
		Providers:    make(map[string]string),
		CreatedAt:    time.Now(),
		LastLogin:    time.Now(),
	}
	s.users[u.ID] = u
	log.Printf("[auth] registered user %q (id=%s)", username, u.ID)

	// Create profile — first user is admin
	role := RoleUser
	if len(s.users) == 1 {
		role = RoleAdmin
	}
	p := DefaultProfile(u.ID, displayName)
	p.Role = role
	s.profiles[u.ID] = p

	s.persistUser(u)
	s.persistProfile(p)

	return u, nil
}

// errInvalidLogin is the single, uniform failure returned for every bad login
// regardless of cause (unknown user, no password set, wrong password). A
// distinct "no password set" message would let an attacker enumerate accounts.
var errInvalidLogin = fmt.Errorf("invalid username or password")

// dummyBcryptHash is a valid bcrypt hash of a random throwaway password. It is
// compared against on the unknown-user / no-password paths so those paths spend
// the same time as a real bcrypt verify — closing the timing oracle that would
// otherwise reveal whether a username exists.
var dummyBcryptHash = func() string {
	b := make([]byte, 32)
	rand.Read(b)
	h, err := bcrypt.GenerateFromPassword(b, bcrypt.DefaultCost)
	if err != nil {
		panic(fmt.Sprintf("auth: bcrypt dummy hash init failed: %v", err))
	}
	return string(h)
}()

// Login validates username + password and returns the user.
func (s *Store) Login(username, password string) (*User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, u := range s.users {
		if u.Username == username {
			// Known user but no usable bcrypt hash: burn an equivalent bcrypt
			// compare so timing matches the success path, then fail uniformly.
			if !isbcryptHash(u.PasswordHash) {
				bcrypt.CompareHashAndPassword([]byte(dummyBcryptHash), []byte(password))
				log.Printf("[auth] login failed for %q: no usable password hash", username)
				return nil, errInvalidLogin
			}
			if !verifyPassword(u.PasswordHash, password) {
				log.Printf("[auth] login failed for %q: password mismatch", username)
				return nil, errInvalidLogin
			}
			u.LastLogin = time.Now()
			s.persistUser(u)
			log.Printf("[auth] login OK for %q", username)
			return u, nil
		}
	}
	// Unknown user: run a dummy bcrypt compare so the unknown-user path costs
	// the same as the known-user path (no user-enumeration timing oracle).
	bcrypt.CompareHashAndPassword([]byte(dummyBcryptHash), []byte(password))
	log.Printf("[auth] login failed: user %q not found", username)
	return nil, errInvalidLogin
}

// HasAnyUsers returns true if at least one user exists.
func (s *Store) HasAnyUsers() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.users) > 0
}

// ListUsernames returns all usernames in the store.
func (s *Store) ListUsernames() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.users))
	for _, u := range s.users {
		out = append(out, u.Username)
	}
	return out
}

// UserRole is a username + role pair for system reconciliation.
type UserRole struct {
	Username string
	Role     string
}

// ListUsersWithRoles returns all users with their profile roles.
func (s *Store) ListUsersWithRoles() []UserRole {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []UserRole
	for id, u := range s.users {
		role := "user"
		if p, ok := s.profiles[id]; ok {
			role = string(p.Role)
		}
		out = append(out, UserRole{Username: u.Username, Role: role})
	}
	return out
}

// ChangePassword updates a user's password.
func (s *Store) ChangePassword(userID, oldPassword, newPassword string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[userID]
	if !ok {
		return fmt.Errorf("user not found")
	}
	if u.PasswordHash != "" && !verifyPassword(u.PasswordHash, oldPassword) {
		return fmt.Errorf("incorrect current password")
	}
	if len(newPassword) < minPasswordLength {
		return fmt.Errorf("password must be %d+ chars", minPasswordLength)
	}
	if len(newPassword) > bcryptMaxBytes {
		return fmt.Errorf("password must be %d chars or fewer", bcryptMaxBytes)
	}
	u.PasswordHash = hashPassword(newPassword)

	// SEC-J: a password change invalidates every existing session for this
	// user, forcing re-authentication everywhere. This loop is the existing
	// SEC-J behaviour and runs FIRST, unchanged.
	revoked := make([]string, 0)
	for token, sess := range s.sessions {
		if sess.UserID == userID {
			delete(s.sessions, token)
			revoked = append(revoked, token)
		}
	}

	// CLUSTER-02: AFTER SEC-J's revoke loop, mirror the change into SQLite —
	// persist the updated user, then delete the revoked sessions. Additive;
	// does not reorder or restructure the logic above.
	s.persistUser(u)
	for _, token := range revoked {
		s.deleteSession(token)
	}

	return nil
}

// isbcryptHash returns true when hash was produced by bcrypt (starts with $2).
func isbcryptHash(hash string) bool {
	return strings.HasPrefix(hash, "$2")
}

// at10FirstUser returns the first registered user — the admin by convention.
// Returns nil if no users exist.
func (s *Store) at10FirstUser() *User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Prefer an explicit admin profile.
	for id, p := range s.profiles {
		if p.Role == RoleAdmin {
			return s.users[id]
		}
	}
	// Fallback: return any user.
	for _, u := range s.users {
		return u
	}
	return nil
}
