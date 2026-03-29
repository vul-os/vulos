package pty

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/creack/pty"
	"golang.org/x/net/websocket"

	"vulos/backend/services/sysuser"
)

const scrollbackSize = 64 * 1024 // 64KB ring buffer

// UserResolver maps a Vula user ID to a Linux username.
type UserResolver func(userID string) string

// Service manages PTY sessions bridged to WebSocket clients.
type Service struct {
	mu       sync.Mutex
	sessions map[string]*Session
	shell    string
	sysUsers *sysuser.Service
	resolve  UserResolver
}

// Session is a single PTY session that persists independently of WebSocket connections.
type Session struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
	Attached  bool      `json:"attached"`
	Alive     bool      `json:"alive"`

	mu         sync.Mutex
	ptmx       *os.File
	cmd        *exec.Cmd
	done       chan struct{}
	scrollback *ringBuffer
	ws         *websocket.Conn // current attached client (nil = detached)
	detachCh   chan struct{}   // closed when WS detaches
}

// ringBuffer is a fixed-size circular buffer for scrollback.
type ringBuffer struct {
	buf  []byte
	pos  int
	full bool
}

func newRingBuffer(size int) *ringBuffer {
	return &ringBuffer{buf: make([]byte, size)}
}

func (r *ringBuffer) Write(data []byte) {
	for _, b := range data {
		r.buf[r.pos] = b
		r.pos++
		if r.pos == len(r.buf) {
			r.pos = 0
			r.full = true
		}
	}
}

func (r *ringBuffer) Bytes() []byte {
	if !r.full {
		return r.buf[:r.pos]
	}
	out := make([]byte, len(r.buf))
	n := copy(out, r.buf[r.pos:])
	copy(out[n:], r.buf[:r.pos])
	return out
}

func NewService(sys *sysuser.Service, resolve UserResolver) *Service {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	return &Service{
		sessions: make(map[string]*Session),
		shell:    shell,
		sysUsers: sys,
		resolve:  resolve,
	}
}

// Handler returns the WebSocket endpoint for PTY sessions.
// Query params:
//   - cols, rows: terminal size
//   - session: existing session ID to reattach (omit to create new)
func (s *Service) Handler() http.Handler {
	return websocket.Handler(func(ws *websocket.Conn) {
		ws.PayloadType = websocket.BinaryFrame

		userID := ws.Request().Header.Get("X-User-ID")
		query := ws.Request().URL.Query()
		cols := parseIntOr(query.Get("cols"), 80)
		rows := parseIntOr(query.Get("rows"), 24)
		sessionID := query.Get("session")

		var sess *Session
		var err error

		if sessionID != "" {
			// Reattach to existing session
			sess = s.getSession(sessionID, userID)
			if sess == nil {
				log.Printf("[pty] reattach failed: session %s not found for user %s", sessionID, userID)
				return
			}
			// Resize to new client's terminal size
			s.handleResize(sess, cols, rows)
		} else {
			// Create new session
			sess, err = s.createSession(userID, uint16(cols), uint16(rows))
			if err != nil {
				log.Printf("[pty] failed to create session: %v", err)
				return
			}
		}

		s.attach(sess, ws)
		defer s.detach(sess)

		// Send scrollback to newly attached client
		sess.mu.Lock()
		scrollback := sess.scrollback.Bytes()
		sess.mu.Unlock()
		if len(scrollback) > 0 {
			ws.Write(scrollback)
		}

		inputDone := make(chan struct{})

		// WebSocket → PTY (input)
		go func() {
			defer close(inputDone)
			buf := make([]byte, 4096)
			for {
				n, err := ws.Read(buf)
				if err != nil {
					return
				}
				if n > 0 {
					if buf[0] == 1 && n > 1 {
						c, r := parseTwoInts(string(buf[1:n]))
						if c > 0 && r > 0 {
							s.handleResize(sess, c, r)
						}
						continue
					}
					sess.ptmx.Write(buf[:n])
				}
			}
		}()

		// Wait for WS disconnect or process exit
		select {
		case <-inputDone:
			// Client disconnected — session stays alive (detached)
		case <-sess.done:
			// Shell process exited — clean up
			s.destroySession(sess.ID)
		}
	})
}

// attach connects a WebSocket client to a session, disconnecting any previous client.
func (s *Service) attach(sess *Session, ws *websocket.Conn) {
	sess.mu.Lock()
	defer sess.mu.Unlock()

	// Kick previous client if still attached
	if sess.ws != nil {
		sess.ws.Close()
		close(sess.detachCh)
	}

	sess.ws = ws
	sess.detachCh = make(chan struct{})

	log.Printf("[pty] session %s attached", sess.ID)
}

// detach disconnects the WebSocket from the session without killing it.
func (s *Service) detach(sess *Session) {
	sess.mu.Lock()
	defer sess.mu.Unlock()

	if sess.ws != nil {
		sess.ws.Close()
		sess.ws = nil
	}

	log.Printf("[pty] session %s detached", sess.ID)
}

func (s *Service) getSession(id, userID string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok || sess.UserID != userID {
		return nil
	}
	return sess
}

