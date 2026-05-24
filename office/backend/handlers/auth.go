package handlers

import (
	"net/http"
	"sync"
	"time"

	"vulos-office/backend/config"
	"vulos-office/backend/middleware"
	"vulos-office/backend/models"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

type attemptRecord struct {
	count      int
	lockedUntil time.Time
}

type AuthHandler struct {
	cfg      *config.Config
	mu       sync.Mutex
	attempts map[string]*attemptRecord
}

func NewAuthHandler(cfg *config.Config) *AuthHandler {
	return &AuthHandler{
		cfg:      cfg,
		attempts: make(map[string]*attemptRecord),
	}
}

func (h *AuthHandler) Status(c *gin.Context) {
	authenticated := false
	if h.cfg.Auth.Enabled {
		token := c.GetHeader("Authorization")
		if len(token) > 7 {
			token = token[7:]
		}
		if token == "" {
			if cookie, err := c.Cookie("session"); err == nil {
				token = cookie
			}
		}
		if token != "" {
			claims := &jwt.RegisteredClaims{}
			parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (interface{}, error) {
				return []byte(middleware.JWTSecret), nil
			})
			authenticated = err == nil && parsed.Valid
		}
	} else {
		authenticated = true
	}

	c.JSON(http.StatusOK, models.AuthStatusResponse{
		Enabled:       h.cfg.Auth.Enabled,
		Authenticated: authenticated,
	})
}

func (h *AuthHandler) Login(c *gin.Context) {
	if !h.cfg.Auth.Enabled {
		c.JSON(http.StatusOK, models.LoginResponse{Message: "auth not required"})
		return
	}

	ip := c.ClientIP()

	h.mu.Lock()
	rec, ok := h.attempts[ip]
	if !ok {
		rec = &attemptRecord{}
		h.attempts[ip] = rec
	}

	// Check lockout
	if time.Now().Before(rec.lockedUntil) {
		h.mu.Unlock()
		c.JSON(http.StatusTooManyRequests, models.ErrorResponse{
			Error:       "account locked",
			LockedUntil: rec.lockedUntil.Format(time.RFC3339),
		})
		return
	}
	h.mu.Unlock()

	var req models.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Error: "invalid request"})
		return
	}

	if req.Password != h.cfg.Auth.Password {
		h.mu.Lock()
		rec.count++
		remaining := h.cfg.Auth.MaxAttempts - rec.count
		if remaining <= 0 {
			rec.lockedUntil = time.Now().Add(time.Duration(h.cfg.Auth.LockoutMinutes) * time.Minute)
			rec.count = 0
			remaining = 0
		}
		h.mu.Unlock()

		status := http.StatusUnauthorized
		if remaining == 0 {
			status = http.StatusTooManyRequests
		}
		c.JSON(status, models.ErrorResponse{
			Error:             "incorrect password",
			RemainingAttempts: remaining,
		})
		return
	}

	// Success — reset attempts
	h.mu.Lock()
	rec.count = 0
	rec.lockedUntil = time.Time{}
	h.mu.Unlock()

	expiry := time.Duration(h.cfg.Auth.SessionHours) * time.Hour
	if expiry == 0 {
		expiry = 24 * time.Hour
	}

	claims := jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(expiry)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(middleware.JWTSecret))
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Error: "failed to create session"})
		return
	}

	http.SetCookie(c.Writer, &http.Cookie{
		Name:     "session",
		Value:    signed,
		MaxAge:   int(expiry.Seconds()),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// Secure should be true in production (HTTPS). Set via env/config if needed.
	})
	// Return only a success message — never expose the token in the response body
	// so JavaScript cannot read it.
	c.JSON(http.StatusOK, models.LoginResponse{Message: "logged in"})
}

func (h *AuthHandler) Logout(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     "session",
		Value:    "",
		MaxAge:   -1,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	c.JSON(http.StatusOK, gin.H{"message": "logged out"})
}
