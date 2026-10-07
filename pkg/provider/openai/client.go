package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"strings"
	"time"

	"github.com/pkg/errors"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// DefaultBaseURL points to OpenCode Go OpenAI-compatible API.
const DefaultBaseURL = "https://opencode.ai/zen/go/v1"

// DefaultUserAgent is the client identity sent to OpenAI-compatible endpoints like OpenCode Go.
const DefaultUserAgent = "golem/1.0"

type contextKey string

const (
	sessionIDContextKey contextKey = "opencode_session_id"
)

// WithSessionID returns a copy of parent context with the session ID set.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionIDContextKey, sessionID)
}

// SessionIDFromContext retrieves the session ID from ctx if present.
func SessionIDFromContext(ctx context.Context) (string, bool) {
	sid, ok := ctx.Value(sessionIDContextKey).(string)
	return sid, ok
}

// Client implements ADK v2 model.LLM for OpenAI-compatible providers
// such as OpenCode Go, DeepSeek, OpenRouter, and local inference engines.
type Client struct {
	modelName  string
	baseURL    string
	apiKey     string
	sessionID  string
	userAgent  string
	httpClient *http.Client
}

// NewClient constructs an OpenAI-compatible model.LLM client.
func NewClient(modelName, baseURL, apiKey string, httpClient *http.Client) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: 120 * time.Second,
		}
	}
	return &Client{
		modelName:  modelName,
		baseURL:    baseURL,
		apiKey:     apiKey,
		sessionID:  fmt.Sprintf("golem-%d", time.Now().UnixNano()),
		userAgent:  DefaultUserAgent,
		httpClient: httpClient,
	}
}

// SetSessionID updates the default session ID for requests from this client.
func (c *Client) SetSessionID(sessionID string) {
	if sessionID != "" {
		c.sessionID = sessionID
	}
}

// SessionID returns the client's default session ID.
func (c *Client) SessionID() string {
	return c.sessionID
}

// SetUserAgent updates the client's User-Agent header value.
func (c *Client) SetUserAgent(userAgent string) {
	if userAgent != "" {
		c.userAgent = userAgent
	}
}

// UserAgent returns the client's User-Agent string.
func (c *Client) UserAgent() string {
	return c.userAgent
}

// Name returns the configured model identifier.
func (c *Client) Name() string {
	return c.modelName
}

// OpenAIChatMessage represents a message in the OpenAI chat format.
type openAIChatMessage struct {
	Role       string              `json:"role"`
	Content    string              `json:"content,omitempty"`
	ToolCalls  []openAIToolCall    `json:"tool_calls,omitempty"`
	ToolCallID string              `json:"tool_call_id,omitempty"`
}

type openAIToolCall struct {
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Function openAIFunctionCallDesc `json:"function"`
}

type openAIFunctionCallDesc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAITool struct {
	Type     string             `json:"type"`
	Function openAIFunctionSpec `json:"function"`
}

type openAIFunctionSpec struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

type openAIChatRequest struct {
	Model       string              `json:"model"`
	Messages    []openAIChatMessage `json:"messages"`
	Tools       []openAITool        `json:"tools,omitempty"`
	Stream      bool                `json:"stream,omitempty"`
	Temperature *float64            `json:"temperature,omitempty"`
}

type openAIChatResponse struct {
	Choices []struct {
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
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// GenerateContent executes a generation request, supporting both streaming and unary calls.
func (c *Client) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		chatReq, err := c.convertRequest(req, stream)
		if err != nil {
			yield(nil, errors.Wrap(err, "converting request to openai format"))
			return
		}

		endpoint := c.baseURL
		if !strings.HasSuffix(endpoint, "/chat/completions") {
			endpoint += "/chat/completions"
		}

		bodyBytes, err := json.Marshal(chatReq)
		if err != nil {
			yield(nil, errors.Wrap(err, "marshaling openai request json"))
			return
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			yield(nil, errors.Wrap(err, "creating http request"))
			return
		}

		httpReq.Header.Set("Content-Type", "application/json")
		if c.apiKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
		}

		ua := c.userAgent
		if ua == "" {
			ua = DefaultUserAgent
		}
		httpReq.Header.Set("User-Agent", ua)

		sid := c.sessionID
		if ctxSid, ok := SessionIDFromContext(ctx); ok && ctxSid != "" {
			sid = ctxSid
		}
		if sid == "" {
			sid = fmt.Sprintf("golem-%d", time.Now().UnixNano())
		}
		httpReq.Header.Set("x-opencode-session", sid)

		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			yield(nil, errors.Wrapf(err, "executing request to %s", endpoint))
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			errBody, _ := io.ReadAll(resp.Body)
			yield(nil, fmt.Errorf("api error %d from %s: %s", resp.StatusCode, endpoint, string(errBody)))
			return
		}

		if stream {
			c.handleStreamingResponse(resp.Body, yield)
		} else {
			c.handleUnaryResponse(resp.Body, yield)
		}
	}
}

