package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/user/golem/pkg/golem"
)

// Attachments let a terminal user hand an image to the model. Plain terminals
// never give an app a clipboard image, so Golem reads the clipboard itself on a
// hotkey and also accepts image paths that a drag & drop drops into the prompt.
const (
	// maxAttachmentBytes bounds one image. Providers cap inline payloads and a
	// 4K screenshot is ~2-4MB, so 12MB leaves room without inviting a 60MB
	// GIF into the request.
	maxAttachmentBytes = 12 << 20

	// maxAttachmentsPerTurn keeps one turn (and one request) finite.
	maxAttachmentsPerTurn = 4
)

// errNoClipboardImage means the clipboard held no image, so Ctrl+V should fall
// through to ordinary text paste.
var errNoClipboardImage = errors.New("no image on the clipboard")

// Attachment is one image attached to the turn being composed.
type Attachment struct {
	Name     string
	Path     string // resolved file path (the persisted copy once sent)
	MIMEType string
	Data     []byte
	Width    int
	Height   int
}

// Chip renders the attachment for the transcript, e.g. "[img 1: shot.png 1440x900 212KB]".
// Fractal memory is text only, so the chip is what survives a restart: it names
// the file, its dimensions and its size.
func (a Attachment) Chip(index int) string {
	name := a.Name
	if name == "" {
		name = "image"
	}
	var details []string
	if a.Width > 0 && a.Height > 0 {
		details = append(details, fmt.Sprintf("%dx%d", a.Width, a.Height))
	}
	if n := len(a.Data); n > 0 {
		details = append(details, humanBytes(n))
	} else if a.Path != "" {
		if st, err := os.Stat(a.Path); err == nil {
			details = append(details, humanBytes(int(st.Size())))
		}
	}
	if len(details) == 0 {
		return fmt.Sprintf("[img %d: %s]", index, name)
	}
	return fmt.Sprintf("[img %d: %s %s]", index, name, strings.Join(details, " "))
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		mb := float64(n) / (1 << 20)
		if mb >= 10 {
			return fmt.Sprintf("%.0fMB", mb)
		}
		return fmt.Sprintf("%.1fMB", mb)
	case n >= 1<<10:
		return fmt.Sprintf("%dKB", n/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// clipboardCommands are tried in order until one yields image bytes.
var clipboardCommands = [][]string{
	{"wl-paste", "--no-newline", "--type", "image/png"},
	{"xclip", "-selection", "clipboard", "-t", "image/png", "-o"},
	{"pngpaste", "-"},
}

// readClipboardImage shells out to whichever clipboard tool the session has.
// It is a variable so tests can fake the clipboard without one.
var readClipboardImage = func() ([]byte, error) {
	var problems []string
	for _, cmdline := range clipboardCommands {
		bin := cmdline[0]
		if _, err := exec.LookPath(bin); err != nil {
			problems = append(problems, bin+": not installed")
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		cmd := exec.CommandContext(ctx, bin, cmdline[1:]...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		cancel()
		if err != nil || len(out) == 0 {
			problems = append(problems, fmt.Sprintf("%s: %v", bin, firstLine(err, stderr.String())))
			continue
		}
		if _, _, _, err := sniffImage(out); err != nil {
			problems = append(problems, bin+": clipboard holds no image")
			continue
		}
		return out, nil
	}
	if len(problems) == 0 {
		return nil, errNoClipboardImage
	}
	return nil, fmt.Errorf("%w (%s)", errNoClipboardImage, strings.Join(problems, "; "))
}

func firstLine(err error, stderr string) string {
	if s := strings.TrimSpace(stderr); s != "" {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[:i]
		}
		return s
	}
	if err != nil {
		return err.Error()
	}
	return "no output"
}

// sniffImage identifies the image type and, where the format is decodable by the
// standard library, its dimensions. WebP is accepted by magic bytes only.
func sniffImage(data []byte) (mime string, width, height int, err error) {
	if len(data) < 12 {
		return "", 0, 0, errors.New("not an image: too short")
	}
	if bytes.HasPrefix(data, []byte("RIFF")) && string(data[8:12]) == "WEBP" {
		return "image/webp", 0, 0, nil
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", 0, 0, fmt.Errorf("not an image: %w", err)
	}
	switch format {
	case "png":
		mime = "image/png"
	case "jpeg":
		mime = "image/jpeg"
	case "gif":
		mime = "image/gif"
	default:
		mime = "image/" + format
	}
	return mime, cfg.Width, cfg.Height, nil
}

// imageExtension reports whether path looks like an image we can attach.
func imageExtension(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	}
	return false
}

// NewAttachmentFromBytes builds an attachment from raw clipboard bytes.
func NewAttachmentFromBytes(data []byte, name string) (Attachment, error) {
	if len(data) == 0 {
		return Attachment{}, errors.New("empty image data")
	}
	if len(data) > maxAttachmentBytes {
		return Attachment{}, fmt.Errorf("image is %s, over the %s limit", humanBytes(len(data)), humanBytes(maxAttachmentBytes))
	}
	mime, w, h, err := sniffImage(data)
	if err != nil {
		return Attachment{}, err
	}
	if name == "" {
		name = "clipboard.png"
	}
	return Attachment{Name: name, MIMEType: mime, Data: data, Width: w, Height: h}, nil
}

// LoadAttachment reads an image file from disk.
func LoadAttachment(path string) (Attachment, error) {
	expanded, err := expandUserPath(path)
	if err != nil {
		return Attachment{}, err
	}
	st, err := os.Stat(expanded)
	if err != nil {
		return Attachment{}, err
	}
	if st.IsDir() {
		return Attachment{}, fmt.Errorf("%s is a directory", expanded)
	}
	if st.Size() > maxAttachmentBytes {
		return Attachment{}, fmt.Errorf("%s is %s, over the %s limit", filepath.Base(expanded), humanBytes(int(st.Size())), humanBytes(maxAttachmentBytes))
	}
	data, err := os.ReadFile(expanded)
	if err != nil {
		return Attachment{}, err
	}
	att, err := NewAttachmentFromBytes(data, filepath.Base(expanded))
	if err != nil {
		return Attachment{}, fmt.Errorf("%s: %w", filepath.Base(expanded), err)
	}
	att.Path = expanded
	return att, nil
}

func expandUserPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "file://")
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	if unescaped, err := url.PathUnescape(path); err == nil {
		path = unescaped
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return abs, nil
}

// shellFields splits a prompt into shell-like words, honouring "..." / '...'
// quoting and backslash escapes. Terminals drop a path into the prompt
// shell-escaped ("/tmp/Screen\ shot.png"), so plain whitespace splitting is not
// enough to recognise it.
func shellFields(s string) []string {
	var (
		out      []string
		cur      strings.Builder
		inSingle bool
		inDouble bool
		escaped  bool
		started  bool
	)
	flush := func() {
		if started {
			out = append(out, cur.String())
			cur.Reset()
			started = false
		}
	}
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\' && !inSingle:
			escaped = true
			started = true
		case r == '\'' && !inDouble:
			inSingle = !inSingle
			started = true
		case r == '"' && !inSingle:
			inDouble = !inDouble
			started = true
		case (r == ' ' || r == '\t' || r == '\n' || r == '\r') && !inSingle && !inDouble:
			flush()
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	flush()
	return out
}

// imagePathsIn returns the existing image files referenced by a prompt. Nothing
// is removed from the prompt: the model can still use the path with its tools.
func imagePathsIn(text string) []string {
	if !strings.Contains(text, ".") {
		return nil
	}
	seen := make(map[string]bool)
	var paths []string
	for _, field := range shellFields(text) {
		if !imageExtension(field) {
			continue
		}
		resolved, err := expandUserPath(field)
		if err != nil {
			continue
		}
		st, err := os.Stat(resolved)
		if err != nil || st.IsDir() {
			continue
		}
		if seen[resolved] {
			continue
		}
		seen[resolved] = true
		paths = append(paths, resolved)
	}
	return paths
}

// PersistAttachment copies an attachment into the memory directory so the
// transcript chip keeps pointing at a real file after a restart (and after the
// source screenshot is deleted). It returns the attachment with Path set.
func PersistAttachment(memoryDir string, att Attachment) (Attachment, error) {
	if memoryDir == "" {
		return att, errors.New("no memory directory configured")
	}
	dir := filepath.Join(memoryDir, "attachments")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return att, err
	}
	name := fmt.Sprintf("%d-%s", time.Now().UnixNano(), sanitiseName(att.Name))
	target := filepath.Join(dir, name)
	if err := os.WriteFile(target, att.Data, 0o644); err != nil {
		return att, err
	}
	att.Path = target
	return att, nil
}

