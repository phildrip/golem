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
	"time"

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
		if r.Header.Get("User-Agent") != "golem/1.0" {
			t.Errorf("expected User-Agent golem/1.0, got %s", r.Header.Get("User-Agent"))
		}
		if r.Header.Get("x-opencode-session") == "" {
			t.Errorf("expected x-opencode-session header to be non-empty")
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

func TestClient_OpenCodeSessionAndUserAgentHeaders(t *testing.T) {
	var receivedUA, receivedSession string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUA = r.Header.Get("User-Agent")
		receivedSession = r.Header.Get("x-opencode-session")
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
						Content: "ok",
					},
					FinishReason: "stop",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient("test-model", server.URL, "key", server.Client())
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			{Role: "user", Parts: []*genai.Part{{Text: "test"}}},
		},
	}

	// 1. Default headers
	for _, err := range client.GenerateContent(context.Background(), req, false) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if receivedUA != DefaultUserAgent {
		t.Errorf("expected default UA %q, got %q", DefaultUserAgent, receivedUA)
	}
	if receivedSession == "" || !strings.HasPrefix(receivedSession, "golem-") {
		t.Errorf("expected default session with golem- prefix, got %q", receivedSession)
	}

	// 2. Custom User-Agent and SetSessionID
	client.SetUserAgent("custom-golem/2.5")
	client.SetSessionID("session-custom-999")
	if client.UserAgent() != "custom-golem/2.5" {
		t.Errorf("expected client.UserAgent() to be custom-golem/2.5, got %s", client.UserAgent())
	}
	if client.SessionID() != "session-custom-999" {
		t.Errorf("expected client.SessionID() to be session-custom-999, got %s", client.SessionID())
	}

	for _, err := range client.GenerateContent(context.Background(), req, false) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if receivedUA != "custom-golem/2.5" {
		t.Errorf("expected custom UA 'custom-golem/2.5', got %q", receivedUA)
	}
	if receivedSession != "session-custom-999" {
		t.Errorf("expected custom session 'session-custom-999', got %q", receivedSession)
	}

	// 3. Context override via WithSessionID
	ctx := WithSessionID(context.Background(), "session-ctx-override-888")
	extracted, ok := SessionIDFromContext(ctx)
	if !ok || extracted != "session-ctx-override-888" {
		t.Errorf("expected SessionIDFromContext to return session-ctx-override-888, got %q", extracted)
	}

	for _, err := range client.GenerateContent(ctx, req, false) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if receivedSession != "session-ctx-override-888" {
		t.Errorf("expected context override session 'session-ctx-override-888', got %q", receivedSession)
	}
}

