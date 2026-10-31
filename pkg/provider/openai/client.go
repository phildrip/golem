package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"sort"
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
	Role string `json:"role"`
	// Content is either a plain string or, for a turn that carries images, a
	// content-parts array (OpenAI-compatible multimodal wire format).
	Content          any              `json:"content,omitempty"`
	ReasoningContent *string          `json:"reasoning_content,omitempty"`
	ToolCalls        []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
}

// openAIContentPart is one element of a multimodal content array.
type openAIContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *openAIImageURL `json:"image_url,omitempty"`
}

// openAIImageURL carries an image as an inline data URL.
type openAIImageURL struct {
	URL string `json:"url"`
}

// imageDataURL encodes image bytes as a data: URL, which is what every
// OpenAI-compatible vision API accepts for inline images.
func imageDataURL(mimeType string, data []byte) string {
	if mimeType == "" {
		mimeType = "image/png"
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// multimodalContent builds the content-parts array for a turn with images. The
// text comes first so providers keep the ordering they expect.
func multimodalContent(text string, images []*genai.Blob) []openAIContentPart {
	parts := make([]openAIContentPart, 0, len(images)+1)
	if text != "" {
		parts = append(parts, openAIContentPart{Type: "text", Text: text})
	}
	for _, blob := range images {
		if blob == nil || len(blob.Data) == 0 {
			continue
		}
		parts = append(parts, openAIContentPart{
			Type:     "image_url",
			ImageURL: &openAIImageURL{URL: imageDataURL(blob.MIMEType, blob.Data)},
		})
	}
	return parts
}

type openAIToolCall struct {
	Index    *int                   `json:"index,omitempty"`
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

type openAIChatMessageResponse struct {
	Role             string           `json:"role"`
	Content          string           `json:"content"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	Reasoning        string           `json:"reasoning,omitempty"`
	Thought          string           `json:"thought,omitempty"`
	ToolCalls        []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAIChatChoice struct {
	Message      openAIChatMessageResponse `json:"message"`
	Delta        openAIChatMessageResponse `json:"delta"`
	FinishReason string                    `json:"finish_reason"`
}

type openAIUsage struct {
	PromptTokens         int32 `json:"prompt_tokens"`
	CompletionTokens     int32 `json:"completion_tokens"`
	TotalTokens          int32 `json:"total_tokens"`
	PromptCacheHitTokens int32 `json:"prompt_cache_hit_tokens,omitempty"`
	PromptTokensDetails  *struct {
		CachedTokens int32 `json:"cached_tokens"`
	} `json:"prompt_tokens_details,omitempty"`
}

func toUsageMetadata(u *openAIUsage) *genai.GenerateContentResponseUsageMetadata {
	if u == nil || (u.TotalTokens <= 0 && u.PromptTokens <= 0 && u.CompletionTokens <= 0) {
		return nil
	}
	total := u.TotalTokens
	if total <= 0 {
		total = u.PromptTokens + u.CompletionTokens
	}
	var cached int32
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens > 0 {
		cached = u.PromptTokensDetails.CachedTokens
	} else if u.PromptCacheHitTokens > 0 {
		cached = u.PromptCacheHitTokens
	}
	return &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount:        u.PromptTokens,
		CandidatesTokenCount:    u.CompletionTokens,
		TotalTokenCount:         total,
		CachedContentTokenCount: cached,
	}
}

type openAIChatResponse struct {
	Choices []openAIChatChoice `json:"choices"`
	Usage   *openAIUsage       `json:"usage,omitempty"`
	Error   *struct {
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

		headers := map[string]string{
			"Content-Type": "application/json",
		}
		if c.apiKey != "" {
			headers["Authorization"] = "Bearer " + c.apiKey
		}

		ua := c.userAgent
		if ua == "" {
			ua = DefaultUserAgent
		}
		headers["User-Agent"] = ua

		sid := c.sessionID
		if ctxSid, ok := SessionIDFromContext(ctx); ok && ctxSid != "" {
			sid = ctxSid
		}
		if sid == "" {
			sid = fmt.Sprintf("golem-%d", time.Now().UnixNano())
		}
		headers["x-opencode-session"] = sid

		resp, err := c.doRequestWithRetry(ctx, endpoint, bodyBytes, headers)
		if err != nil {
			yield(nil, err)
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

// Request-level retry bounds, for transient provider failures.
const (
	maxRequestAttempts = 4
	initialRetryDelay  = 500 * time.Millisecond
	maxRetryDelay      = 8 * time.Second
)

func retryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, // 408: the server gave up waiting, the request never ran
		http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// doRequestWithRetry posts body to endpoint, retrying transient network errors
// and retryable HTTP statuses with bounded exponential backoff. A retryable
// status on the final attempt is returned unread so the caller can report its
// body. Cancellation stops immediately.
func (c *Client) doRequestWithRetry(ctx context.Context, endpoint string, body []byte, headers map[string]string) (*http.Response, error) {
	delay := initialRetryDelay
	var lastErr error

	for attempt := 1; attempt <= maxRequestAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, errors.Wrap(err, "creating http request")
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := c.httpClient.Do(req)
		sleep := delay
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = errors.Wrapf(err, "executing request to %s", endpoint)
		case !retryableStatus(resp.StatusCode):
			return resp, nil
		default:
			lastErr = fmt.Errorf("api error %d from %s", resp.StatusCode, endpoint)
			sleep = retryAfter(resp, delay)
			if attempt < maxRequestAttempts {
				// Drain so the keep-alive connection can be reused; a drain
				// failure does not change the retry decision.
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}

		if attempt == maxRequestAttempts {
			if resp != nil && err == nil {
				return resp, nil // caller surfaces the final retryable status body
			}
			return nil, lastErr
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(sleep):
		}
		if delay < maxRetryDelay {
			delay *= 2
		}
	}

	return nil, lastErr
}

// retryAfter honors a Retry-After header (seconds) when present, bounded.
func retryAfter(resp *http.Response, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if v == "" {
		return fallback
	}
	if secs, err := time.ParseDuration(v + "s"); err == nil && secs > 0 {
		if secs > maxRetryDelay {
			return maxRetryDelay
		}
		return secs
	}
	return fallback
}

func isThinkingModel(modelName string) bool {
	m := strings.ToLower(modelName)
	return strings.Contains(m, "deepseek") ||
		strings.Contains(m, "reasoner") ||
		strings.Contains(m, "r1") ||
		strings.Contains(m, "qwq") ||
		strings.Contains(m, "thinking")
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
		var reasoningBuilder strings.Builder
		var toolCalls []openAIToolCall
		var toolResponses []*genai.FunctionResponse
		var images []*genai.Blob

		for _, part := range content.Parts {
			if part == nil {
				continue
			}
			if part.Thought {
				reasoningBuilder.WriteString(part.Text)
			} else if part.Text != "" {
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
			if part.InlineData != nil && len(part.InlineData.Data) > 0 {
				images = append(images, part.InlineData)
			}
		}

		// Tool response messages in OpenAI format
		if len(toolResponses) > 0 {
			for _, tr := range toolResponses {
				respJSON, _ := json.Marshal(tr.Response)
				toolCallID := tr.ID
				if toolCallID == "" {
					// Match against preceding assistant messages' tool calls by function name
					for i := len(chatReq.Messages) - 1; i >= 0; i-- {
						for _, tc := range chatReq.Messages[i].ToolCalls {
							if tc.Function.Name == tr.Name {
								toolCallID = tc.ID
								break
							}
						}
						if toolCallID != "" {
							break
						}
					}
				}
				if toolCallID == "" {
					toolCallID = fmt.Sprintf("call_%s_default", tr.Name)
				}
				chatReq.Messages = append(chatReq.Messages, openAIChatMessage{
					Role:       "tool",
					ToolCallID: toolCallID,
					Content:    string(respJSON),
				})
			}
			continue
		}

		// Standard user or assistant message
		var rcPtr *string
		if reasoningBuilder.Len() > 0 {
			rc := reasoningBuilder.String()
			rcPtr = &rc
		} else if role == "assistant" && len(toolCalls) > 0 && isThinkingModel(c.modelName) {
			emptyRC := ""
			rcPtr = &emptyRC
		}

		msg := openAIChatMessage{
			Role:             role,
			ReasoningContent: rcPtr,
			ToolCalls:        toolCalls,
		}
		if len(images) > 0 && role == "user" {
			// A user turn with images goes out as a content-parts array; there is
			// no way to express an inline image in a plain string.
			msg.Content = multimodalContent(textBuilder.String(), images)
		} else if text := textBuilder.String(); text != "" {
			msg.Content = text
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
				paramsMap, _ := convertSchemaToOpenAI(decl.Parameters)
				chatReq.Tools = append(chatReq.Tools, openAITool{
					Type: "function",
					Function: openAIFunctionSpec{
						Name:        decl.Name,
						Description: decl.Description,
						Parameters:  paramsMap,
					},
				})
			}
		}
	}

	return chatReq, nil
}

func convertSchemaToOpenAI(schema *genai.Schema) (map[string]any, error) {
	if schema == nil {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}, nil
	}
	data, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	lowercaseSchemaTypes(result)
	if _, ok := result["type"]; !ok {
		result["type"] = "object"
	}
	if _, ok := result["properties"]; !ok && result["type"] == "object" {
		result["properties"] = map[string]any{}
	}
	return result, nil
}

func lowercaseSchemaTypes(val any) {
	switch v := val.(type) {
	case map[string]any:
		if t, ok := v["type"]; ok {
			switch tVal := t.(type) {
			case string:
				v["type"] = strings.ToLower(tVal)
			case []any:
				for i, item := range tVal {
					if str, ok := item.(string); ok {
						tVal[i] = strings.ToLower(str)
					}
				}
			}
		}
		for _, child := range v {
			lowercaseSchemaTypes(child)
		}
	case []any:
		for _, child := range v {
			lowercaseSchemaTypes(child)
		}
	}
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

	usage := toUsageMetadata(resp.Usage)

	if len(resp.Choices) == 0 {
		yield(&model.LLMResponse{TurnComplete: true, UsageMetadata: usage}, nil)
		return
	}

	choice := resp.Choices[0]
	msg := choice.Message

	var parts []*genai.Part
	reasoning := msg.ReasoningContent
	if reasoning == "" {
		reasoning = msg.Reasoning
	}
	if reasoning == "" {
		reasoning = msg.Thought
	}
	if reasoning != "" {
		parts = append(parts, &genai.Part{
			Text:    reasoning,
			Thought: true,
		})
	}

	if msg.Content != "" {
		parts = append(parts, &genai.Part{Text: msg.Content})
	}

	for _, tc := range msg.ToolCalls {
		rawArgs := strings.TrimSpace(tc.Function.Arguments)
		args := make(map[string]any)
		if rawArgs != "" && rawArgs != "null" {
			_ = json.Unmarshal([]byte(rawArgs), &args)
		}
		callID := tc.ID
		if callID == "" {
			callID = fmt.Sprintf("call_%s_%d", tc.Function.Name, time.Now().UnixNano())
		}
		parts = append(parts, &genai.Part{
			FunctionCall: &genai.FunctionCall{
				ID:   callID,
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
		Content:       content,
		TurnComplete:  true,
		UsageMetadata: usage,
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
	var accumulatedReasoning strings.Builder
	var lastUsage *genai.GenerateContentResponseUsageMetadata
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimPrefix(line, "data:")
		data = strings.TrimPrefix(data, " ")
		if strings.TrimSpace(data) == "[DONE]" {
			break
		}

		var chunk openAIChatResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if chunk.Error != nil && chunk.Error.Message != "" {
			yield(nil, errors.New(chunk.Error.Message))
			return
		}

		if u := toUsageMetadata(chunk.Usage); u != nil {
			lastUsage = u
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]
		delta := choice.Delta

		// Reasoning / thinking tokens streaming
		reasoningChunk := delta.ReasoningContent
		if reasoningChunk == "" {
			reasoningChunk = delta.Reasoning
		}
		if reasoningChunk == "" {
			reasoningChunk = delta.Thought
		}
		if reasoningChunk != "" {
			accumulatedReasoning.WriteString(reasoningChunk)
			if !yield(&model.LLMResponse{
				Content: &genai.Content{
					Role: "model",
					Parts: []*genai.Part{
						{Text: reasoningChunk, Thought: true},
					},
				},
				Partial: true,
			}, nil) {
				return
			}
		}

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

		// Tool calls streaming: check both delta and message tool calls
		toolCalls := delta.ToolCalls
		if len(toolCalls) == 0 && len(choice.Message.ToolCalls) > 0 {
			toolCalls = choice.Message.ToolCalls
		}

		for idx, tc := range toolCalls {
			toolIdx := idx
			if tc.Index != nil {
				toolIdx = *tc.Index
			}
			acc, ok := accumulatedTools[toolIdx]
			if !ok {
				acc = &toolAccumulator{}
				accumulatedTools[toolIdx] = acc
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

		if choice.FinishReason != "" && choice.FinishReason != "null" {
			// Model has finished generating this turn
			break
		}
	}

	// Finalize parts if model produced reasoning or text
	var finalParts []*genai.Part
	if accumulatedReasoning.Len() > 0 {
		finalParts = append(finalParts, &genai.Part{
			Text:    accumulatedReasoning.String(),
			Thought: true,
		})
	}
	if accumulatedText.Len() > 0 {
		finalParts = append(finalParts, &genai.Part{Text: accumulatedText.String()})
	}

	keys := make([]int, 0, len(accumulatedTools))
	for k := range accumulatedTools {
		keys = append(keys, k)
	}
	sort.Ints(keys)

	for _, k := range keys {
		acc := accumulatedTools[k]
		if acc == nil || acc.name == "" {
			continue
		}
		rawArgs := strings.TrimSpace(acc.args.String())
		args := make(map[string]any)
		if rawArgs != "" && rawArgs != "null" {
			if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
				// Failed to unmarshal
			}
		}
		callID := acc.id
		if callID == "" {
			callID = fmt.Sprintf("call_%s_%d", acc.name, time.Now().UnixNano())
		}
		finalParts = append(finalParts, &genai.Part{
			FunctionCall: &genai.FunctionCall{
				ID:   callID,
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
		Content:       finalContent,
		TurnComplete:  true,
		UsageMetadata: lastUsage,
	}, scanner.Err())
}