func sanitiseName(name string) string {
	if name == "" {
		return "image"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "image"
	}
	return out
}

// attachmentsChipLine renders the pending attachments for the input area.
func attachmentsChipLine(atts []Attachment) string {
	if len(atts) == 0 {
		return ""
	}
	chips := make([]string, 0, len(atts))
	for i, att := range atts {
		chips = append(chips, att.Chip(i+1))
	}
	return strings.Join(chips, " ")
}

// imagesForTurn converts attachments into the agent's per-turn image input.
func imagesForTurn(atts []Attachment) []golem.ImageInput {
	out := make([]golem.ImageInput, 0, len(atts))
	for _, att := range atts {
		out = append(out, golem.ImageInput{
			Name:     att.Name,
			MIMEType: att.MIMEType,
			Data:     att.Data,
		})
	}
	return out
}

// defaultImagePrompt is sent when the user attaches an image and presses Enter
// on an otherwise empty prompt.
const defaultImagePrompt = "Have a look at the attached image."

// grabClipboardImageCmd reads the clipboard off the UI goroutine.
func (m *Model) grabClipboardImageCmd() tea.Cmd {
	return func() tea.Msg {
		data, err := readClipboardImage()
		if err != nil {
			return attachMsg{err: err}
		}
		att, err := NewAttachmentFromBytes(data, "clipboard.png")
		if err != nil {
			return attachMsg{err: err}
		}
		return attachMsg{attachment: att}
	}
}

