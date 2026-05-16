package ai

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Mission is a multi-step task the AI is working on for the user.
// The AI breaks a complex intent into steps, tracks progress, and
// reports back as each step completes.
type Mission struct {
	ID          string        `json:"id"`
	UserID      string        `json:"user_id"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Status      MissionStatus `json:"status"`
	Steps       []MissionStep `json:"steps"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

type MissionStatus string

const (
	MissionPending   MissionStatus = "pending"
	MissionRunning   MissionStatus = "running"
	MissionCompleted MissionStatus = "completed"
	MissionFailed    MissionStatus = "failed"
	MissionCancelled MissionStatus = "cancelled"
)

type MissionStep struct {
	ID       string        `json:"id"`
	Label    string        `json:"label"`
	Status   MissionStatus `json:"status"`
	Output   string        `json:"output,omitempty"`
	Error    string        `json:"error,omitempty"`
	Duration string        `json:"duration,omitempty"`
}

// MissionStore persists missions.
type MissionStore struct {
	mu       sync.RWMutex
	missions map[string]*Mission // missionID → Mission
	path     string
}

func NewMissionStore(dataDir string) *MissionStore {
	p := filepath.Join(dataDir, "missions.json")
	ms := &MissionStore{
		missions: make(map[string]*Mission),
		path:     p,
	}
	if data, err := os.ReadFile(p); err == nil {
		var list []*Mission
		json.Unmarshal(data, &list)
		for _, m := range list {
			ms.missions[m.ID] = m
		}
	}
	return ms
}

// Create starts a new mission.
func (ms *MissionStore) Create(userID, title, description string, steps []string) *Mission {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	id := time.Now().Format("20060102150405.000")
	m := &Mission{
		ID:          id,
		UserID:      userID,
		Title:       title,
		Description: description,
		Status:      MissionPending,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	for i, label := range steps {
		m.Steps = append(m.Steps, MissionStep{
			ID:     id + "-" + string(rune('a'+i)),
			Label:  label,
			Status: MissionPending,
		})
	}
	ms.missions[id] = m
	return m
}

// UpdateStep updates a step's status and output.
func (ms *MissionStore) UpdateStep(missionID, stepID string, status MissionStatus, output string) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	m, ok := ms.missions[missionID]
	if !ok {
		return
	}
	for i := range m.Steps {
		if m.Steps[i].ID == stepID {
			m.Steps[i].Status = status
			if output != "" {
				m.Steps[i].Output = output
			}
			break
		}
	}
	// Check if all steps are done
	allDone := true
	anyFailed := false
	for _, s := range m.Steps {
		if s.Status == MissionPending || s.Status == MissionRunning {
			allDone = false
		}
		if s.Status == MissionFailed {
			anyFailed = true
		}
	}
	if allDone {
		if anyFailed {
			m.Status = MissionFailed
		} else {
			m.Status = MissionCompleted
		}
	} else {
		m.Status = MissionRunning
	}
	m.UpdatedAt = time.Now()
}

// Cancel cancels a mission.
func (ms *MissionStore) Cancel(missionID string) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if m, ok := ms.missions[missionID]; ok {
		m.Status = MissionCancelled
		for i := range m.Steps {
			if m.Steps[i].Status == MissionPending || m.Steps[i].Status == MissionRunning {
				m.Steps[i].Status = MissionCancelled
			}
		}
		m.UpdatedAt = time.Now()
	}
}

// Get returns a mission.
func (ms *MissionStore) Get(missionID string) *Mission {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.missions[missionID]
}

// ListForUser returns missions for a user, newest first.
func (ms *MissionStore) ListForUser(userID string, limit int) []*Mission {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	var result []*Mission
	for _, m := range ms.missions {
		if m.UserID == userID {
			result = append(result, m)
		}
	}
	// Sort newest first
	for i := 0; i < len(result); i++ {
		for j := i + 1; j < len(result); j++ {
			if result[j].CreatedAt.After(result[i].CreatedAt) {
				result[i], result[j] = result[j], result[i]
			}
		}
	}
	if limit > 0 && limit < len(result) {
		result = result[:limit]
	}
	return result
}

// ActiveCount returns number of running missions for a user.
func (ms *MissionStore) ActiveCount(userID string) int {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	count := 0
	for _, m := range ms.missions {
		if m.UserID == userID && m.Status == MissionRunning {
			count++
		}
	}
	return count
}

// Flush persists to disk.
func (ms *MissionStore) Flush() error {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	var list []*Mission
	for _, m := range ms.missions {
		list = append(list, m)
	}
	data, _ := json.Marshal(list)
	tmp := ms.path + ".tmp"
	os.WriteFile(tmp, data, 0600)
	return os.Rename(tmp, ms.path)
}
