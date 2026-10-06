package mcp

import "encoding/json"

// JSON-RPC 2.0 primitives
type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonrpcNotification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// MCP Protocol structures
type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type initializeParams struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    clientCapabilities `json:"capabilities"`
	ClientInfo      clientInfo         `json:"clientInfo"`
}

type clientCapabilities struct {
	Roots *struct{} `json:"roots,omitempty"`
}

type initializeResult struct {
	ProtocolVersion string          `json:"protocolVersion"`
	ServerInfo      serverInfo      `json:"serverInfo"`
	Capabilities    json.RawMessage `json:"capabilities"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Tool Definition from tools/list
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type toolsListResult struct {
	Tools []Tool `json:"tools"`
}

// Tool Call params and result
type callToolParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type ToolContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type CallToolResult struct {
	Content []ToolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}
