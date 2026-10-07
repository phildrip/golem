package swarm

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/pkg/errors"
)

// MessageHandler is a callback invoked when a message is published to a channel or mailbox.
type MessageHandler func(msg Message)

// Bus manages inter-agent messaging, channel subscriptions, and resource locking.
type Bus struct {
	mu          sync.RWMutex
	mailboxes   map[string]*Mailbox
	subscribers map[string]map[uint64]MessageHandler // channel -> subID -> listeners
	listeners   map[uint64]MessageHandler            // global bus listeners
	nextSubID   uint64
	locks       map[string]ResourceLock              // resource path -> lock
	closed      bool
}

// NewBus creates an initialized Bus.
func NewBus() *Bus {
	return &Bus{
		mailboxes:   make(map[string]*Mailbox),
		subscribers: make(map[string]map[uint64]MessageHandler),
		listeners:   make(map[uint64]MessageHandler),
		locks:       make(map[string]ResourceLock),
	}
}

// GetOrCreateMailbox gets or initializes a mailbox for the specified agent.
func (b *Bus) GetOrCreateMailbox(agentID string) *Mailbox {
	b.mu.Lock()
	defer b.mu.Unlock()

	if mb, ok := b.mailboxes[agentID]; ok {
		return mb
	}
	mb := NewMailbox(agentID, 256)
	b.mailboxes[agentID] = mb
	return mb
}

// Send sends a point-to-point message directly to a target agent's mailbox.
func (b *Bus) Send(msg Message) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return errors.New("swarm bus is closed")
	}

	if msg.ID == "" {
		msg.ID = fmt.Sprintf("msg-%d", time.Now().UnixNano())
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now().UTC()
	}

	recipient := msg.Recipient
	if recipient == "" {
		b.mu.Unlock()
		return errors.New("recipient required for direct send")
	}

	mb, ok := b.mailboxes[recipient]
	if !ok {
		mb = NewMailbox(recipient, 256)
		b.mailboxes[recipient] = mb
	}

	// Capture global listeners
	allListeners := make([]MessageHandler, 0, len(b.listeners))
	for _, l := range b.listeners {
		allListeners = append(allListeners, l)
	}
	b.mu.Unlock()

	// Deliver to mailbox
	mb.Deliver(msg)

	// Notify global listeners asynchronously and non-blockingly with panic protection
	if len(allListeners) > 0 {
		go func(listeners []MessageHandler, m Message) {
			for _, l := range listeners {
				func() {
					defer func() { _ = recover() }()
					l(m)
				}()
			}
		}(allListeners, msg)
	}

	return nil
}

// Broadcast publishes a message to a channel topic (e.g. "discoveries", "warnings").
func (b *Bus) Broadcast(msg Message) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return errors.New("swarm bus is closed")
	}

	if msg.ID == "" {
		msg.ID = fmt.Sprintf("msg-%d", time.Now().UnixNano())
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now().UTC()
	}
	if msg.Channel == "" {
		msg.Channel = "general"
	}

	// Capture topic subscribers & global listeners
	subs := b.subscribers[msg.Channel]
	channelHandlers := make([]MessageHandler, 0, len(subs))
	for _, h := range subs {
		channelHandlers = append(channelHandlers, h)
	}

	allListeners := make([]MessageHandler, 0, len(b.listeners))
	for _, l := range b.listeners {
		allListeners = append(allListeners, l)
	}
	b.mu.Unlock()

	for _, h := range channelHandlers {
		func() {
			defer func() { _ = recover() }()
			h(msg)
		}()
	}

	if len(allListeners) > 0 {
		go func(listeners []MessageHandler, m Message) {
			for _, l := range listeners {
				func() {
					defer func() { _ = recover() }()
					l(m)
				}()
			}
		}(allListeners, msg)
	}

	return nil
}

// Subscribe registers a listener on a specific channel topic.
// Returns an unsubscribe function.
func (b *Bus) Subscribe(channel string, handler MessageHandler) func() {
	b.mu.Lock()
	defer b.mu.Unlock()

	subID := b.nextSubID
	b.nextSubID++
	if b.subscribers[channel] == nil {
		b.subscribers[channel] = make(map[uint64]MessageHandler)
	}
	b.subscribers[channel][subID] = handler

	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if subs, ok := b.subscribers[channel]; ok {
			delete(subs, subID)
			if len(subs) == 0 {
				delete(b.subscribers, channel)
			}
		}
	}
}

// OnMessage registers a global observer on all messages flowing through the bus.
// Returns an unsubscribe function.
func (b *Bus) OnMessage(handler MessageHandler) func() {
	b.mu.Lock()
	defer b.mu.Unlock()

	subID := b.nextSubID
	b.nextSubID++
	b.listeners[subID] = handler

	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.listeners, subID)
	}
}

// ClaimResource acquires an advisory lock on a file or resource for an agent.
func (b *Bus) ClaimResource(ctx context.Context, resource string, owner string, sessionID string, ttl time.Duration) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return false, errors.New("swarm bus is closed")
	}

	now := time.Now().UTC()
	if existing, ok := b.locks[resource]; ok {
		// Check if expired
		if !existing.ExpiresAt.IsZero() && now.After(existing.ExpiresAt) {
			delete(b.locks, resource)
		} else if existing.Owner != owner {
			return false, errors.Errorf("resource %q is locked by agent %q", resource, existing.Owner)
		}
	}

	var expires time.Time
	if ttl > 0 {
		expires = now.Add(ttl)
	}

	lock := ResourceLock{
		Resource:   resource,
		Owner:      owner,
		SessionID:  sessionID,
		AcquiredAt: now,
		ExpiresAt:  expires,
	}
	b.locks[resource] = lock

	return true, nil
}

// ReleaseResource releases an advisory lock held by the owner.
func (b *Bus) ReleaseResource(resource string, owner string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return errors.New("swarm bus is closed")
	}

	existing, ok := b.locks[resource]
	if !ok {
		return nil
	}
	if existing.Owner != owner {
		return errors.Errorf("cannot release lock for %q: owned by %q", resource, existing.Owner)
	}

	delete(b.locks, resource)
	return nil
}

// ListLocks returns a snapshot of all active locks.
func (b *Bus) ListLocks() []ResourceLock {
	b.mu.RLock()
	defer b.mu.RUnlock()

	now := time.Now().UTC()
	list := make([]ResourceLock, 0, len(b.locks))
	for _, l := range b.locks {
		if l.ExpiresAt.IsZero() || now.Before(l.ExpiresAt) {
			list = append(list, l)
		}
	}
	return list
}

// Close tears down the bus and mailboxes.
func (b *Bus) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	b.mailboxes = make(map[string]*Mailbox)
	b.subscribers = make(map[string]map[uint64]MessageHandler)
	b.listeners = make(map[uint64]MessageHandler)
	b.locks = make(map[string]ResourceLock)
	return nil
}
