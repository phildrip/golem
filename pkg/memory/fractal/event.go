package fractal

import (
	"fmt"
	"time"
)

// EventKind represents the category of recorded conversation events.
type EventKind string

const (
	KindUser EventKind = "user" // User input or external instruction
	KindTalk EventKind = "talk" // Agent assistant responses
	KindTool EventKind = "tool" // Agent tool invocation call (name + JSON args)
	KindEcho EventKind = "echo" // Tool output result
	KindWork EventKind = "work" // Subagent report or background task output
)

// Event is an append-only record of a single conversational interaction.
type Event struct {
	ID        int       `json:"id"`
	Kind      EventKind `json:"kind"`
	Text      string    `json:"text"`
	Timestamp time.Time `json:"timestamp"`
	Size      int       `json:"size"`                 // UTF-8 byte length of kind + ": " + text
	SessionID string    `json:"session_id,omitempty"` // Session GUID that recorded this event
	TurnID    string    `json:"turn_id,omitempty"`    // Turn / transaction GUID grouping all events in a turn
}

// FormatLine formats an event into a string representation for logging or display.
func (e Event) FormatLine() string {
	return fmt.Sprintf("%s: %s", e.Kind, e.Text)
}

// AppendEventOpts specifies optional overrides and metadata when appending an event.
type AppendEventOpts struct {
	Timestamp time.Time `json:"timestamp,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	TurnID    string    `json:"turn_id,omitempty"`
}