func TestClient_StreamingMultiToolCallsWithIndexAndSchema(t *testing.T) {
	var receivedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected http.Flusher")
		}

		// SSE chunk 1: tool 0 start (shell)
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_shell_1","type":"function","function":{"name":"shell","arguments":""}}]}}]}`)
		flusher.Flush()

		// SSE chunk 2: tool 0 args chunk 1
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\":"}}]}}]}`)
		flusher.Flush()

		// SSE chunk 3: tool 0 args chunk 2
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"echo hello\"}"}}]}}]}`)
		flusher.Flush()

		// SSE chunk 4: tool 1 start (read_file)
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_read_2","type":"function","function":{"name":"read_file","arguments":"{\"file_path\":\"README.md\"}"}}]}}]}`)
		flusher.Flush()

		// SSE chunk 5: [DONE]
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	client := NewClient("deepseek-chat", server.URL, "key", server.Client())
	req := &model.LLMRequest{
		Config: &genai.GenerateContentConfig{
			Tools: []*genai.Tool{
				{
					FunctionDeclarations: []*genai.FunctionDeclaration{
						{
							Name:        "shell",
							Description: "Execute shell",
							Parameters: &genai.Schema{
								Type: genai.TypeObject,
								Properties: map[string]*genai.Schema{
									"command": {Type: genai.TypeString},
								},
								Required: []string{"command"},
							},
						},
					},
				},
			},
		},
		Contents: []*genai.Content{
			{Role: "user", Parts: []*genai.Part{{Text: "run shell and read file"}}},
		},
	}

	var finalResp *model.LLMResponse
	for resp, err := range client.GenerateContent(context.Background(), req, true) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.TurnComplete {
			finalResp = resp
		}
	}

	// Verify schema was lowercased in outgoing request
	var chatReq openAIChatRequest
	if err := json.Unmarshal(receivedBody, &chatReq); err != nil {
		t.Fatalf("failed to unmarshal request: %v", err)
	}
	if len(chatReq.Tools) == 0 {
		t.Fatalf("expected tools in request, got 0")
	}
	toolsJSON, _ := json.Marshal(chatReq.Tools)
	if strings.Contains(string(toolsJSON), `"OBJECT"`) || strings.Contains(string(toolsJSON), `"STRING"`) {
		t.Errorf("expected lowercased types in tools schema, got: %s", string(toolsJSON))
	}
	if !strings.Contains(string(toolsJSON), `"object"`) || !strings.Contains(string(toolsJSON), `"string"`) {
		t.Errorf("expected lowercase object/string in tools schema, got: %s", string(toolsJSON))
	}

	// Verify tool calls
	if finalResp == nil || finalResp.Content == nil {
		t.Fatalf("expected final response with content")
	}
	if len(finalResp.Content.Parts) != 2 {
		t.Fatalf("expected 2 tool calls in final parts, got %d", len(finalResp.Content.Parts))
	}

	fc0 := finalResp.Content.Parts[0].FunctionCall
	if fc0 == nil || fc0.Name != "shell" || fc0.ID != "call_shell_1" {
		t.Errorf("unexpected tool 0: %+v", fc0)
	}
	if cmd, ok := fc0.Args["command"].(string); !ok || cmd != "echo hello" {
		t.Errorf("expected command 'echo hello', got %+v", fc0.Args)
	}

	fc1 := finalResp.Content.Parts[1].FunctionCall
	if fc1 == nil || fc1.Name != "read_file" || fc1.ID != "call_read_2" {
		t.Errorf("unexpected tool 1: %+v", fc1)
	}
	if path, ok := fc1.Args["file_path"].(string); !ok || path != "README.md" {
		t.Errorf("expected file_path 'README.md', got %+v", fc1.Args)
	}
}

func TestClient_StreamingSSEErrorHandling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected http.Flusher")
		}

		// SSE chunk with error payload from API (e.g. OpenCode error)
		fmt.Fprintf(w, "data: %s\n\n", `{"error":{"message":"Request is missing parameter","type":"invalid_request"}}`)
		flusher.Flush()
	}))
	defer server.Close()

	client := NewClient("test-model", server.URL, "key", server.Client())
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			{Role: "user", Parts: []*genai.Part{{Text: "hello"}}},
		},
	}

	var streamErr error
	for _, err := range client.GenerateContent(context.Background(), req, true) {
		if err != nil {
			streamErr = err
			break
		}
	}

	if streamErr == nil {
		t.Fatalf("expected stream error for SSE error chunk, got nil")
	}
	if !strings.Contains(streamErr.Error(), "Request is missing parameter") {
		t.Errorf("expected error message to contain 'Request is missing parameter', got %v", streamErr)
	}
}

func TestClient_StreamingFinishReasonWithoutDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected http.Flusher")
		}

		// SSE chunk with text
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"content":"Hi there"}}]}`)
		flusher.Flush()

		// SSE chunk with finish_reason and NO subsequent [DONE], connection kept open
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
		flusher.Flush()

		// Keep connection open without closing or writing [DONE] for 200ms
		time.Sleep(200 * time.Millisecond)
	}))
	defer server.Close()

	client := NewClient("test-model", server.URL, "key", server.Client())
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			{Role: "user", Parts: []*genai.Part{{Text: "hi"}}},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var chunks []string
	var finalResp *model.LLMResponse
	for resp, err := range client.GenerateContent(ctx, req, true) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Partial && len(resp.Content.Parts) > 0 {
			chunks = append(chunks, resp.Content.Parts[0].Text)
		}
		if resp.TurnComplete {
			finalResp = resp
		}
	}

	if strings.Join(chunks, "") != "Hi there" {
		t.Errorf("expected chunks 'Hi there', got %q", strings.Join(chunks, ""))
	}
	if finalResp == nil || !finalResp.TurnComplete {
		t.Errorf("expected TurnComplete response before timeout")
	}
}

func TestClient_StreamingDataPrefixNoSpace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected http.Flusher")
		}

		// data: without space
		fmt.Fprintf(w, "data:%s\n\n", `{"choices":[{"delta":{"content":"NoSpace"}}]}`)
		flusher.Flush()
		fmt.Fprintf(w, "data:[DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	client := NewClient("test-model", server.URL, "key", server.Client())
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			{Role: "user", Parts: []*genai.Part{{Text: "hi"}}},
		},
	}

	var chunks []string
	for resp, err := range client.GenerateContent(context.Background(), req, true) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Partial && len(resp.Content.Parts) > 0 {
			chunks = append(chunks, resp.Content.Parts[0].Text)
		}
	}

	if strings.Join(chunks, "") != "NoSpace" {
		t.Errorf("expected 'NoSpace', got %q", strings.Join(chunks, ""))
	}
}

func TestClient_ToolCallIDFallbackMatching(t *testing.T) {
	client := NewClient("test-model", "http://localhost", "key", nil)

	// Simulate conversation with preceding tool call and subsequent FunctionResponse with empty ID
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			{
				Role: "user",
				Parts: []*genai.Part{{Text: "do something"}},
			},
			{
				Role: "model",
				Parts: []*genai.Part{
					{
						FunctionCall: &genai.FunctionCall{
							ID:   "call_calc_42",
							Name: "calculate",
							Args: map[string]any{"x": 1},
						},
					},
				},
			},
			{
				Role: "user",
				Parts: []*genai.Part{
					{
						FunctionResponse: &genai.FunctionResponse{
							Name:     "calculate",
							ID:       "", // Empty ID from ADK runner
							Response: map[string]any{"result": 2},
						},
					},
				},
			},
		},
	}

	chatReq, err := client.convertRequest(req, false)
	if err != nil {
		t.Fatalf("convertRequest failed: %v", err)
	}

	if len(chatReq.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(chatReq.Messages))
	}

	toolMsg := chatReq.Messages[2]
	if toolMsg.Role != "tool" {
		t.Errorf("expected role 'tool', got %s", toolMsg.Role)
	}
	if toolMsg.ToolCallID != "call_calc_42" {
		t.Errorf("expected ToolCallID to be matched to 'call_calc_42', got %q", toolMsg.ToolCallID)
	}
}