func (s *Service) createSession(userID string, cols, rows uint16) (*Session, error) {
	cmd := exec.Command(s.shell, "--login")
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"LANG=en_US.UTF-8",
		"HOSTNAME=vula",
	)

	if s.resolve != nil && s.sysUsers != nil && os.Getuid() == 0 {
		if username := s.resolve(userID); username != "" {
			if procAttr, homeDir := s.sysUsers.Credential(username); procAttr != nil {
				cmd.SysProcAttr = procAttr
				cmd.Dir = homeDir
				cmd.Env = append(cmd.Env,
					"HOME="+homeDir,
					"USER="+username,
					"SHELL="+s.shell,
				)
			}
		}
	}

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Cols: cols,
		Rows: rows,
	})
	if err != nil {
		return nil, err
	}

	sess := &Session{
		ID:         generateID(),
		UserID:     userID,
		CreatedAt:  time.Now(),
		Alive:      true,
		ptmx:       ptmx,
		cmd:        cmd,
		done:       make(chan struct{}),
		scrollback: newRingBuffer(scrollbackSize),
	}

	// PTY reader: buffers scrollback and forwards to attached client
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := sess.ptmx.Read(buf)
			if err != nil {
				return
			}
			if n > 0 {
				data := sanitizeUTF8(buf[:n])

				sess.mu.Lock()
				sess.scrollback.Write(data)
				ws := sess.ws
				sess.mu.Unlock()

				if ws != nil {
					if _, err := ws.Write(data); err != nil {
						// Write failed — detach client but keep session alive
						sess.mu.Lock()
						sess.ws = nil
						sess.mu.Unlock()
					}
				}
			}
		}
	}()

	// Monitor process exit
	go func() {
		cmd.Wait()
		sess.mu.Lock()
		sess.Alive = false
		sess.mu.Unlock()
		close(sess.done)
	}()

	s.mu.Lock()
	s.sessions[sess.ID] = sess
	s.mu.Unlock()

	log.Printf("[pty] session %s started (user=%s, shell=%s, %dx%d)",
		sess.ID, userID, s.shell, cols, rows)
	return sess, nil
}

func (s *Service) destroySession(id string) {
	s.mu.Lock()
	sess, ok := s.sessions[id]
	if ok {
		delete(s.sessions, id)
	}
	s.mu.Unlock()

	if sess != nil {
		sess.mu.Lock()
		if sess.ws != nil {
			sess.ws.Close()
			sess.ws = nil
		}
		sess.mu.Unlock()

		sess.ptmx.Close()
		if sess.cmd.Process != nil {
			sess.cmd.Process.Kill()
		}
		log.Printf("[pty] session %s destroyed", id)
	}
}

// DestroySession kills a session by ID (for the REST endpoint).
func (s *Service) DestroySession(id, userID string) bool {
	sess := s.getSession(id, userID)
	if sess == nil {
		return false
	}
	s.destroySession(id)
	return true
}

func (s *Service) handleResize(sess *Session, cols, rows int) {
	if cols > 0 && rows > 0 {
		pty.Setsize(sess.ptmx, &pty.Winsize{
			Cols: uint16(cols),
			Rows: uint16(rows),
		})
	}
}

// SessionInfo is the JSON representation of a session for the list endpoint.
type SessionInfo struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at"`
	Attached  bool   `json:"attached"`
	Alive     bool   `json:"alive"`
}

// ListSessions returns sessions belonging to a user.
func (s *Service) ListSessions(userID string) []SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []SessionInfo
	for _, sess := range s.sessions {
		if sess.UserID != userID {
			continue
		}
		sess.mu.Lock()
		info := SessionInfo{
			ID:        sess.ID,
			CreatedAt: sess.CreatedAt.Format(time.RFC3339),
			Attached:  sess.ws != nil,
			Alive:     sess.Alive,
		}
		sess.mu.Unlock()
		out = append(out, info)
	}
	return out
}

// SessionsHandler returns an http.HandlerFunc for listing/killing sessions.
func (s *Service) SessionsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")

		switch r.Method {
		case http.MethodGet:
			sessions := s.ListSessions(userID)
			if sessions == nil {
				sessions = []SessionInfo{}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(sessions)

		case http.MethodDelete:
			id := r.URL.Query().Get("id")
			if id == "" {
				http.Error(w, "missing id", http.StatusBadRequest)
				return
			}
			if s.DestroySession(id, userID) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"status":"destroyed"}`))
			} else {
				http.Error(w, "session not found", http.StatusNotFound)
			}

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// DestroyAll kills all sessions on shutdown.
func (s *Service) DestroyAll() {
	s.mu.Lock()
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.destroySession(id)
	}
}

// ActiveSessions returns count of active PTY sessions.
func (s *Service) ActiveSessions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

func sanitizeUTF8(data []byte) []byte {
	if utf8.Valid(data) {
		return data
	}
	out := make([]byte, 0, len(data))
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size == 1 {
			out = append(out, '?')
			data = data[1:]
		} else {
			out = append(out, data[:size]...)
			data = data[size:]
		}
	}
	return out
}

func generateID() string {
	return time.Now().Format("20060102150405.000000")
}

func parseIntOr(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	var n int
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	if n == 0 {
		return fallback
	}
	return n
}

func parseTwoInts(s string) (int, int) {
	var a, b int
	idx := 0
	for _, c := range s {
		if c == ',' {
			idx = 1
			continue
		}
		if c >= '0' && c <= '9' {
			if idx == 0 {
				a = a*10 + int(c-'0')
			} else {
				b = b*10 + int(c-'0')
			}
		}
	}
	return a, b
}
