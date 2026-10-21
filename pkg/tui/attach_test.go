package tui

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// testPNG builds a real PNG so the sniffing path (type, dimensions, size) is
// exercised end to end rather than faked.
func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := 0; x < width; x++ {
		for y := 0; y < height; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 8), G: uint8(y * 8), B: 120, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding test png: %v", err)
	}
	return buf.Bytes()
}

func TestSniffImage(t *testing.T) {
	mime, w, h, err := sniffImage(testPNG(t, 12, 7))
	if err != nil {
		t.Fatalf("sniffImage: %v", err)
	}
	if mime != "image/png" || w != 12 || h != 7 {
		t.Fatalf("sniffImage = %q %dx%d, want image/png 12x7", mime, w, h)
	}

	webp := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), make([]byte, 16)...)
	if mime, _, _, err := sniffImage(webp); err != nil || mime != "image/webp" {
		t.Fatalf("webp sniff = %q, %v; want image/webp", mime, err)
	}

	if _, _, _, err := sniffImage([]byte("not an image at all, just text")); err == nil {
		t.Fatal("expected an error for non-image bytes")
	}
}

func TestShellFields(t *testing.T) {
	got := shellFields(`look /tmp/Screen\ shot.png "quoted path.png" 'single.png' plain`)
	want := []string{"look", "/tmp/Screen shot.png", "quoted path.png", "single.png", "plain"}
	if len(got) != len(want) {
		t.Fatalf("shellFields = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("shellFields[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestImagePathsIn(t *testing.T) {
	dir := t.TempDir()
	withSpace := filepath.Join(dir, "Screen shot.png")
	if err := os.WriteFile(withSpace, testPNG(t, 4, 4), 0o644); err != nil {
		t.Fatalf("writing png: %v", err)
	}
	plain := filepath.Join(dir, "chart.jpg")
	if err := os.WriteFile(plain, testPNG(t, 3, 3), 0o644); err != nil {
		t.Fatalf("writing jpg: %v", err)
	}
	notImage := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notImage, []byte("hi"), 0o644); err != nil {
		t.Fatalf("writing txt: %v", err)
	}

	escaped := strings.ReplaceAll(withSpace, " ", `\ `)
	prompt := "what is wrong here? " + escaped + " file://" + plain + " " + notImage + " " + escaped

	got := imagePathsIn(prompt)
	if len(got) != 2 {
		t.Fatalf("imagePathsIn = %#v, want the two image files", got)
	}
	if got[0] != withSpace || got[1] != plain {
		t.Fatalf("imagePathsIn = %#v, want [%q %q]", got, withSpace, plain)
	}

	if paths := imagePathsIn("no files here"); len(paths) != 0 {
		t.Fatalf("imagePathsIn(no files) = %#v, want none", paths)
	}
}

func TestAttachmentChip(t *testing.T) {
	att, err := NewAttachmentFromBytes(testPNG(t, 1440, 900), "shot.png")
	if err != nil {
		t.Fatalf("NewAttachmentFromBytes: %v", err)
	}
	chip := att.Chip(2)
	if !strings.HasPrefix(chip, "[img 2: shot.png 1440x900 ") {
		t.Fatalf("chip = %q, want dimension and size details", chip)
	}
	if !strings.HasSuffix(chip, "]") {
		t.Fatalf("chip = %q, want a closing bracket", chip)
	}
}

func TestNewAttachmentFromBytesRejectsNonImages(t *testing.T) {
	if _, err := NewAttachmentFromBytes([]byte("just text"), "x.png"); err == nil {
		t.Fatal("expected an error for non-image bytes")
	}
	if _, err := NewAttachmentFromBytes(nil, "x.png"); err == nil {
		t.Fatal("expected an error for empty bytes")
	}
}

func TestPersistAttachmentCopiesIntoMemoryDir(t *testing.T) {
	dir := t.TempDir()
	att, err := NewAttachmentFromBytes(testPNG(t, 5, 5), "shot one.png")
	if err != nil {
		t.Fatalf("NewAttachmentFromBytes: %v", err)
	}
	persisted, err := PersistAttachment(dir, att)
	if err != nil {
		t.Fatalf("PersistAttachment: %v", err)
	}
	if persisted.Path == "" {
		t.Fatal("PersistAttachment did not set Path")
	}
	if filepath.Dir(persisted.Path) != filepath.Join(dir, "attachments") {
		t.Fatalf("persisted path %q is not under the memory attachments dir", persisted.Path)
	}
	if !strings.HasSuffix(persisted.Path, "shot-one.png") {
		t.Fatalf("persisted path %q lost the image name", persisted.Path)
	}
	onDisk, err := os.ReadFile(persisted.Path)
	if err != nil {
		t.Fatalf("reading persisted image: %v", err)
	}
	if !bytes.Equal(onDisk, att.Data) {
		t.Fatal("persisted image bytes differ from the attachment")
	}
}

func TestComposeTurnText(t *testing.T) {
	if got := composeTurnText("plain prompt", nil); got != "plain prompt" {
		t.Fatalf("composeTurnText(no images) = %q", got)
	}
	att, err := NewAttachmentFromBytes(testPNG(t, 10, 4), "shot.png")
	if err != nil {
		t.Fatalf("NewAttachmentFromBytes: %v", err)
	}
	got := composeTurnText("why is this broken?", []Attachment{att})
	if !strings.HasPrefix(got, "why is this broken?\n[img 1: shot.png 10x4 ") {
		t.Fatalf("composeTurnText = %q, want the chip appended on its own line", got)
	}
}

func TestModelCtrlVAttachesClipboardImage(t *testing.T) {
	m := newResizeTestModel(t, 0)

	original := readClipboardImage
	readClipboardImage = func() ([]byte, error) { return testPNG(t, 20, 9), nil }
	t.Cleanup(func() { readClipboardImage = original })

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("Ctrl+V did not start a clipboard grab")
	}
	msg, ok := cmd().(attachMsg)
	if !ok {
		t.Fatalf("clipboard grab returned %T, want attachMsg", cmd())
	}
	if msg.err != nil {
		t.Fatalf("clipboard grab failed: %v", msg.err)
	}

	updated, _ = m.Update(msg)
	m = updated.(Model)
	if len(m.attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(m.attachments))
	}
	if m.attachments[0].Width != 20 || m.attachments[0].Height != 9 {
		t.Fatalf("attachment dimensions = %dx%d, want 20x9", m.attachments[0].Width, m.attachments[0].Height)
	}

	view := m.View()
	if !strings.Contains(view, "[img 1: clipboard.png 20x9 ") {
		t.Fatalf("view does not show the attachment chip:\n%s", view)
	}
	if !strings.Contains(view, "Ctrl+V: add") {
		t.Fatalf("view does not show the attachment hint:\n%s", view)
	}

	// Ctrl+X clears the pending attachments.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	m = updated.(Model)
	if len(m.attachments) != 0 {
		t.Fatalf("attachments = %d after Ctrl+X, want 0", len(m.attachments))
	}
}

func TestModelCtrlVFallsThroughToTextPaste(t *testing.T) {
	m := newResizeTestModel(t, 0)

	original := readClipboardImage
	readClipboardImage = func() ([]byte, error) {
		return nil, errNoClipboardImage
	}
	t.Cleanup(func() { readClipboardImage = original })

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	m = updated.(Model)
	msg := cmd().(attachMsg)
	if !errors.Is(msg.err, errNoClipboardImage) {
		t.Fatalf("err = %v, want errNoClipboardImage", msg.err)
	}

	updated, pasteCmd := m.Update(msg)
	m = updated.(Model)
	if len(m.attachments) != 0 {
		t.Fatalf("attachments = %d, want 0", len(m.attachments))
	}
	// The key is handed to the textarea, which answers with its own paste command.
	if pasteCmd == nil {
		t.Fatal("Ctrl+V with no clipboard image did not fall through to text paste")
	}
}

func TestModelEnterWithOnlyAnImageSends(t *testing.T) {
	m := newResizeTestModel(t, 0)
	att, err := NewAttachmentFromBytes(testPNG(t, 6, 6), "shot.png")
	if err != nil {
		t.Fatalf("NewAttachmentFromBytes: %v", err)
	}
	if err := m.addAttachment(att); err != nil {
		t.Fatalf("addAttachment: %v", err)
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("Enter with an attached image did not start a turn")
	}
	if len(m.attachments) != 0 {
		t.Fatalf("attachments = %d after send, want 0", len(m.attachments))
	}
	if len(m.entries) != 1 || m.entries[0].Type != TypeUser {
		t.Fatalf("entries = %#v, want one user entry", m.entries)
	}
	if !strings.Contains(m.entries[0].Content, defaultImagePrompt) {
		t.Fatalf("entry content = %q, want the default image prompt", m.entries[0].Content)
	}
	if !strings.Contains(m.entries[0].Content, "[img 1: shot.png 6x6 ") {
		t.Fatalf("entry content = %q, want the image chip logged", m.entries[0].Content)
	}
}

func TestModelEnterAttachesDroppedImagePath(t *testing.T) {
	m := newResizeTestModel(t, 0)
	dir := t.TempDir()
	path := filepath.Join(dir, "dropped.png")
	if err := os.WriteFile(path, testPNG(t, 7, 3), 0o644); err != nil {
		t.Fatalf("writing dropped png: %v", err)
	}

	m.textarea.SetValue("why is this layout broken? " + path)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("Enter with a dropped image path did not start a turn")
	}
	if len(m.attachments) != 0 {
		t.Fatalf("attachments = %d after send, want 0", len(m.attachments))
	}
	if len(m.entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(m.entries))
	}
	if !strings.Contains(m.entries[0].Content, "[img 1: dropped.png 7x3 ") {
		t.Fatalf("entry content = %q, want the dropped image chip", m.entries[0].Content)
	}
}
