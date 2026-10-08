package swarm

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestSwarmMailbox(t *testing.T) {
	mb := NewMailbox("agent-1", 5)

	if mb.UnreadCount() != 0 {
		t.Fatalf("expected 0 unread messages, got %d", mb.UnreadCount())
	}

	// Deliver messages
	for i := 1; i <= 3; i++ {
		mb.Deliver(Message{
			ID:     string(rune('0' + i)),
			Body:   "test message",
			Sender: "agent-0",
		})
	}

	if mb.UnreadCount() != 3 {
		t.Fatalf("expected 3 unread, got %d", mb.UnreadCount())
	}

	// Read and drain
	unread := mb.ReadUnread()
	if len(unread) != 3 {
		t.Fatalf("expected 3 drained messages, got %d", len(unread))
	}
	if mb.UnreadCount() != 0 {
		t.Fatalf("expected 0 unread after drain, got %d", mb.UnreadCount())
	}

	// History is preserved
	peeked := mb.Peek(10)
	if len(peeked) != 3 {
		t.Fatalf("expected 3 history messages, got %d", len(peeked))
	}
}

func TestSwarmBusSendAndBroadcast(t *testing.T) {
	bus := NewBus()
	defer bus.Close()

	// Register the recipient so the direct send counts as delivered.
	bus.RegisterAgent("agent-tester", "sess-1")

	// 1. Direct P2P send
	msg := Message{
		Sender:    "agent-coder",
		Recipient: "agent-tester",
		Subject:   "Code ready for testing",
		Body:      "Please test pkg/server",
	}

	status, err := bus.Send(msg)
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if status != DeliveryDelivered {
		t.Fatalf("expected DeliveryDelivered, got %q", status)
	}

	testerBox := bus.GetOrCreateMailbox("agent-tester")
	if testerBox.UnreadCount() != 1 {
		t.Fatalf("expected 1 unread message in tester mailbox, got %d", testerBox.UnreadCount())
	}

	received := testerBox.ReadUnread()[0]
	if received.Subject != "Code ready for testing" {
		t.Errorf("expected subject 'Code ready for testing', got %q", received.Subject)
	}

	// 2. Channel broadcast
	var broadcastReceived []Message
	var mu sync.Mutex

	bus.Subscribe("discoveries", func(m Message) {
		mu.Lock()
		broadcastReceived = append(broadcastReceived, m)
		mu.Unlock()
	})

	bMsg := Message{
		Sender:  "agent-researcher",
		Channel: "discoveries",
		Body:    "Discovered optimization in sqlite query",
	}

	n, err := bus.Broadcast(bMsg)
	if err != nil {
		t.Fatalf("Broadcast failed: %v", err)
	}
	// Registered agents other than the sender receive it in their mailbox.
	if n != 1 {
		t.Errorf("expected broadcast delivered to 1 live agent inbox, got %d", n)
	}

	mu.Lock()
	if len(broadcastReceived) != 1 {
		t.Fatalf("expected 1 broadcast message received, got %d", len(broadcastReceived))
	}
	if broadcastReceived[0].Body != "Discovered optimization in sqlite query" {
		t.Errorf("unexpected broadcast body: %q", broadcastReceived[0].Body)
	}
	mu.Unlock()
}

func TestSwarmBusRegistryAndDeadLetter(t *testing.T) {
	bus := NewBus()
	defer bus.Close()

	// Sending to an unknown agent is reported as a dead letter, not silently lost.
	status, err := bus.Send(Message{Sender: "orchestrator", Recipient: "ghost", Body: "anyone?"})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if status != DeliveryDeadLetter {
		t.Fatalf("expected DeliveryDeadLetter for unknown agent, got %q", status)
	}
	if got := bus.DeadLetterCount(); got != 1 {
		t.Fatalf("expected 1 dead letter, got %d", got)
	}

	// Register the agent and confirm live probes flip.
	info := bus.RegisterAgent("ghost", "sess-9")
	if info.ID != "ghost" || info.SessionID != "sess-9" {
		t.Fatalf("unexpected agent info: %+v", info)
	}
	if !bus.IsRegistered("ghost") {
		t.Fatal("expected ghost to be registered")
	}
	if ids := bus.LiveAgentIDs(); len(ids) != 1 || ids[0] != "ghost" {
		t.Fatalf("unexpected live agents: %v", ids)
	}
	if got := len(bus.LiveAgents()); got != 1 {
		t.Fatalf("expected 1 live agent, got %d", got)
	}

	status, err = bus.Send(Message{Sender: "orchestrator", Recipient: "ghost", Body: "welcome"})
	if err != nil || status != DeliveryDelivered {
		t.Fatalf("expected delivered after registration, got status=%q err=%v", status, err)
	}

	// Dead-letter queue is bounded: saving past the cap drops the oldest.
	for i := 0; i < defaultDeadLetterCap+10; i++ {
		_, _ = bus.Send(Message{Sender: "orchestrator", Recipient: "nobody", Body: "spam"})
	}
	if got := bus.DeadLetterCount(); got > defaultDeadLetterCap {
		t.Fatalf("dead-letter queue exceeded cap: %d", got)
	}

	// Unregister takes the agent offline.
	if wasAlive := bus.UnregisterAgent("ghost"); !wasAlive {
		t.Fatal("expected UnregisterAgent to report the agent was alive")
	}
	if bus.IsRegistered("ghost") {
		t.Fatal("expected ghost to be offline after unregister")
	}
	if alive := bus.UnregisterAgent("ghost"); alive {
		t.Fatal("expected second UnregisterAgent to report not alive")
	}
}

