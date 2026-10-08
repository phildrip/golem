package swarm

import (
	"time"
)

// OrchestratorID is the well-known identity of the top-level Golem agent on the bus.
const OrchestratorID = "orchestrator"

// Priority indicates message urgency.
type Priority string

const (
	PriorityNormal Priority = "normal"
	PriorityUrgent Priority = "urgent"
)

// Message represents an inter-agent or broadcast communication within the swarm.
type Message struct {
	ID        string            `json:"id"`
	SessionID string            `json:"session_id,omitempty"`
	Sender    string            `json:"sender"`
	Recipient string            `json:"recipient,omitempty"` // empty recipient indicates a channel broadcast
	Channel   string            `json:"channel,omitempty"`   // e.g. "discoveries", "warnings", "locks"
	Subject   string            `json:"subject,omitempty"`
	Body      string            `json:"body"`
	Priority  Priority          `json:"priority,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// AgentInfo describes an agent currently registered (alive) on the swarm bus.
type AgentInfo struct {
	ID           string    `json:"id"`
	SessionID    string    `json:"session_id,omitempty"`
	RegisteredAt time.Time `json:"registered_at"`
}

// DeliveryStatus describes the outcome of routing a direct message.
type DeliveryStatus string

const (
	// DeliveryDelivered means the recipient was registered and the message landed in its mailbox.
	DeliveryDelivered DeliveryStatus = "delivered"
	// DeliveryDeadLetter means the recipient was unknown/offline: the message was parked
	// in the dead-letter queue (and still delivered to a mailbox) instead of vanishing.
	DeliveryDeadLetter DeliveryStatus = "dead_letter"
)

// ResourceLock represents an advisory lock on a file, directory, or shared resource.
type ResourceLock struct {
	Resource   string    `json:"resource"`
	Owner      string    `json:"owner"`
	SessionID  string    `json:"session_id,omitempty"`
	AcquiredAt time.Time `json:"acquired_at"`
	ExpiresAt  time.Time `json:"expires_at,omitempty"`
}

// LockEventType distinguishes advisory-lock lifecycle transitions.
type LockEventType string

const (
	LockClaimed  LockEventType = "claimed"
	LockReleased LockEventType = "released"
)

// LockEvent is emitted when an advisory lock is claimed or released so UIs and
// observers can render swarm lock state in realtime.
type LockEvent struct {
	Type LockEventType `json:"type"`
	Lock ResourceLock  `json:"lock"`
}
