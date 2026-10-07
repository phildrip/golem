package swarm

import (
	"context"
	"testing"
)

func TestSwarmToolset(t *testing.T) {
	bus := NewBus()
	defer bus.Close()

	ts1 := NewToolset(bus, "agent-coder")
	tools1, err := ts1.BuildTools()
	if err != nil {
		t.Fatalf("BuildTools failed: %v", err)
	}

	if len(tools1) != 6 {
		t.Fatalf("expected 6 tools, got %d", len(tools1))
	}

	ts2 := NewToolset(bus, "agent-tester")
	_, err = ts2.BuildTools()
	if err != nil {
		t.Fatalf("BuildTools for ts2 failed: %v", err)
	}

	ctx := context.Background()

	// 1. Direct send via tool
	sendTool := tools1[0]
	if sendTool.Name() != "send_message" {
		t.Fatalf("expected tool 0 to be send_message, got %s", sendTool.Name())
	}

	// 2. Resource locking via bus and release
	claimTool := tools1[3]
	if claimTool.Name() != "claim_resource" {
		t.Fatalf("expected tool 3 to be claim_resource, got %s", claimTool.Name())
	}

	ok, err := bus.ClaimResource(ctx, "file.go", "agent-coder", "sess-1", 0)
	if err != nil || !ok {
		t.Fatalf("claim failed: %v", err)
	}

	locks := bus.ListLocks()
	if len(locks) != 1 || locks[0].Resource != "file.go" {
		t.Fatalf("unexpected locks list: %+v", locks)
	}

	// 3. list_locks tool
	listLocksTool := tools1[5]
	if listLocksTool.Name() != "list_locks" {
		t.Fatalf("expected tool 5 to be list_locks, got %s", listLocksTool.Name())
	}
}
