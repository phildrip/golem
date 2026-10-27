package approval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/user/golem/pkg/sandbox"
	"google.golang.org/adk/v2/tool/toolconfirmation"
)

// TestGateRecordsEveryDecision pins the audit contract: allow, deny and confirm
// all leave a record, because "what did it do" is only answerable if the
// allowed calls are in the log too.
func TestGateRecordsEveryDecision(t *testing.T) {
	a, err := NewAuditor("", 10)
	if err != nil {
		t.Fatalf("NewAuditor: %v", err)
	}
	SetAuditor(a)
	t.Cleanup(func() { SetAuditor(nil) })

	ctx := &fakeApprover{}
	_ = Gate(ctx, Interactive, "shell", sandbox.Decision{Verdict: sandbox.Allow, Rule: "default"}, "ls")
	_ = Gate(ctx, Interactive, "shell", sandbox.Decision{Verdict: sandbox.Deny, Rule: "privilege-escalation", Reason: "no sudo"}, "sudo ls")
	_ = Gate(ctx, Interactive, "shell", sandbox.Decision{Verdict: sandbox.Confirm, Rule: "force-push"}, "git push --force")

	got := a.Recent(0)
	if len(got) != 3 {
		t.Fatalf("records = %d, want 3: %+v", len(got), got)
	}
	if got[0].Verdict != "allow" || got[1].Verdict != "deny" || got[2].Verdict != "confirm" {
		t.Fatalf("verdicts = %q, %q, %q", got[0].Verdict, got[1].Verdict, got[2].Verdict)
	}
	if got[2].Action != "git push --force" {
		t.Fatalf("the log lost the exact action: %+v", got[2])
	}
}

// TestGateRecordsTheAnswer keeps the human's decision in the log next to the
// prompt that produced it.
func TestGateRecordsTheAnswer(t *testing.T) {
	a, _ := NewAuditor("", 10)
	SetAuditor(a)
	t.Cleanup(func() { SetAuditor(nil) })

	approved := &fakeApprover{confirmation: &toolconfirmation.ToolConfirmation{Confirmed: true}}
	if err := Gate(approved, Interactive, "shell", sandbox.Decision{Verdict: sandbox.Confirm, Rule: "force-push"}, "git push --force"); err != nil {
		t.Fatalf("approved call: %v", err)
	}
	records := a.Recent(0)
	if len(records) != 2 {
		t.Fatalf("records = %d, want the prompt and the answer: %+v", len(records), records)
	}
	if records[1].Rule != "user-approved" || records[1].Verdict != "allow" {
		t.Fatalf("answer record = %+v", records[1])
	}
}

// TestAuditorWritesJSONL covers the durable half: one line per decision, mode
// 0600, and the file survives a restart because it is opened for append.
func TestAuditorWritesJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "audit.jsonl")
	a, err := NewAuditor(path, 10)
	if err != nil {
		t.Fatalf("NewAuditor: %v", err)
	}
	a.Record(Record{Tool: "shell", Verdict: "deny", Rule: "sudo", Action: "sudo ls"})
	a.Record(Record{Tool: "write_file", Verdict: "allow", Action: "write_file notes.md"})
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("audit log mode = %o, want 600", perm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2:\n%s", len(lines), data)
	}
	var rec Record
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rec.Tool != "shell" || rec.Rule != "sudo" || rec.Time.IsZero() {
		t.Fatalf("record = %+v", rec)
	}

	// Re-opening appends rather than truncating.
	a2, err := NewAuditor(path, 10)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	a2.Record(Record{Tool: "shell", Verdict: "allow"})
	_ = a2.Close()
	data, _ = os.ReadFile(path)
	if got := len(strings.Split(strings.TrimSpace(string(data)), "\n")); got != 3 {
		t.Fatalf("after re-open lines = %d, want 3", got)
	}
}

// TestAuditorRingIsBounded keeps a long session from growing without bound.
func TestAuditorRingIsBounded(t *testing.T) {
	a, _ := NewAuditor("", 3)
	for i := 0; i < 10; i++ {
		a.Record(Record{Tool: "shell", Verdict: "allow", Action: "call"})
	}
	if got := len(a.Recent(0)); got != 3 {
		t.Fatalf("ring kept %d records, want 3", got)
	}
}

// TestNilAuditorIsSafe keeps the disabled path free of nil checks everywhere.
func TestNilAuditorIsSafe(t *testing.T) {
	var a *Auditor
	a.Record(Record{Tool: "shell"})
	if a.Recent(5) != nil {
		t.Fatal("a nil auditor must report nothing")
	}
	if err := a.Close(); err != nil {
		t.Fatalf("Close on nil: %v", err)
	}
	SetAuditor(nil)
	_ = Gate(&fakeApprover{}, Interactive, "shell", sandbox.Decision{Verdict: sandbox.Allow}, "ls")
}
