package server

import (
	"encoding/json"
	"time"
)

// WSEventType distinguishes realtime WebSocket events streamed to UI clients.
type WSEventType string

const (
	// Session lifecycle
	WSEventSessionCreated WSEventType = "session_created"
	WSEventSessionDeleted WSEventType = "session_deleted"
	WSEventSessionActive  WSEventType = "session_active"

	// Message and streaming
	WSEventUserPrompt     WSEventType = "user_prompt"
	WSEventAgentChunk     WSEventType = "agent_chunk"
	WSEventAgentDone      WSEventType = "agent_done"

	// Tool execution
	WSEventToolStart      WSEventType = "tool_start"
	WSEventToolDone       WSEventType = "tool_done"

	// Swarm / Subagent lifecycle
	WSEventSubagentStart  WSEventType = "subagent_start"
	WSEventSubagentTool   WSEventType = "subagent_tool"
	WSEventSubagentDone   WSEventType = "subagent_done"
	WSEventSwarmMessage   WSEventType = "swarm_message"
	WSEventSwarmLock      WSEventType = "swarm_lock"

	// Memory synchronization
	WSEventMemoryAdded    WSEventType = "memory_added"

	// Patch proposals
	WSEventPatchProposed  WSEventType = "patch_proposed"
	WSEventPatchApplied   WSEventType = "patch_applied"
)

// WSEvent is the standard JSON envelope transmitted over WebSockets.
type WSEvent struct {
	Type      WSEventType     `json:"type"`
	SessionID string          `json:"session_id,omitempty"`
	Timestamp time.Time       `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

// SessionInfo provides metadata for a managed agent session.
type SessionInfo struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	IsActive  bool      `json:"is_active"`
	TurnCount int       `json:"turn_count"`
}

// CreateSessionRequest is the payload for creating a new session.
type CreateSessionRequest struct {
	Title string `json:"title,omitempty"`
}

// SendMessageRequest is the payload for submitting a prompt to a session.
type SendMessageRequest struct {
	Prompt string `json:"prompt"`
}

// PatchRequest defines a hashline patch execution request via REST.
type PatchRequest struct {
	FilePath string `json:"file_path"`
	Op       string `json:"op"`
	Anchor   string `json:"anchor"`
	Content  string `json:"content,omitempty"`
	DryRun   bool   `json:"dry_run,omitempty"`
}
