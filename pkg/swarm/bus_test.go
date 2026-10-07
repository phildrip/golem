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

	// 1. Direct P2P send
	msg := Message{
		Sender:    "agent-coder",
		Recipient: "agent-tester",
		Subject:   "Code ready for testing",
		Body:      "Please test pkg/server",
	}

	if err := bus.Send(msg); err != nil {
		t.Fatalf("Send failed: %v", err)
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

	if err := bus.Broadcast(bMsg); err != nil {
		t.Fatalf("Broadcast failed: %v", err)
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

func TestSwarmBusConcurrency(t *testing.T) {
	bus := NewBus()
	defer bus.Close()

	var wg sync.WaitGroup
	workers := 20
	msgsPerWorker := 50

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < msgsPerWorker; j++ {
				_ = bus.Send(Message{
					Sender:    "worker",
					Recipient: "collector",
					Body:      "work item",
				})
				_ = bus.Broadcast(Message{
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

	_ = bus.Broadcast(Message{Channel: "alerts", Body: "alert 1"})

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

	_ = bus.Broadcast(Message{Channel: "alerts", Body: "alert 2"})
	time.Sleep(20 * time.Millisecond)

	mu.Lock()
	if topicCount != 1 || globalCount != 1 {
		t.Fatalf("expected topicCount=1, globalCount=1 after unsub; got topic=%d, global=%d", topicCount, globalCount)
	}
	mu.Unlock()
}
