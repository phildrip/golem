package server

import (
	"encoding/json"
	"time"

	"github.com/user/golem/pkg/config"
	"github.com/user/golem/pkg/runtime"
)

// WSEventType distinguishes realtime WebSocket events streamed to UI clients.
type WSEventType string

const (
	// Session lifecycle
	WSEventSessionCreated WSEventType = "session_created"
	WSEventSessionDeleted WSEventType = "session_deleted"
	WSEventSessionActive  WSEventType = "session_active"

	// Message and streaming
	WSEventUserPrompt WSEventType = "user_prompt"
	WSEventAgentChunk WSEventType = "agent_chunk"
	WSEventAgentDone  WSEventType = "agent_done"

	// Tool execution
	WSEventToolStart WSEventType = "tool_start"
	WSEventToolDone  WSEventType = "tool_done"

	// Swarm / Subagent lifecycle
	WSEventSubagentStart WSEventType = "subagent_start"
	WSEventSubagentTool  WSEventType = "subagent_tool"
	WSEventSubagentDone  WSEventType = "subagent_done"
	WSEventSwarmMessage  WSEventType = "swarm_message"
	WSEventSwarmLock     WSEventType = "swarm_lock"

	// Memory synchronization
	WSEventMemoryAdded WSEventType = "memory_added"

	// Patch proposals
	WSEventPatchProposed WSEventType = "patch_proposed"
	WSEventPatchApplied  WSEventType = "patch_applied"

	// Profile configuration
	WSEventProfileChanged WSEventType = "profile_changed"

	// Human-in-the-Loop sandbox confirmation
	WSEventApprovalRequest WSEventType = "approval_request"

	// Stream resume fallback when requested sequence aged out of ring buffer
	WSEventResumeFailed WSEventType = "resume_failed"
)

// WSEvent is the standard JSON envelope transmitted over WebSockets.
type WSEvent struct {
	Type      WSEventType     `json:"type"`
	SessionID string          `json:"session_id,omitempty"`
	Seq       uint64          `json:"seq,omitempty"`
	Timestamp time.Time       `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

// SessionInfo provides metadata for a managed agent session.
type SessionInfo = runtime.SessionInfo

// CreateSessionRequest is the payload for creating a new session.
type CreateSessionRequest struct {
	Title string `json:"title,omitempty"`
}

// SendMessageRequest is the payload for submitting a prompt to a session.
type SendMessageRequest struct {
	Prompt string `json:"prompt"`
}

// ApprovalRequest is the answer to a sandbox confirmation prompt: the call the
// user was shown and their decision.
type ApprovalRequest struct {
	CallID   string `json:"call_id"`
	Approved bool   `json:"approved"`
}

// PatchRequest defines a hashline patch execution request via REST.
type PatchRequest struct {
	FilePath string `json:"file_path"`
	Op       string `json:"op"`
	Anchor   string `json:"anchor"`
	Content  string `json:"content,omitempty"`
	DryRun   bool   `json:"dry_run,omitempty"`
}

// ProfilesResponse is the payload returned by GET /api/profiles.
type ProfilesResponse struct {
	ActiveProfile       string                          `json:"active_profile"`
	Profiles            map[string]config.ProfileConfig `json:"profiles"`
	UserConfigPath      string                          `json:"user_config_path,omitempty"`
	WorkspaceConfigPath string                          `json:"workspace_config_path,omitempty"`
}

// SaveProfileRequest is the payload for creating or updating a profile.
type SaveProfileRequest struct {
	Name      string                  `json:"name"`
	Scope     string                  `json:"scope,omitempty"` // "workspace" or "user"
	Provider  string                  `json:"provider,omitempty"`
	Model     string                  `json:"model,omitempty"`
	BaseURL   string                  `json:"base_url,omitempty"`
	APIKey    string                  `json:"api_key,omitempty"`
	APIKeyEnv string                  `json:"api_key_env,omitempty"`
	MemoryDir string                  `json:"memory_dir,omitempty"`
	Compactor *config.CompactorConfig `json:"compactor,omitempty"`
	SetActive bool                    `json:"set_active,omitempty"`
}

// SetActiveProfileRequest is the payload for switching the active profile.
type SetActiveProfileRequest struct {
	Name  string `json:"name"`
	Scope string `json:"scope,omitempty"` // "workspace" or "user"
}

// ModelsResponse is the payload returned by GET /api/models.
type ModelsResponse struct {
	Provider string   `json:"provider"`
	Models   []string `json:"models"`
}