func (c *Client) convertRequest(req *model.LLMRequest, stream bool) (*openAIChatRequest, error) {
	chatReq := &openAIChatRequest{
		Model:    c.modelName,
		Messages: make([]openAIChatMessage, 0),
		Stream:   stream,
	}

	// 1. System instruction
	if req.Config != nil && req.Config.SystemInstruction != nil {
		var sb strings.Builder
		for _, part := range req.Config.SystemInstruction.Parts {
			if part.Text != "" {
				sb.WriteString(part.Text)
			}
		}
		if sysText := sb.String(); sysText != "" {
			chatReq.Messages = append(chatReq.Messages, openAIChatMessage{
				Role:    "system",
				Content: sysText,
			})
		}
	}

	// 2. Chat history contents
	for _, content := range req.Contents {
		if content == nil {
			continue
		}

		role := "user"
		if content.Role == "model" || content.Role == "assistant" {
			role = "assistant"
		}

		var textBuilder strings.Builder
		var toolCalls []openAIToolCall
		var toolResponses []*genai.FunctionResponse

		for _, part := range content.Parts {
			if part == nil {
				continue
			}
			if part.Text != "" {
				textBuilder.WriteString(part.Text)
			}
			if part.FunctionCall != nil {
				argsJSON, _ := json.Marshal(part.FunctionCall.Args)
				callID := part.FunctionCall.ID
				if callID == "" {
					callID = fmt.Sprintf("call_%s_%d", part.FunctionCall.Name, time.Now().UnixNano())
				}
				toolCalls = append(toolCalls, openAIToolCall{
					ID:   callID,
					Type: "function",
					Function: openAIFunctionCallDesc{
						Name:      part.FunctionCall.Name,
						Arguments: string(argsJSON),
					},
				})
			}
			if part.FunctionResponse != nil {
				toolResponses = append(toolResponses, part.FunctionResponse)
			}
		}

		// Tool response messages in OpenAI format
		if len(toolResponses) > 0 {
			for _, tr := range toolResponses {
				respJSON, _ := json.Marshal(tr.Response)
				chatReq.Messages = append(chatReq.Messages, openAIChatMessage{
					Role:       "tool",
					ToolCallID: tr.ID,
					Content:    string(respJSON),
				})
			}
			continue
		}

		// Standard user or assistant message
		msg := openAIChatMessage{
			Role:      role,
			Content:   textBuilder.String(),
			ToolCalls: toolCalls,
		}
		chatReq.Messages = append(chatReq.Messages, msg)
	}

	// 3. Tools definitions
	if req.Config != nil && len(req.Config.Tools) > 0 {
		for _, t := range req.Config.Tools {
			if t == nil {
				continue
			}
			for _, decl := range t.FunctionDeclarations {
				if decl == nil {
					continue
				}
				chatReq.Tools = append(chatReq.Tools, openAITool{
					Type: "function",
					Function: openAIFunctionSpec{
						Name:        decl.Name,
						Description: decl.Description,
						Parameters:  decl.Parameters,
					},
				})
			}
		}
	}

	return chatReq, nil
}

func (c *Client) handleUnaryResponse(r io.Reader, yield func(*model.LLMResponse, error) bool) {
	var resp openAIChatResponse
	if err := json.NewDecoder(r).Decode(&resp); err != nil {
		yield(nil, errors.Wrap(err, "decoding unary response json"))
		return
	}

	if resp.Error != nil && resp.Error.Message != "" {
		yield(nil, errors.New(resp.Error.Message))
		return
	}

	if len(resp.Choices) == 0 {
		yield(&model.LLMResponse{TurnComplete: true}, nil)
		return
	}

	choice := resp.Choices[0]
	msg := choice.Message

	var parts []*genai.Part
	if msg.Content != "" {
		parts = append(parts, &genai.Part{Text: msg.Content})
	}

	for _, tc := range msg.ToolCalls {
		var args map[string]any
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
		parts = append(parts, &genai.Part{
			FunctionCall: &genai.FunctionCall{
				ID:   tc.ID,
				Name: tc.Function.Name,
				Args: args,
			},
		})
	}

	content := &genai.Content{
		Role:  "model",
		Parts: parts,
	}

	yield(&model.LLMResponse{
		Content:      content,
		TurnComplete: true,
	}, nil)
}

func (c *Client) handleStreamingResponse(r io.Reader, yield func(*model.LLMResponse, error) bool) {
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)

	type toolAccumulator struct {
		id   string
		name string
		args strings.Builder
	}
	accumulatedTools := make(map[int]*toolAccumulator)

	var accumulatedText strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if strings.TrimSpace(data) == "[DONE]" {
			break
		}

		var chunk openAIChatResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]
		delta := choice.Delta

		// Plain text token streaming
		if delta.Content != "" {
			accumulatedText.WriteString(delta.Content)
			if !yield(&model.LLMResponse{
				Content: &genai.Content{
					Role: "model",
					Parts: []*genai.Part{
						{Text: delta.Content},
					},
				},
				Partial: true,
			}, nil) {
				return
			}
		}

		// Tool calls streaming
		for idx, tc := range delta.ToolCalls {
			acc, ok := accumulatedTools[idx]
			if !ok {
				acc = &toolAccumulator{}
				accumulatedTools[idx] = acc
			}
			if tc.ID != "" {
				acc.id = tc.ID
			}
			if tc.Function.Name != "" {
				acc.name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				acc.args.WriteString(tc.Function.Arguments)
			}
		}
	}

	// Finalize tool calls if model produced any
	var finalParts []*genai.Part
	if accumulatedText.Len() > 0 {
		finalParts = append(finalParts, &genai.Part{Text: accumulatedText.String()})
	}

	for idx := 0; idx < len(accumulatedTools); idx++ {
		acc := accumulatedTools[idx]
		var args map[string]any
		_ = json.Unmarshal([]byte(acc.args.String()), &args)
		finalParts = append(finalParts, &genai.Part{
			FunctionCall: &genai.FunctionCall{
				ID:   acc.id,
				Name: acc.name,
				Args: args,
			},
		})
	}

	var finalContent *genai.Content
	if len(finalParts) > 0 {
		finalContent = &genai.Content{
			Role:  "model",
			Parts: finalParts,
		}
	}

	yield(&model.LLMResponse{
		Content:      finalContent,
		TurnComplete: true,
	}, scanner.Err())
}