// addAttachment queues an image for the next turn, copying it into the memory
// directory so the transcript chip keeps resolving after a restart.
func (m *Model) addAttachment(att Attachment) error {
	if len(m.attachments) >= maxAttachmentsPerTurn {
		return fmt.Errorf("at most %d images per turn", maxAttachmentsPerTurn)
	}
	if m.agent != nil && len(att.Data) > 0 {
		if persisted, err := PersistAttachment(m.agent.Config().StorageDir, att); err == nil {
			att = persisted
		}
	}
	m.attachments = append(m.attachments, att)
	if m.agent != nil && !golem.ModelAcceptsImages(m.agent.Config().ModelName) {
		m.setStatus(fmt.Sprintf("Attached %s • %s may be text-only; sending anyway",
			att.Chip(len(m.attachments)), m.agent.Config().ModelName))
	}
	return nil
}

// attachImagePath loads an image file named in the prompt (drag & drop or a
// typed path) and queues it exactly like a clipboard grab.
func (m *Model) attachImagePath(path string) {
	att, err := LoadAttachment(path)
	if err != nil {
		m.setStatus("Attach failed: " + err.Error())
		return
	}
	if err := m.addAttachment(att); err != nil {
		m.setStatus("Attach failed: " + err.Error())
	}
}

// composeTurnText appends the attachment chips to the prompt. This text is what
// gets logged, so the picture stays addressable in a text-only memory.
func composeTurnText(input string, atts []Attachment) string {
	if len(atts) == 0 {
		return input
	}
	var sb strings.Builder
	sb.WriteString(input)
	for i, att := range atts {
		sb.WriteString("\n")
		sb.WriteString(att.Chip(i + 1))
	}
	return sb.String()
}

// renderUserContent colours a user turn: the prompt stands out, the attachment
// chips stay muted so they read as metadata rather than words.
func renderUserContent(content string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "[img ") {
			lines[i] = lipgloss.NewStyle().Foreground(ColorMuted).Render(line)
			continue
		}
		lines[i] = lipgloss.NewStyle().Foreground(ColorHighlight).Render(line)
	}
	return strings.Join(lines, "\n")
}
