package golem

import (
	"strings"
	"testing"
)

func TestBuildUserContentTextOnly(t *testing.T) {
	content, err := buildUserContent("hello", nil)
	if err != nil {
		t.Fatalf("buildUserContent: %v", err)
	}
	if content.Role != "user" {
		t.Fatalf("role = %q, want user", content.Role)
	}
	if len(content.Parts) != 1 || content.Parts[0].Text != "hello" {
		t.Fatalf("parts = %#v, want a single text part", content.Parts)
	}
}

func TestBuildUserContentWithImages(t *testing.T) {
	images := []ImageInput{
		{Name: "shot.png", MIMEType: "image/png", Data: []byte{1, 2, 3}},
		{Name: "chart.jpg", Data: []byte{4, 5}},
	}
	content, err := buildUserContent("what is broken?", images)
	if err != nil {
		t.Fatalf("buildUserContent: %v", err)
	}
	if len(content.Parts) != 4 {
		t.Fatalf("parts = %d, want text + 2 images + directive", len(content.Parts))
	}
	if content.Parts[0].Text != "what is broken?" {
		t.Fatalf("first part = %q, want the prompt text first", content.Parts[0].Text)
	}
	first := content.Parts[1].InlineData
	if first == nil || first.MIMEType != "image/png" || len(first.Data) != 3 {
		t.Fatalf("first image part = %#v, want the png blob", content.Parts[1])
	}
	second := content.Parts[2].InlineData
	if second == nil || second.MIMEType != "image/png" {
		t.Fatalf("second image part = %#v, want a defaulted png mime type", content.Parts[2])
	}
	if !strings.Contains(content.Parts[3].Text, "describe what you actually saw") {
		t.Fatalf("directive part = %q, want the transcript instruction", content.Parts[3].Text)
	}
}

func TestBuildUserContentRejectsBadImages(t *testing.T) {
	if _, err := buildUserContent("hi", []ImageInput{{Name: "empty.png"}}); err == nil {
		t.Fatal("expected an error for an image with no data")
	}
	huge := ImageInput{Name: "huge.png", MIMEType: "image/png", Data: make([]byte, MaxImageBytes+1)}
	if _, err := buildUserContent("hi", []ImageInput{huge}); err == nil {
		t.Fatal("expected an error for an oversized image")
	}
}

func TestModelAcceptsImages(t *testing.T) {
	cases := map[string]bool{
		"gemini-flash-latest": true,
		"gemini-2.5-pro":      true,
		"gpt-4o":              true,
		"claude-sonnet-4":     true,
		"qwen2.5-vl-72b":      true,
		"deepseek-v4.1-flash": false,
		"qwen2.5-coder-32b":   false,
		"":                    true, // unknown: assume the provider will decide
	}
	for name, want := range cases {
		if got := ModelAcceptsImages(name); got != want {
			t.Errorf("ModelAcceptsImages(%q) = %v, want %v", name, got, want)
		}
	}
}
