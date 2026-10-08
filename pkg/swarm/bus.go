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

// LockHandler is a callback invoked when an advisory lock is claimed or released.
type LockHandler func(ev LockEvent)

// defaultDeadLetterCap bounds how many undeliverable messages are retained.
const defaultDeadLetterCap = 256

// Bus manages inter-agent messaging, channel subscriptions, and resource locking.
type Bus struct {
	mu            sync.RWMutex
	agents        map[string]AgentInfo // registered (alive) agents
	mailboxes     map[string]*Mailbox
	subscribers   map[string]map[uint64]MessageHandler // channel -> subID -> listeners
	listeners     map[uint64]MessageHandler            // global bus listeners
	lockListeners map[uint64]LockHandler               // advisory-lock observers
	nextSubID     uint64
	locks         map[string]ResourceLock // resource path -> lock
	deadLetters   []Message               // undeliverable direct messages
	dlqCap        int
	closed        bool
}

// NewBus creates an initialized Bus.
func NewBus() *Bus {
	return &Bus{
		agents:        make(map[string]AgentInfo),
		mailboxes:     make(map[string]*Mailbox),
		subscribers:   make(map[string]map[uint64]MessageHandler),
		listeners:     make(map[uint64]MessageHandler),
		lockListeners: make(map[uint64]LockHandler),
		locks:         make(map[string]ResourceLock),
		dlqCap:        defaultDeadLetterCap,
	}
}

// RegisterAgent marks an agent as alive (and ensures it has a mailbox), so
// broadcast and direct messages can be routed to it. Registration is idempotent.
func (b *Bus) RegisterAgent(agentID, sessionID string) AgentInfo {
	b.mu.Lock()
	defer b.mu.Unlock()

	info, ok := b.agents[agentID]
	if !ok {
		info = AgentInfo{ID: agentID, RegisteredAt: time.Now().UTC()}
	}
	if sessionID != "" {
		info.SessionID = sessionID
	}
	b.agents[agentID] = info

	if _, ok := b.mailboxes[agentID]; !ok {
		b.mailboxes[agentID] = NewMailbox(agentID, 256)
	}
	return info
}

// UnregisterAgent marks an agent offline. It returns true if the agent was
// registered, so callers can use it as a "was alive" probe.
func (b *Bus) UnregisterAgent(agentID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.agents[agentID]; !ok {
		return false
	}
	delete(b.agents, agentID)
	return true
}

// IsRegistered reports whether an agent is currently alive on the bus.
func (b *Bus) IsRegistered(agentID string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, ok := b.agents[agentID]
	return ok
}

// LiveAgents returns a snapshot of the currently registered agents.
func (b *Bus) LiveAgents() []AgentInfo {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]AgentInfo, 0, len(b.agents))
	for _, a := range b.agents {
		out = append(out, a)
	}
	return out
}

// LiveAgentIDs returns the identifiers of the currently registered agents.
func (b *Bus) LiveAgentIDs() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]string, 0, len(b.agents))
	for id := range b.agents {
		out = append(out, id)
	}
	return out
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

// UnreadCount returns the number of unread messages in an agent's mailbox,
// without draining them. Returns 0 if the agent has no mailbox yet.
func (b *Bus) UnreadCount(agentID string) int {
	b.mu.RLock()
	mb, ok := b.mailboxes[agentID]
	b.mu.RUnlock()
	if !ok {
		return 0
	}
	return mb.UnreadCount()
}

// prepare assigns an ID, timestamp, and default channel, returning the
// normalized message. Callers must hold b.mu.
func (b *Bus) stamp(msg *Message) {
	if msg.ID == "" {
		msg.ID = fmt.Sprintf("msg-%d", time.Now().UnixNano())
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now().UTC()
	}
}

// Send routes a point-to-point message to a target agent's mailbox.
//
// If the recipient is not a registered (alive) agent, the message is parked in
// the bounded dead-letter queue and DeliveryDeadLetter is returned instead of
// silently disappearing. A message is still delivered to the mailbox of a
// registered recipient even if no goroutine is currently reading it.
func (b *Bus) Send(msg Message) (DeliveryStatus, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return "", errors.New("swarm bus is closed")
	}
	if msg.Recipient == "" {
		b.mu.Unlock()
		return "", errors.New("recipient required for direct send")
	}
	b.stamp(&msg)

	recipient := msg.Recipient
	mb, hasMailbox := b.mailboxes[recipient]
	_, registered := b.agents[recipient]

	status := DeliveryDelivered
	if !registered {
		status = DeliveryDeadLetter
		b.appendDeadLetterLocked(msg)
	}

	// Always ensure a mailbox so a later check_inbox can still observe the
	// message (backward compatible); registered agents additionally surface it
	// via the inbox nudge.
	if !hasMailbox {
		mb = NewMailbox(recipient, 256)
		b.mailboxes[recipient] = mb
	}

	allListeners := make([]MessageHandler, 0, len(b.listeners))
	for _, l := range b.listeners {
		allListeners = append(allListeners, l)
	}
	b.mu.Unlock()

	mb.Deliver(msg)

	b.notifyListeners(allListeners, msg)

	return status, nil
}