func TestSwarmBroadcastReachesUnsubscribedRegisteredAgents(t *testing.T) {
	bus := NewBus()
	defer bus.Close()

	bus.RegisterAgent("orchestrator", "sess-1")
	bus.RegisterAgent("subagent-coder-1", "sess-1")

	// No explicit Subscribe: the broadcast must still land in the live peers'
	// mailboxes so an inbox nudge can surface it.
	n, err := bus.Broadcast(Message{Sender: "orchestrator", Channel: "discoveries", Body: "found a lead"})
	if err != nil {
		t.Fatalf("Broadcast failed: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 delivery (sender excluded), got %d", n)
	}
	if got := bus.UnreadCount("subagent-coder-1"); got != 1 {
		t.Fatalf("expected broadcast in subagent inbox, got %d unread", got)
	}
	if got := bus.UnreadCount("orchestrator"); got != 0 {
		t.Fatalf("expected sender NOT to receive its own broadcast, got %d", got)
	}
}

func TestSwarmResourceLocking(t *testing.T) {
	bus := NewBus()
	defer bus.Close()

	ctx := context.Background()

	// Acquire lock
	ok, err := bus.ClaimResource(ctx, "pkg/main.go", "agent-coder-1", "sess-1", 100*time.Millisecond)
	if err != nil || !ok {
		t.Fatalf("ClaimResource failed: ok=%v, err=%v", ok, err)
	}

	// Same owner re-acquires safely
	ok, err = bus.ClaimResource(ctx, "pkg/main.go", "agent-coder-1", "sess-1", 100*time.Millisecond)
	if err != nil || !ok {
		t.Fatalf("re-acquire by owner failed: ok=%v, err=%v", ok, err)
	}

	// Another agent is rejected
	_, err = bus.ClaimResource(ctx, "pkg/main.go", "agent-coder-2", "sess-1", 100*time.Millisecond)
	if err == nil {
		t.Fatalf("expected lock collision error for different owner")
	}

	// Release lock
	if err := bus.ReleaseResource("pkg/main.go", "agent-coder-1"); err != nil {
		t.Fatalf("ReleaseResource failed: %v", err)
	}

	// Now agent-coder-2 can acquire
	ok, err = bus.ClaimResource(ctx, "pkg/main.go", "agent-coder-2", "sess-1", 100*time.Millisecond)
	if err != nil || !ok {
		t.Fatalf("agent-coder-2 acquisition failed after release: %v", err)
	}
}

func TestSwarmLockEvents(t *testing.T) {
	bus := NewBus()
	defer bus.Close()

	var mu sync.Mutex
	var events []LockEvent
	unsub := bus.OnLockChange(func(ev LockEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	})
	defer unsub()

	ctx := context.Background()
	if ok, err := bus.ClaimResource(ctx, "a.go", "owner-1", "sess", time.Minute); err != nil || !ok {
		t.Fatalf("claim failed: %v", err)
	}
	if err := bus.ReleaseResource("a.go", "owner-1"); err != nil {
		t.Fatalf("release failed: %v", err)
	}

	// Events are dispatched asynchronously.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(events)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 {
		t.Fatalf("expected 2 lock events, got %d: %+v", len(events), events)
	}
	if events[0].Type != LockClaimed || events[0].Lock.Owner != "owner-1" {
		t.Errorf("unexpected first event: %+v", events[0])
	}
	if events[1].Type != LockReleased {
		t.Errorf("unexpected second event: %+v", events[1])
	}
}

