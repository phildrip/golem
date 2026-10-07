package swarm

import (
	"sync"
)

// Mailbox holds queued messages for a specific agent.
type Mailbox struct {
	agentID string
	mu      sync.RWMutex
	unread  []Message
	history []Message
	maxSize int
}

// NewMailbox creates a new Mailbox for an agent.
func NewMailbox(agentID string, maxSize int) *Mailbox {
	if maxSize <= 0 {
		maxSize = 256
	}
	return &Mailbox{
		agentID: agentID,
		unread:  make([]Message, 0),
		history: make([]Message, 0),
		maxSize: maxSize,
	}
}

// Deliver appends a message to the inbox.
func (m *Mailbox) Deliver(msg Message) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.unread = append(m.unread, msg)
	if len(m.unread) > m.maxSize {
		// Drop oldest unread if buffer is saturated, clearing element to avoid memory leak
		m.unread[0] = Message{}
		m.unread = m.unread[1:]
	}

	m.history = append(m.history, msg)
	if len(m.history) > m.maxSize*2 {
		m.history[0] = Message{}
		m.history = m.history[1:]
	}
}

// ReadUnread drains and returns all unread messages.
func (m *Mailbox) ReadUnread() []Message {
	m.mu.Lock()
	defer m.mu.Unlock()

	msgs := make([]Message, len(m.unread))
	copy(msgs, m.unread)
	for i := range m.unread {
		m.unread[i] = Message{}
	}
	m.unread = m.unread[:0]
	return msgs
}

// Peek returns recent history without draining unread messages.
func (m *Mailbox) Peek(limit int) []Message {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if limit <= 0 || limit > len(m.history) {
		limit = len(m.history)
	}
	start := len(m.history) - limit
	out := make([]Message, limit)
	copy(out, m.history[start:])
	return out
}

// UnreadCount returns the number of pending unread messages.
func (m *Mailbox) UnreadCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.unread)
}
