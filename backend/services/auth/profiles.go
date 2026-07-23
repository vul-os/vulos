package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"time"
)

// Role defines user permission levels.
type Role string

const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
	RoleGuest Role = "guest"
)

// Profile extends a User with OS-level settings and role.
type Profile struct {
	UserID      string            `json:"user_id"`
	Role        Role              `json:"role"`
	DisplayName string            `json:"display_name"`
	Avatar      string            `json:"avatar,omitempty"`
	Theme       string            `json:"theme"`       // "dark", "light", "auto"
	Locale      string            `json:"locale"`      // e.g., "en-ZA"
	Timezone    string            `json:"timezone"`    // e.g., "Africa/Johannesburg"
	AIProvider  string            `json:"ai_provider"` // "claude", "openai", "ollama"
	AIModel     string            `json:"ai_model"`
	AIAPIKey    string            `json:"ai_api_key,omitempty"`
	Initiative  string            `json:"initiative"` // "minimal", "balanced", "proactive"
	PinHash     string            `json:"pin_hash,omitempty"`
	Settings    map[string]string `json:"settings,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// DefaultProfile creates a default profile for a new user.
func DefaultProfile(userID, displayName string) *Profile {
	return &Profile{
		UserID:      userID,
		Role:        RoleUser,
		DisplayName: displayName,
		Theme:       "dark",
		Locale:      "en",
		Timezone:    "UTC",
		Initiative:  "balanced",
		Settings:    map[string]string{},
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
}

// --- Profile management on Store ---

// GetProfile returns the OS profile for a user.
func (s *Store) GetProfile(userID string) (*Profile, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.profiles[userID]
	return p, ok
}

// SetProfile creates or updates a user's OS profile.
func (s *Store) SetProfile(p *Profile) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p.UpdatedAt = time.Now()
	s.profiles[p.UserID] = p
	s.persistProfile(p)
}

// ListProfiles returns all user profiles.
func (s *Store) ListProfiles() []*Profile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*Profile, 0, len(s.profiles))
	for _, p := range s.profiles {
		result = append(result, p)
	}
	return result
}

// DeleteProfile removes a user profile (does not delete the user account).
func (s *Store) DeleteProfile(userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.profiles[userID]; !ok {
		return fmt.Errorf("profile not found")
	}
	delete(s.profiles, userID)
	s.deleteProfile(userID)
	return nil
}

// SetRole changes a user's role. Only admins should call this.
func (s *Store) SetRole(userID string, role Role) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.profiles[userID]
	if !ok {
		return fmt.Errorf("profile not found")
	}
	p.Role = role
	p.UpdatedAt = time.Now()
	s.persistProfile(p)
	// ACCOUNTSECURITY-IP: "role_change" is fired by the HTTP handler
	// (Handler.handleSetRole) with the real client IP/user-agent — see that
	// handler's comment. SetRole has exactly one caller (that handler); firing
	// here too would double-record the same logical event.
	return nil
}

// AdminCount returns the number of admin users.
func (s *Store) AdminCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, p := range s.profiles {
		if p.Role == RoleAdmin {
			count++
		}
	}
	return count
}

// --- PIN ---

func hashPIN(pin, salt string) string {
	// SHA256 with per-PIN random salt (not bcrypt since PINs are short — rate limiting is the real defense)
	h := sha256.Sum256([]byte(salt + ":" + pin))
	return salt + "$" + hex.EncodeToString(h[:])
}

func generateSalt() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func extractSalt(hash string) string {
	for i, c := range hash {
		if c == '$' {
			return hash[:i]
		}
	}
	return ""
}

// SetPIN sets the lock screen PIN for a user.
func (s *Store) SetPIN(userID, pin string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.profiles[userID]
	if !ok {
		return fmt.Errorf("profile not found")
	}
	if pin == "" {
		p.PinHash = ""
	} else {
		p.PinHash = hashPIN(pin, generateSalt())
	}
	p.UpdatedAt = time.Now()
	s.persistProfile(p)
	return nil
}

// ValidatePIN checks a PIN against the stored hash. Returns true if no PIN is set.
// Uses subtle.ConstantTimeCompare to avoid timing oracle on the hash comparison.
func (s *Store) ValidatePIN(userID, pin string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.profiles[userID]
	if !ok {
		return false
	}
	if p.PinHash == "" {
		return true
	}
	salt := extractSalt(p.PinHash)
	expected := hashPIN(pin, salt)
	// SEC-PIN-01: use constant-time comparison to prevent timing oracle.
	return subtle.ConstantTimeCompare([]byte(p.PinHash), []byte(expected)) == 1
}

// HasPIN returns whether a user has a PIN set.
func (s *Store) HasPIN(userID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.profiles[userID]
	return ok && p.PinHash != ""
}
