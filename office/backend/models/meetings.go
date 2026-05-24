package models

import "time"

// MeetingStatus represents the lifecycle state of a scheduled meeting.
type MeetingStatus string

const (
	MeetingStatusScheduled MeetingStatus = "scheduled"
	MeetingStatusActive    MeetingStatus = "active"
	MeetingStatusEnded     MeetingStatus = "ended"
	MeetingStatusCancelled MeetingStatus = "cancelled"
)

// Meeting is a named, optionally scheduled video meeting room.
// A meeting maps 1:1 to a fabric session (SessionID) which CallView uses.
// When ScheduledAt is zero the room is a permanent / instant room.
type Meeting struct {
	ID          string        `json:"id"`
	Title       string        `json:"title"`
	SessionID   string        `json:"session_id"` // fabric session / room id fed into createCall
	HostVumail  string        `json:"host_vumail"`
	Invitees    []string      `json:"invitees"`   // Vulos account addresses (@vulos.org)
	ScheduledAt *time.Time    `json:"scheduled_at,omitempty"`
	DurationMin int           `json:"duration_min,omitempty"` // 0 = open-ended
	Status      MeetingStatus `json:"status"`
	JoinLink    string        `json:"join_link"` // /room/<session_id>
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

// MeetingParticipant tracks who has joined a live room (ephemeral, in-memory only).
type MeetingParticipant struct {
	Vumail      string    `json:"vumail"`
	DisplayName string    `json:"display_name"`
	JoinedAt    time.Time `json:"joined_at"`
}

// ---- request / response bodies ----

type CreateMeetingRequest struct {
	Title       string     `json:"title" binding:"required"`
	HostVumail  string     `json:"host_vumail"`
	Invitees    []string   `json:"invitees"`
	ScheduledAt *time.Time `json:"scheduled_at,omitempty"`
	DurationMin int        `json:"duration_min,omitempty"`
}

type UpdateMeetingRequest struct {
	Title       string     `json:"title"`
	Invitees    []string   `json:"invitees"`
	ScheduledAt *time.Time `json:"scheduled_at,omitempty"`
	DurationMin int        `json:"duration_min,omitempty"`
	Status      string     `json:"status,omitempty"`
}

type MeetingJoinResponse struct {
	Meeting   *Meeting `json:"meeting"`
	SessionID string   `json:"session_id"`
	JoinLink  string   `json:"join_link"`
}