func TestCheckToolLock(t *testing.T) {
	bus := NewBus()
	defer bus.Close()
	ctx := context.Background()

	if ok, err := bus.ClaimResource(ctx, "pkg/server/server.go", "subagent-coder-1", "sess", time.Minute); err != nil || !ok {
		t.Fatalf("claim failed: %v", err)
	}

	// A different owner is blocked when mutating the locked file.
	err := CheckToolLock(bus, "orchestrator", "hashline_patch", map[string]any{"file_path": "pkg/server/server.go"})
	if err == nil {
		t.Fatal("expected CheckToolLock to reject a locked resource for another owner")
	}

	// Equivalent path spellings collide.
	if err := CheckToolLock(bus, "orchestrator", "write_file", map[string]any{"file_path": "./pkg/server/./server.go"}); err == nil {
		t.Fatal("expected canonical path comparison to detect the lock")
	}

	// The owner itself is allowed.
	if err := CheckToolLock(bus, "subagent-coder-1", "hashline_patch", map[string]any{"file_path": "pkg/server/server.go"}); err != nil {
		t.Fatalf("owner should be allowed to edit its own lock: %v", err)
	}

	// Unrelated files and non-mutating tools are not blocked.
	if err := CheckToolLock(bus, "orchestrator", "write_file", map[string]any{"file_path": "pkg/other.go"}); err != nil {
		t.Fatalf("unlocked file should be allowed: %v", err)
	}
	if err := CheckToolLock(bus, "orchestrator", "shell", map[string]any{"command": "rm pkg/server/server.go"}); err != nil {
		t.Fatalf("shell is not lock-enforced: %v", err)
	}
	if err := CheckToolLock(nil, "orchestrator", "write_file", map[string]any{"file_path": "pkg/server/server.go"}); err != nil {
		t.Fatalf("nil bus must be a no-op: %v", err)
	}
}

func TestSwarmBusConcurrency(t *testing.T) {
	bus := NewBus()
	defer bus.Close()

	bus.RegisterAgent("collector", "sess-1")

	var wg sync.WaitGroup
	workers := 20
	msgsPerWorker := 50

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < msgsPerWorker; j++ {
				_, _ = bus.Send(Message{
					Sender:    "worker",
					Recipient: "collector",
					Body:      "work item",
				})
				_, _ = bus.Broadcast(Message{
					Sender:  "worker",
					Channel: "metrics",
					Body:    "metric tick",
				})
			}
		}(i)
	}

	wg.Wait()

	mb := bus.GetOrCreateMailbox("collector")
	// Draining messages under concurrency
	msgs := mb.ReadUnread()
	if len(msgs) == 0 {
		t.Fatalf("expected messages delivered under concurrency")
	}
}

func TestSwarmBusUnsubscribe(t *testing.T) {
	bus := NewBus()
	defer bus.Close()

	var topicCount int
	var globalCount int
	var mu sync.Mutex

	unsubTopic := bus.Subscribe("alerts", func(m Message) {
		mu.Lock()
		topicCount++
		mu.Unlock()
	})

	unsubGlobal := bus.OnMessage(func(m Message) {
		mu.Lock()
		globalCount++
		mu.Unlock()
	})

	_, _ = bus.Broadcast(Message{Channel: "alerts", Body: "alert 1"})

	// Give async global listener a moment to fire
	time.Sleep(20 * time.Millisecond)

	mu.Lock()
	if topicCount != 1 || globalCount != 1 {
		t.Fatalf("expected topicCount=1, globalCount=1; got topic=%d, global=%d", topicCount, globalCount)
	}
	mu.Unlock()

	// Unsubscribe both
	unsubTopic()
	unsubGlobal()

	_, _ = bus.Broadcast(Message{Channel: "alerts", Body: "alert 2"})
	time.Sleep(20 * time.Millisecond)

	mu.Lock()
	if topicCount != 1 || globalCount != 1 {
		t.Fatalf("expected topicCount=1, globalCount=1 after unsub; got topic=%d, global=%d", topicCount, globalCount)
	}
	mu.Unlock()
}

func TestSwarmBusUnreadCount(t *testing.T) {
	bus := NewBus()
	defer bus.Close()

	// No mailbox yet -> zero, and must not create one as a side effect.
	if got := bus.UnreadCount("orchestrator"); got != 0 {
		t.Fatalf("expected 0 unread for unknown agent, got %d", got)
	}

	bus.RegisterAgent("orchestrator", "sess-1")

	// Deliver two messages without draining.
	for i := 0; i < 2; i++ {
		if _, err := bus.Send(Message{Recipient: "orchestrator", Sender: "subagent", Body: "hi"}); err != nil {
			t.Fatalf("Send failed: %v", err)
		}
	}
	if got := bus.UnreadCount("orchestrator"); got != 2 {
		t.Fatalf("expected 2 unread, got %d", got)
	}

	// UnreadCount is a peek: draining afterwards must still yield both.
	if got := len(bus.GetOrCreateMailbox("orchestrator").ReadUnread()); got != 2 {
		t.Fatalf("expected 2 drained messages after peek, got %d", got)
	}
	if got := bus.UnreadCount("orchestrator"); got != 0 {
		t.Fatalf("expected 0 unread after drain, got %d", got)
	}
}
