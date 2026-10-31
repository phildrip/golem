package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
)

// Client handles a JSON-RPC 2.0 stdio session with an MCP server subprocess.
type Client struct {
	name       string
	cfg        ServerConfig
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     io.ReadCloser
	stderr     io.ReadCloser
	scanner    *bufio.Scanner
	mu         sync.Mutex
	reqID      atomic.Int64
	pending    map[int64]chan jsonrpcResponse
	tools      []Tool
	serverInfo serverInfo
	running    bool
}

// NewClient initializes a client for the given server config.
func NewClient(name string, cfg ServerConfig) *Client {
	return &Client{
		name:    name,
		cfg:     cfg,
		pending: make(map[int64]chan jsonrpcResponse),
	}
}

// Start spawns the server process, performs the initialize handshake, and lists available tools.
func (c *Client) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.running {
		return nil
	}

	if c.cfg.Command == "" {
		return errors.Errorf("mcp server %q has no command configured", c.name)
	}

	cmd := exec.Command(c.cfg.Command, c.cfg.Args...)

	// Configure environment
	cmd.Env = os.Environ()
	for k, v := range c.cfg.Env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return errors.Wrap(err, "creating stdin pipe")
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return errors.Wrap(err, "creating stdout pipe")
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		stdout.Close()
		return errors.Wrap(err, "creating stderr pipe")
	}

	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		stderr.Close()
		return errors.Wrapf(err, "starting mcp server process %s", c.cfg.Command)
	}

	c.cmd = cmd
	c.stdin = stdin
	c.stdout = stdout
	c.stderr = stderr
	c.scanner = bufio.NewScanner(stdout)
	c.scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	c.running = true

	// Drain stderr in background to avoid blocking
	go func() {
		buf := make([]byte, 2048)
		for {
			_, err := stderr.Read(buf)
			if err != nil {
				return
			}
		}
	}()

	// Reader loop for JSON-RPC responses from stdout
	go c.readLoop()

	// 1. Perform initialize handshake
	initReq := initializeParams{
		ProtocolVersion: "2024-11-05",
		ClientInfo: clientInfo{
			Name:    "golem",
			Version: "2.0.0",
		},
	}

	var initRes initializeResult
	if err := c.callLocked(ctx, "initialize", initReq, &initRes); err != nil {
		c.stopLocked()
		return errors.Wrap(err, "mcp initialize handshake failed")
	}
	c.serverInfo = initRes.ServerInfo

	// 2. Send initialized notification
	_ = c.notifyLocked("notifications/initialized", map[string]any{})

	// 3. Query available tools
	var listRes toolsListResult
	if err := c.callLocked(ctx, "tools/list", map[string]any{}, &listRes); err != nil {
		c.stopLocked()
		return errors.Wrap(err, "mcp tools/list query failed")
	}
	c.tools = listRes.Tools

	return nil
}

// readLoop reads newline-delimited JSON-RPC messages from server stdout.
func (c *Client) readLoop() {
	for c.scanner.Scan() {
		line := c.scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var resp jsonrpcResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			continue
		}

		c.mu.Lock()
		ch, ok := c.pending[resp.ID]
		if ok {
			delete(c.pending, resp.ID)
			c.mu.Unlock()
			ch <- resp
		} else {
			c.mu.Unlock()
		}
	}

	c.mu.Lock()
	c.running = false
	// Wake up any waiting callers
	for id, ch := range c.pending {
		delete(c.pending, id)
		close(ch)
	}
	c.mu.Unlock()
}

// Call executes a JSON-RPC method request and unmarshals the result.
func (c *Client) Call(ctx context.Context, method string, params any, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.callLocked(ctx, method, params, result)
}

func (c *Client) callLocked(ctx context.Context, method string, params any, result any) error {
	if !c.running {
		return errors.Errorf("mcp server %q is not running", c.name)
	}

	id := c.reqID.Add(1)

	var rawParams json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return errors.Wrap(err, "marshaling params")
		}
		rawParams = b
	}

	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  rawParams,
	}

	reqBytes, err := json.Marshal(req)
	if err != nil {
		return errors.Wrap(err, "marshaling request")
	}

	respChan := make(chan jsonrpcResponse, 1)
	c.pending[id] = respChan

	if _, err := fmt.Fprintf(c.stdin, "%s\n", reqBytes); err != nil {
		delete(c.pending, id)
		return errors.Wrap(err, "writing to server stdin")
	}

	c.mu.Unlock()
	var resp jsonrpcResponse
	var ok bool

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		return ctx.Err()
	case resp, ok = <-respChan:
		c.mu.Lock()
		if !ok {
			return errors.Errorf("mcp server %q closed unexpectedly", c.name)
		}
	}

	if resp.Error != nil {
		return errors.Errorf("mcp rpc error %d: %s", resp.Error.Code, resp.Error.Message)
	}

	if result != nil && len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return errors.Wrap(err, "unmarshaling result")
		}
	}

	return nil
}

func (c *Client) notifyLocked(method string, params any) error {
	if !c.running {
		return errors.Errorf("mcp server %q is not running", c.name)
	}

	var rawParams json.RawMessage
	if params != nil {
		b, _ := json.Marshal(params)
		rawParams = b
	}

	notif := jsonrpcNotification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  rawParams,
	}

	b, _ := json.Marshal(notif)
	_, err := fmt.Fprintf(c.stdin, "%s\n", b)
	return err
}

// CallTool invokes an MCP tool on this server.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (CallToolResult, error) {
	params := callToolParams{
		Name:      name,
		Arguments: args,
	}

	var result CallToolResult
	if err := c.Call(ctx, "tools/call", params, &result); err != nil {
		return CallToolResult{}, err
	}
	return result, nil
}

// Tools returns the list of tools announced by the server.
func (c *Client) Tools() []Tool {
	c.mu.Lock()
	defer c.mu.Unlock()
	res := make([]Tool, len(c.tools))
	copy(res, c.tools)
	return res
}

// ServerInfo returns the remote server metadata.
func (c *Client) ServerInfo() serverInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.serverInfo
}

// Name returns the identifier of this server.
func (c *Client) Name() string {
	return c.name
}

// Stop gracefully shuts down the server process.
func (c *Client) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopLocked()
}

func (c *Client) stopLocked() {
	if !c.running {
		return
	}
	c.running = false
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		proc := c.cmd.Process
		_ = proc.Signal(os.Interrupt)
		timer := time.AfterFunc(1*time.Second, func() {
			_ = proc.Kill()
		})
		_ = c.cmd.Wait()
		timer.Stop()
	}
}
