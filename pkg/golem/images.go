package golem

import (
	"fmt"
	"strings"

	"google.golang.org/genai"
)

// MaxImageBytes bounds a single inline image. Providers cap inline payloads and
// a large screenshot is only a few megabytes, so 12MB is generous but finite.
const MaxImageBytes = 12 << 20

// ImageInput is an image attached to one user turn. Bytes are sent with the turn
// only: fractal memory is a text log, so the transcript keeps the chip the TUI
// writes ("[img 1: shot.png 1440x900 212KB]") and the file on disk, not the blob.
type ImageInput struct {
	Name     string
	MIMEType string
	Data     []byte
}

// imageDirective travels with an image turn. It is not written to memory: the
// model is asked to leave a text account of what it used from the picture,
// because the picture itself cannot be replayed on the next turn.
const imageDirective = "[An image is attached to this turn and shown to you inline. " +
	"The conversation transcript only keeps a text chip for it, so refer to it by that chip " +
	"and describe what you actually saw in it in your reply.]"

// buildUserContent assembles the turn content: the prompt text, any images, and
// (when images are present) the directive that keeps the text transcript usable.
func buildUserContent(input string, images []ImageInput) (*genai.Content, error) {
	parts := []*genai.Part{{Text: input}}
	for i, img := range images {
		label := img.Name
		if label == "" {
			label = fmt.Sprintf("image %d", i+1)
		}
		if len(img.Data) == 0 {
			return nil, fmt.Errorf("attached image %q has no data", label)
		}
		if len(img.Data) > MaxImageBytes {
			return nil, fmt.Errorf("attached image %q is %d bytes, over the %d byte limit", label, len(img.Data), MaxImageBytes)
		}
		mimeType := img.MIMEType
		if mimeType == "" {
			mimeType = "image/png"
		}
		parts = append(parts, &genai.Part{
			InlineData: &genai.Blob{MIMEType: mimeType, Data: img.Data},
		})
	}
	if len(images) > 0 {
		parts = append(parts, &genai.Part{Text: imageDirective})
	}
	return genai.NewContentFromParts(parts, genai.RoleUser), nil
}

// visionModelMarkers are name fragments of models known to accept image input.
// The list only drives a warning: unknown models are allowed through, and the
// provider's own error is surfaced if it cannot read images.
var visionModelMarkers = []string{
	"gemini", "gpt-4o", "gpt-4.1", "gpt-5", "claude", "o3", "o4",
	"vision", "-vl", "vl-", "pixtral", "llava", "gemma3", "qwen2-vl",
	"qwen2.5-vl", "internvl", "minicpm-v", "molmo",
}

// ModelAcceptsImages reports whether the model name looks vision-capable. It is
// a hint for the UI, not a gate: images are still sent, and a text-only model's
// error is reported instead of being silently swallowed.
func ModelAcceptsImages(modelName string) bool {
	name := strings.ToLower(strings.TrimSpace(modelName))
	if name == "" {
		return true
	}
	for _, marker := range visionModelMarkers {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}
