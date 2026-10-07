package swarm

import (
	"time"
)

// Priority indicates message urgency.
type Priority string

const (
	PriorityNormal Priority = "normal"
	PriorityUrgent Priority = "urgent"
)

// Message represents an inter-agent or broadcast communication within the swarm.
type Message struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id,omitempty"`
	Sender    string    `json:"sender"`
	Recipient string    `json:"recipient,omitempty"` // empty recipient indicates a channel broadcast
	Channel   string    `json:"channel,omitempty"`   // e.g. "discoveries", "warnings", "locks"
	Subject   string    `json:"subject,omitempty"`
	Body      string    `json:"body"`
	Priority  Priority  `json:"priority,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// ResourceLock represents an advisory lock on a file, directory, or shared resource.
type ResourceLock struct {
	Resource  string    `json:"resource"`
	Owner     string    `json:"owner"`
	SessionID string    `json:"session_id,omitempty"`
	AcquiredAt time.Time `json:"acquired_at"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}
