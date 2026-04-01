package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
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
// Primary auth is username + password. OAuth is optional for service connections.
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
	users    map[string]*User     // user_id -> User
	sessions map[string]*Session  // token -> Session
	profiles map[string]*Profile  // user_id -> Profile
	path     string
	secret   []byte
}

type storeData struct {
	Users    []*User    `json:"users"`
	Sessions []*Session `json:"sessions"`
	Profiles []*Profile `json:"profiles,omitempty"`
}

// NewStore creates or loads the auth store.
func NewStore(dataDir string) (*Store, error) {
	p := filepath.Join(dataDir, "auth.json")
	s := &Store{
		users:    make(map[string]*User),
		sessions: make(map[string]*Session),
		profiles: make(map[string]*Profile),
		path:     p,
		secret:   loadOrCreateSecret(filepath.Join(dataDir, "auth.key")),
	}

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
	return s, nil
}

// FindOrCreateUser finds a user by provider+providerID, or creates a new one.
func (s *Store) FindOrCreateUser(provider, providerUserID, email, name, picture string) *User {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Find existing by provider link
	for _, u := range s.users {
		if u.Providers[provider] == providerUserID {
			u.LastLogin = time.Now()
			u.Name = name
			u.Picture = picture
			return u
		}
	}

	// Find by email and link provider
	for _, u := range s.users {
		if u.Email == email {
			u.Providers[provider] = providerUserID
			u.LastLogin = time.Now()
			u.Name = name
			u.Picture = picture
			return u
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

	// Create default profile — first user gets admin
	role := RoleUser
	if len(s.users) == 1 {
		role = RoleAdmin
	}
	p := DefaultProfile(u.ID, name)
	p.Role = role
	p.Avatar = picture
	s.profiles[u.ID] = p

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
}

// Flush persists to disk.
func (s *Store) Flush() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	d := storeData{}
	for _, u := range s.users {
		d.Users = append(d.Users, u)
	}
	for _, sess := range s.sessions {
		if sess.ExpiresAt.After(time.Now()) {
			d.Sessions = append(d.Sessions, sess)
		}
	}
	for _, p := range s.profiles {
		d.Profiles = append(d.Profiles, p)
	}

	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
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
		// Fallback to SHA256 if bcrypt fails (should never happen)
		salt := make([]byte, 16)
		rand.Read(salt)
		saltHex := fmt.Sprintf("%x", salt)
		h := sha256.Sum256([]byte(saltHex + ":" + password))
		return saltHex + "$" + fmt.Sprintf("%x", h)
	}
	return string(hash)
}

func verifyPassword(hash, password string) bool {
	// Try bcrypt first (new hashes)
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err == nil {
		return true
	}
	// Fall back to legacy SHA256 for existing users (auto-migrated on next password change)
	idx := strings.Index(hash, "$")
	if idx < 0 {
		return false
	}
	salt := hash[:idx]
	h := sha256.Sum256([]byte(salt + ":" + password))
	return hash == salt+"$"+fmt.Sprintf("%x", h)
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

	if len(username) < 2 || len(password) < 4 {
		return nil, fmt.Errorf("username must be 2+ chars, password 4+ chars")
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
	log.Printf("[auth] registered user %q (id=%s, hash_prefix=%s)", username, u.ID, hash[:20])

	// Create profile — first user is admin
	role := RoleUser
	if len(s.users) == 1 {
		role = RoleAdmin
	}
	p := DefaultProfile(u.ID, displayName)
	p.Role = role
	s.profiles[u.ID] = p

	return u, nil
}

// Login validates username + password and returns the user.
func (s *Store) Login(username, password string) (*User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, u := range s.users {
		if u.Username == username {
			if u.PasswordHash == "" {
				log.Printf("[auth] login failed for %q: no password set", username)
				return nil, fmt.Errorf("account has no password set")
			}
			if !verifyPassword(u.PasswordHash, password) {
				log.Printf("[auth] login failed for %q: password mismatch (hash_prefix=%s, pw_len=%d)", username, u.PasswordHash[:20], len(password))
				return nil, fmt.Errorf("invalid username or password")
			}
			u.LastLogin = time.Now()
			log.Printf("[auth] login OK for %q", username)
			return u, nil
		}
	}
	log.Printf("[auth] login failed: user %q not found (have %d users)", username, len(s.users))
	return nil, fmt.Errorf("invalid username or password")
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
	if len(newPassword) < 4 {
		return fmt.Errorf("password must be 4+ chars")
	}
	u.PasswordHash = hashPassword(newPassword)
	return nil
}
