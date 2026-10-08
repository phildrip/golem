package swarm

import (
	"fmt"
	"time"

	"github.com/pkg/errors"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// SendMessageArgs defines input for sending a direct message.
type SendMessageArgs struct {
	Recipient string `json:"recipient" jsonschema:"The target agent identifier (e.g. 'subagent-coder', 'orchestrator')"`
	Subject   string `json:"subject,omitempty" jsonschema:"Optional subject or summary of the message"`
	Body      string `json:"body" jsonschema:"Content of the message to deliver"`
	Priority  string `json:"priority,omitempty" jsonschema:"Message priority: 'normal' (default) or 'urgent'"`
}

// BroadcastMessageArgs defines input for broadcasting to a topic.
type BroadcastMessageArgs struct {
	Channel string `json:"channel" jsonschema:"Topic channel name (e.g. 'discoveries', 'warnings', 'general')"`
	Subject string `json:"subject,omitempty" jsonschema:"Optional subject or summary"`
	Body    string `json:"body" jsonschema:"Content to broadcast to all agents listening on this channel"`
}

// CheckInboxArgs defines input for reading the agent's inbox.
type CheckInboxArgs struct {
	Drain *bool `json:"drain,omitempty" jsonschema:"If true, marks retrieved messages as read and empties the unread inbox. Defaults to true."`
	Limit int   `json:"limit,omitempty" jsonschema:"Maximum number of messages to retrieve when peek is enabled (drain=false). Defaults to 20."`
}

// ClaimResourceArgs defines input for acquiring an advisory lock on a file or resource.
type ClaimResourceArgs struct {
	Resource string `json:"resource" jsonschema:"File path or resource identifier to claim (e.g. 'pkg/server/server.go')"`
	TTLSecs  int    `json:"ttl_secs,omitempty" jsonschema:"Time to hold lock in seconds. Defaults to 60 seconds."`
}

// ReleaseResourceArgs defines input for releasing a lock.
type ReleaseResourceArgs struct {
	Resource string `json:"resource" jsonschema:"File path or resource identifier to release"`
}

// ListLocksArgs defines input for listing active resource locks.
type ListLocksArgs struct{}

// Toolset wraps the Swarm Bus into ADK Tools bound to a specific agent's identity.
type Toolset struct {
	bus        *Bus
	agentID    string
	sessionID  string
	registered bool
}

// NewToolset creates a Swarm Toolset for an agent without registering it on the
// bus. The agent will not receive broadcasts until Register is called.
func NewToolset(bus *Bus, agentID string) *Toolset {
	return &Toolset{
		bus:     bus,
		agentID: agentID,
	}
}

// NewToolsetWithSession creates a Toolset for an agent, registers the agent as
// alive on the bus under the given session, and returns it. Call Close (or
// Unregister) when the agent finishes so peers see it go offline.
func NewToolsetWithSession(bus *Bus, agentID, sessionID string) *Toolset {
	ts := NewToolset(bus, agentID)
	ts.sessionID = sessionID
	ts.Register()
	return ts
}

// AgentID returns the identity this toolset acts as.
func (ts *Toolset) AgentID() string { return ts.agentID }

// Register marks the toolset's agent alive and ensures it has a mailbox.
func (ts *Toolset) Register() {
	if ts.bus == nil {
		return
	}
	ts.bus.RegisterAgent(ts.agentID, ts.sessionID)
	ts.registered = true
}

// Unregister marks the toolset's agent offline. Safe to call multiple times.
func (ts *Toolset) Unregister() {
	if ts.bus == nil || !ts.registered {
		return
	}
	ts.bus.UnregisterAgent(ts.agentID)
	ts.registered = false
}

// BuildTools constructs the collection of tools for the agent.
func (ts *Toolset) BuildTools() ([]tool.Tool, error) {
	var tools []tool.Tool

	// 1. send_message
	sendTool, err := functiontool.New(functiontool.Config{
		Name:        "send_message",
		Description: "Sends a direct point-to-point message to another agent or subagent's mailbox.",
	}, func(ctx agent.Context, args SendMessageArgs) (string, error) {
		priority := PriorityNormal
		if args.Priority == string(PriorityUrgent) {
			priority = PriorityUrgent
		}
		msg := Message{
			SessionID: ts.sessionID,
			Sender:    ts.agentID,
			Recipient: args.Recipient,
			Subject:   args.Subject,
			Body:      args.Body,
			Priority:  priority,
		}
		status, err := ts.bus.Send(msg)
		if err != nil {
			return "", errors.Wrap(err, "sending swarm message")
		}
		if status == DeliveryDeadLetter {
			return fmt.Sprintf("Message to %q could NOT be delivered (agent not registered/live). It was parked in the dead-letter queue; registered agents are: %v.", args.Recipient, ts.bus.LiveAgentIDs()), nil
		}
		return fmt.Sprintf("Message sent to %s successfully.", args.Recipient), nil
	})
	if err != nil {
		return nil, err
	}
	tools = append(tools, sendTool)

	// 2. broadcast_message
	broadcastTool, err := functiontool.New(functiontool.Config{
		Name:        "broadcast_message",
		Description: "Broadcasts a message to an entire topic channel (e.g. 'discoveries', 'warnings', 'general') for all agents to see.",
	}, func(ctx agent.Context, args BroadcastMessageArgs) (string, error) {
		msg := Message{
			SessionID: ts.sessionID,
			Sender:    ts.agentID,
			Channel:   args.Channel,
			Subject:   args.Subject,
			Body:      args.Body,
		}
		n, err := ts.bus.Broadcast(msg)
		if err != nil {
			return "", errors.Wrap(err, "broadcasting swarm message")
		}
		channel := args.Channel
		if channel == "" {
			channel = "general"
		}
		return fmt.Sprintf("Message broadcasted to channel #%s (delivered to %d live agent inbox(es)).", channel, n), nil
	})
	if err != nil {
		return nil, err
	}
	tools = append(tools, broadcastTool)

	// 3. check_inbox
	inboxTool, err := functiontool.New(functiontool.Config{
		Name:        "check_inbox",
		Description: "Checks the agent's mailbox for incoming messages from peer agents or subagents.",
	}, func(ctx agent.Context, args CheckInboxArgs) ([]Message, error) {
		mb := ts.bus.GetOrCreateMailbox(ts.agentID)
		drain := true
		if args.Drain != nil {
			drain = *args.Drain
		}
		if drain {
			return mb.ReadUnread(), nil
		}
		limit := args.Limit
		if limit <= 0 {
			limit = 20
		}
		return mb.Peek(limit), nil
	})
	if err != nil {
		return nil, err
	}
	tools = append(tools, inboxTool)

	// 4. claim_resource
	claimTool, err := functiontool.New(functiontool.Config{
		Name:        "claim_resource",
		Description: "Acquires an advisory lock on a file or shared resource to avoid conflicting edits with other swarm agents.",
	}, func(ctx agent.Context, args ClaimResourceArgs) (string, error) {
		ttl := 60 * time.Second
		if args.TTLSecs > 0 {
			ttl = time.Duration(args.TTLSecs) * time.Second
		}
		ok, err := ts.bus.ClaimResource(ctx, args.Resource, ts.agentID, ts.sessionID, ttl)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("could not acquire lock for %s", args.Resource)
		}
		return fmt.Sprintf("Resource lock acquired on %s for %v.", args.Resource, ttl), nil
	})
	if err != nil {
		return nil, err
	}
	tools = append(tools, claimTool)

	// 5. release_resource
	releaseTool, err := functiontool.New(functiontool.Config{
		Name:        "release_resource",
		Description: "Releases a previously claimed advisory lock on a file or resource.",
	}, func(ctx agent.Context, args ReleaseResourceArgs) (string, error) {
		if err := ts.bus.ReleaseResource(args.Resource, ts.agentID); err != nil {
			return "", err
		}
		return fmt.Sprintf("Released lock on %s.", args.Resource), nil
	})
	if err != nil {
		return nil, err
	}
	tools = append(tools, releaseTool)

	// 6. list_locks
	listLocksTool, err := functiontool.New(functiontool.Config{
		Name:        "list_locks",
		Description: "Lists all currently active advisory resource locks held across the swarm.",
	}, func(ctx agent.Context, args ListLocksArgs) ([]ResourceLock, error) {
		return ts.bus.ListLocks(), nil
	})
	if err != nil {
		return nil, err
	}
	tools = append(tools, listLocksTool)

	return tools, nil
}
