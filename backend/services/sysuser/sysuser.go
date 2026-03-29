package sysuser

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// Service manages Linux system users that map to Vula OS profiles.
// Each Vula profile gets a real Linux user with sudo access and bash.
type Service struct {
	mu sync.Mutex
}

func New() *Service {
	return &Service{}
}

// EnsureUser creates a Linux user for the given username if it doesn't exist.
// The user gets: home directory, bash shell, wheel group (sudo), and the given password.
func (s *Service) EnsureUser(username, password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if u, err := user.Lookup(username); err == nil {
		// User exists — ensure .bashrc is present
		s.ensureBashrc(u.HomeDir, username)
		return nil
	}

	// Sanitize username for Linux (lowercase, alphanumeric + dash/underscore)
	sysName := sanitizeUsername(username)
	if sysName == "" {
		return fmt.Errorf("invalid username: %q", username)
	}

	// Create user with home dir, bash shell, wheel group
	cmd := exec.Command("adduser", "-D", "-s", "/bin/bash", "-G", "wheel", "-h", "/home/"+sysName, sysName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("adduser failed: %s: %w", string(out), err)
	}

	// Set password (skip if empty — reconciliation on startup)
	if password != "" {
		cmd = exec.Command("chpasswd")
		cmd.Stdin = strings.NewReader(sysName + ":" + password)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("chpasswd failed: %s: %w", string(out), err)
		}
	}

	homeDir := "/home/" + sysName

	// Create standard XDG directories
	for _, dir := range []string{"Documents", "Downloads", "Pictures", "Music", "Videos", "Desktop"} {
		os.MkdirAll(homeDir+"/"+dir, 0755)
	}

	// Create .vulos directories
	for _, dir := range []string{"data", "db", "sandbox", "apps"} {
		os.MkdirAll(homeDir+"/.vulos/"+dir, 0755)
	}

	s.ensureBashrc(homeDir, sysName)

	// Set ownership
	if u, err := user.Lookup(sysName); err == nil {
		uid, _ := strconv.Atoi(u.Uid)
		gid, _ := strconv.Atoi(u.Gid)
		chownR(homeDir, uid, gid)
	}

	log.Printf("[sysuser] created Linux user %q", sysName)
	return nil
}

// Lookup returns the Linux user for a given username, or nil.
func (s *Service) Lookup(username string) *user.User {
	sysName := sanitizeUsername(username)
	u, err := user.Lookup(sysName)
	if err != nil {
		return nil
	}
	return u
}

// Credential returns syscall credentials for dropping privileges to a user.
// Returns nil if the user doesn't exist or we're not root.
func (s *Service) Credential(username string) (*syscall.SysProcAttr, string) {
	if os.Getuid() != 0 {
		return nil, ""
	}
	u := s.Lookup(username)
	if u == nil {
		return nil, ""
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Uid: uint32(uid),
			Gid: uint32(gid),
		},
	}, u.HomeDir
}

// SetPassword updates the Linux user's password.
func (s *Service) SetPassword(username, password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	sysName := sanitizeUsername(username)
	if sysName == "" || password == "" {
		return nil
	}

	// Only set if user exists
	if _, err := user.Lookup(sysName); err != nil {
		return nil
	}

	cmd := exec.Command("chpasswd")
	cmd.Stdin = strings.NewReader(sysName + ":" + password)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("chpasswd failed: %s: %w", string(out), err)
	}
	return nil
}

// DeleteUser removes a Linux user and their home directory.
func (s *Service) DeleteUser(username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	sysName := sanitizeUsername(username)
	cmd := exec.Command("deluser", "--remove-home", sysName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("deluser failed: %s: %w", string(out), err)
	}
	log.Printf("[sysuser] deleted Linux user %q", sysName)
	return nil
}

func (s *Service) ensureBashrc(homeDir, username string) {
	bashrc := homeDir + "/.bashrc"
	if _, err := os.Stat(bashrc); err == nil {
		return // already exists
	}
	content := fmt.Sprintf(`# Vula OS shell config
export PS1='\[\e[1;32m\]\u\[\e[0m\]@\[\e[1;34m\]vula\[\e[0m\]:\[\e[1;36m\]\w\[\e[0m\]\$ '
export HOSTNAME=vula
alias ls='ls --color=auto'
alias ll='ls -la'
alias la='ls -A'
`)
	os.WriteFile(bashrc, []byte(content), 0644)
	os.WriteFile(homeDir+"/.profile", []byte("[ -f ~/.bashrc ] && . ~/.bashrc\n"), 0644)

	// Fix ownership
	if u, err := user.Lookup(username); err == nil {
		uid, _ := strconv.Atoi(u.Uid)
		gid, _ := strconv.Atoi(u.Gid)
		os.Chown(bashrc, uid, gid)
		os.Chown(homeDir+"/.profile", uid, gid)
	}
}

func sanitizeUsername(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	s := b.String()
	// Linux usernames must start with a letter
	if len(s) > 0 && s[0] >= '0' && s[0] <= '9' {
		s = "u" + s
	}
	if len(s) > 32 {
		s = s[:32]
	}
	return s
}

func chownR(path string, uid, gid int) {
	exec.Command("chown", "-R", fmt.Sprintf("%d:%d", uid, gid), path).Run()
}
