package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

func TestClient_UnaryGenerateContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-api-key" {
			t.Errorf("unexpected Authorization header: %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected Content-Type header: %s", r.Header.Get("Content-Type"))
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		var req openAIChatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("failed to unmarshal request: %v", err)
		}

		if req.Model != "deepseek-chat" {
			t.Errorf("expected model deepseek-chat, got %s", req.Model)
		}
		if len(req.Messages) == 0 {
			t.Errorf("expected messages, got 0")
		}

		// Return OpenAI formatted response with a tool call
		resp := openAIChatResponse{
			Choices: []struct {
				Message struct {
					Role      string           `json:"role"`
					Content   string           `json:"content"`
					ToolCalls []openAIToolCall `json:"tool_calls"`
				} `json:"message"`
				Delta struct {
					Role      string           `json:"role"`
					Content   string           `json:"content"`
					ToolCalls []openAIToolCall `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			}{
				{
					Message: struct {
						Role      string           `json:"role"`
						Content   string           `json:"content"`
						ToolCalls []openAIToolCall `json:"tool_calls"`
					}{
						Role:    "assistant",
						Content: "Let me check that file for you.",
						ToolCalls: []openAIToolCall{
							{
								ID:   "call_123",
								Type: "function",
								Function: openAIFunctionCallDesc{
									Name:      "read_file",
									Arguments: `{"path":"main.go"}`,
								},
							},
						},
					},
					FinishReason: "tool_calls",
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient("deepseek-chat", server.URL, "test-api-key", server.Client())
	if client.Name() != "deepseek-chat" {
		t.Errorf("expected client name deepseek-chat, got %s", client.Name())
	}

	req := &model.LLMRequest{
		Config: &genai.GenerateContentConfig{
			SystemInstruction: &genai.Content{
				Parts: []*genai.Part{{Text: "You are an assistant."}},
			},
			Tools: []*genai.Tool{
				{
					FunctionDeclarations: []*genai.FunctionDeclaration{
						{
							Name:        "read_file",
							Description: "Reads a file",
						},
					},
				},
			},
		},
		Contents: []*genai.Content{
			{
				Role:  "user",
				Parts: []*genai.Part{{Text: "Can you read main.go?"}},
			},
		},
	}

	var responses []*model.LLMResponse
	for resp, err := range client.GenerateContent(context.Background(), req, false) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		responses = append(responses, resp)
	}

	if len(responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(responses))
	}

	r := responses[0]
	if !r.TurnComplete {
		t.Errorf("expected TurnComplete to be true")
	}
	if len(r.Content.Parts) != 2 {
		t.Fatalf("expected 2 parts (text + tool call), got %d", len(r.Content.Parts))
	}
	if r.Content.Parts[0].Text != "Let me check that file for you." {
		t.Errorf("unexpected text part: %s", r.Content.Parts[0].Text)
	}
	if r.Content.Parts[1].FunctionCall == nil || r.Content.Parts[1].FunctionCall.Name != "read_file" {
		t.Errorf("unexpected function call part: %+v", r.Content.Parts[1].FunctionCall)
	}
}

func TestClient_StreamingGenerateContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected http.Flusher")
		}

		// SSE chunk 1: text token "Hello"
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"role":"assistant","content":"Hello"}}]}`)
		flusher.Flush()

		// SSE chunk 2: text token " world"
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"content":" world"}}]}`)
		flusher.Flush()

		// SSE chunk 3: tool call start
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"tool_calls":[{"id":"call_999","type":"function","function":{"name":"run_cmd","arguments":"{\"cmd\":"}}]}}]}`)
		flusher.Flush()

		// SSE chunk 4: tool call argument continuation
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"\"ls\"}"}}]}}]}`)
		flusher.Flush()

		// SSE chunk 5: [DONE]
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	client := NewClient("gpt-4o", server.URL, "dummy-key", server.Client())
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			{
				Role:  "user",
				Parts: []*genai.Part{{Text: "Hi"}},
			},
		},
	}

	var textParts []string
	var finalResp *model.LLMResponse

	for resp, err := range client.GenerateContent(context.Background(), req, true) {
		if err != nil {
			t.Fatalf("unexpected streaming error: %v", err)
		}
		if resp.Partial {
			if len(resp.Content.Parts) > 0 {
				textParts = append(textParts, resp.Content.Parts[0].Text)
			}
		}
		if resp.TurnComplete {
			finalResp = resp
		}
	}

	fullText := strings.Join(textParts, "")
	if fullText != "Hello world" {
		t.Errorf("expected 'Hello world', got %q", fullText)
	}

	if finalResp == nil {
		t.Fatalf("expected final response with TurnComplete")
	}
	if len(finalResp.Content.Parts) != 2 || finalResp.Content.Parts[0].Text != "Hello world" || finalResp.Content.Parts[1].FunctionCall == nil {
		t.Fatalf("expected text and accumulated function call in finalResp, got: %+v", finalResp.Content)
	}
	fc := finalResp.Content.Parts[1].FunctionCall
	if fc.Name != "run_cmd" || fc.ID != "call_999" {
		t.Errorf("unexpected function call: %+v", fc)
	}
	if cmdVal, ok := fc.Args["cmd"].(string); !ok || cmdVal != "ls" {
		t.Errorf("expected arg cmd=ls, got %+v", fc.Args)
	}
}

func TestClient_ErrorHandling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"Rate limit exceeded","type":"rate_limit"}}`, 429)
	}))
	defer server.Close()

	client := NewClient("deepseek-chat", server.URL, "key", server.Client())
	req := &model.LLMRequest{}

	hasError := false
	for _, err := range client.GenerateContent(context.Background(), req, false) {
		if err != nil {
			hasError = true
			if !strings.Contains(err.Error(), "429") {
				t.Errorf("expected error to mention 429, got %v", err)
			}
		}
	}

	if !hasError {
		t.Errorf("expected error from 429 response, but got none")
	}
}