// appendDeadLetterLocked records an undeliverable message. Callers must hold b.mu.
func (b *Bus) appendDeadLetterLocked(msg Message) {
	b.deadLetters = append(b.deadLetters, msg)
	if len(b.deadLetters) > b.dlqCap {
		b.deadLetters[0] = Message{}
		b.deadLetters = b.deadLetters[1:]
	}
}

// DeadLetters returns a snapshot of undeliverable direct messages.
func (b *Bus) DeadLetters() []Message {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]Message, len(b.deadLetters))
	copy(out, b.deadLetters)
	return out
}

// DeadLetterCount returns the number of undeliverable direct messages currently held.
func (b *Bus) DeadLetterCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.deadLetters)
}

// Broadcast publishes a channel message. It is delivered to the mailboxes of
// every registered (alive) agent except the sender, so an agent that is not
// actively subscribed still receives it via check_inbox / its inbox nudge.
// Explicit channel subscribers and global listeners are also notified.
// It returns the number of agent mailboxes that received the message.
func (b *Bus) Broadcast(msg Message) (int, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return 0, errors.New("swarm bus is closed")
	}
	b.stamp(&msg)
	if msg.Channel == "" {
		msg.Channel = "general"
	}

	// Collect recipient mailboxes for all live agents except the sender.
	type delivery struct {
		id string
		mb *Mailbox
	}
	var deliveries []delivery
	for id := range b.agents {
		if id == msg.Sender {
			continue
		}
		mb, ok := b.mailboxes[id]
		if !ok {
			mb = NewMailbox(id, 256)
			b.mailboxes[id] = mb
		}
		deliveries = append(deliveries, delivery{id: id, mb: mb})
	}

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

	for _, d := range deliveries {
		d.mb.Deliver(msg)
	}

	for _, h := range channelHandlers {
		safeCall(h, msg)
	}

	b.notifyListeners(allListeners, msg)

	return len(deliveries), nil
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

// OnLockChange registers an observer for advisory-lock claim/release events.
// Returns an unsubscribe function.
func (b *Bus) OnLockChange(handler LockHandler) func() {
	b.mu.Lock()
	defer b.mu.Unlock()

	subID := b.nextSubID
	b.nextSubID++
	b.lockListeners[subID] = handler

	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.lockListeners, subID)
	}
}

// notifyListeners dispatches a message to global listeners asynchronously and
// non-blockingly with panic protection.
func (b *Bus) notifyListeners(listeners []MessageHandler, msg Message) {
	if len(listeners) == 0 {
		return
	}
	go func(ls []MessageHandler, m Message) {
		for _, l := range ls {
			safeCall(l, m)
		}
	}(listeners, msg)
}

// notifyLockChange dispatches a lock event to observers in order. Lock
// operations are infrequent and observers are expected to be non-blocking
// (e.g. an enqueue to the WebSocket hub), so dispatch is synchronous to
// preserve claim/release ordering for UIs and tests.
// Callers must NOT hold b.mu when calling this.
func (b *Bus) notifyLockChange(ev LockEvent) {
	b.mu.RLock()
	listeners := make([]LockHandler, 0, len(b.lockListeners))
	for _, l := range b.lockListeners {
		listeners = append(listeners, l)
	}
	b.mu.RUnlock()

	for _, l := range listeners {
		func() {
			defer func() { _ = recover() }()
			l(ev)
		}()
	}
}

// safeCall invokes a message handler, recovering from panics so one bad
// listener cannot take down the bus.
func safeCall(h MessageHandler, msg Message) {
	defer func() { _ = recover() }()
	h(msg)
}

// ClaimResource acquires an advisory lock on a file or resource for an agent.
func (b *Bus) ClaimResource(ctx context.Context, resource string, owner string, sessionID string, ttl time.Duration) (bool, error) {
	b.mu.Lock()

	if b.closed {
		b.mu.Unlock()
		return false, errors.New("swarm bus is closed")
	}

	now := time.Now().UTC()
	if existing, ok := b.locks[resource]; ok {
		// Check if expired
		if !existing.ExpiresAt.IsZero() && now.After(existing.ExpiresAt) {
			delete(b.locks, resource)
		} else if existing.Owner != owner {
			b.mu.Unlock()
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
	b.mu.Unlock()

	b.notifyLockChange(LockEvent{Type: LockClaimed, Lock: lock})

	return true, nil
}

// ReleaseResource releases an advisory lock held by the owner.
func (b *Bus) ReleaseResource(resource string, owner string) error {
	b.mu.Lock()

	if b.closed {
		b.mu.Unlock()
		return errors.New("swarm bus is closed")
	}

	existing, ok := b.locks[resource]
	if !ok {
		b.mu.Unlock()
		return nil
	}
	if existing.Owner != owner {
		b.mu.Unlock()
		return errors.Errorf("cannot release lock for %q: owned by %q", resource, existing.Owner)
	}

	delete(b.locks, resource)
	b.mu.Unlock()

	b.notifyLockChange(LockEvent{Type: LockReleased, Lock: existing})

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
	b.agents = make(map[string]AgentInfo)
	b.mailboxes = make(map[string]*Mailbox)
	b.subscribers = make(map[string]map[uint64]MessageHandler)
	b.listeners = make(map[uint64]MessageHandler)
	b.lockListeners = make(map[uint64]LockHandler)
	b.locks = make(map[string]ResourceLock)
	b.deadLetters = nil
	return nil
}
