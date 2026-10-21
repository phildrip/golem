package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// A user turn carrying an image must go out as a content-parts array with a data
// URL, not as a flattened string: otherwise the picture is silently dropped.
func TestConvertRequestBuildsMultimodalContentForImages(t *testing.T) {
	client := NewClient("gpt-4o", "", "", nil)
	req := &model.LLMRequest{
		Contents: []*genai.Content{{
			Role: "user",
			Parts: []*genai.Part{
				{Text: "what is broken here?"},
				{InlineData: &genai.Blob{MIMEType: "image/png", Data: []byte{1, 2, 3, 4}}},
			},
		}},
	}

	chatReq, err := client.convertRequest(req, true)
	if err != nil {
		t.Fatalf("convertRequest: %v", err)
	}
	if len(chatReq.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(chatReq.Messages))
	}

	parts, ok := chatReq.Messages[0].Content.([]openAIContentPart)
	if !ok {
		t.Fatalf("content = %T, want []openAIContentPart", chatReq.Messages[0].Content)
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want text + image", len(parts))
	}
	if parts[0].Type != "text" || parts[0].Text != "what is broken here?" {
		t.Fatalf("first part = %#v, want the prompt text", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL == nil {
		t.Fatalf("second part = %#v, want an image_url part", parts[1])
	}
	if !strings.HasPrefix(parts[1].ImageURL.URL, "data:image/png;base64,") {
		t.Fatalf("image url = %q, want a png data URL", parts[1].ImageURL.URL)
	}

	raw, err := json.Marshal(chatReq.Messages[0])
	if err != nil {
		t.Fatalf("marshaling message: %v", err)
	}
	if !strings.Contains(string(raw), `"type":"image_url"`) {
		t.Fatalf("marshaled message has no image_url: %s", raw)
	}
	if !strings.Contains(string(raw), "data:image/png;base64,AQIDBA==") {
		t.Fatalf("marshaled message has no base64 payload: %s", raw)
	}
}

// Text-only turns keep the plain string form so nothing changes for the common case.
func TestConvertRequestKeepsTextContentAsString(t *testing.T) {
	client := NewClient("deepseek-v4.1-flash", "", "", nil)
	req := &model.LLMRequest{
		Contents: []*genai.Content{{
			Role:  "user",
			Parts: []*genai.Part{{Text: "hello"}},
		}},
	}

	chatReq, err := client.convertRequest(req, true)
	if err != nil {
		t.Fatalf("convertRequest: %v", err)
	}
	content, ok := chatReq.Messages[0].Content.(string)
	if !ok {
		t.Fatalf("content = %T, want a plain string", chatReq.Messages[0].Content)
	}
	if content != "hello" {
		t.Fatalf("content = %q, want hello", content)
	}

	raw, err := json.Marshal(chatReq.Messages[0])
	if err != nil {
		t.Fatalf("marshaling message: %v", err)
	}
	if !strings.Contains(string(raw), `"content":"hello"`) {
		t.Fatalf("marshaled message = %s, want a string content", raw)
	}
}

// The in-memory parts slice is not the proof: the picture has to leave the
// process. This drives the real HTTP path and asserts the body the provider
// receives carries the image as a data URL, so a regression that drops images
// (or encodes them wrong) fails here rather than in a user's turn.
func TestClient_ImageTurnReachesRequestWire(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"a 1x1 png"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	client := NewClient("gpt-4o", server.URL, "test-api-key", server.Client())
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	req := &model.LLMRequest{
		Contents: []*genai.Content{{
			Role: "user",
			Parts: []*genai.Part{
				{Text: "what is broken here?"},
				{InlineData: &genai.Blob{MIMEType: "image/png", Data: png}},
			},
		}},
	}

	for llmResp, err := range client.GenerateContent(context.Background(), req, false) {
		if err != nil {
			t.Fatalf("GenerateContent: %v", err)
		}
		_ = llmResp
	}

	if len(gotBody) == 0 {
		t.Fatal("provider received no request body")
	}
	wire := string(gotBody)
	if !strings.Contains(wire, `"type":"image_url"`) {
		t.Fatalf("request body has no image_url part: %s", wire)
	}
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	if !strings.Contains(wire, want) {
		t.Fatalf("request body does not carry %q: %s", want, wire)
	}
	if !strings.Contains(wire, "what is broken here?") {
		t.Fatalf("request body lost the prompt text: %s", wire)
	}
}
