package adapter

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"openpal/internal/util"
)

// ACP protocol JSON-RPC version used by Copilot and OpenCode.
const acpProtocolVersion = 1

const (
	acpInitTimeout    = 30 * time.Second
	acpSessionTimeout = 30 * time.Second
)

// ACPMessage ACP Protocol Message
type ACPMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *ACPError       `json:"error,omitempty"`
}

// ACPError ACP Error
type ACPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

// ACPSessionUpdate - ACP session Update notification (flattened params used by Copilot/OpenCode)
type ACPSessionUpdate struct {
	SessionID     string          `json:"sessionId"`
	SessionUpdate string          `json:"sessionUpdate"`
	Content       ACPContent      `json:"content"`
	Update        json.RawMessage `json:"update,omitempty"`
}

// ACPContent ACP Content
type ACPContent struct {
	Type string `json:"type"` // text, diff, command, etc.
	Text string `json:"text,omitempty"`
}

// ACPPermissionOption is one choice in session/request_permission.
type ACPPermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

type acpRPCMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *ACPError       `json:"error,omitempty"`
}

type acpPendingCall struct {
	ch chan acpRPCMessage
}

type acpPendingPermission struct {
	id      json.RawMessage
	options []ACPPermissionOption
	params  json.RawMessage
}

// ACPClient ACP client. A single read loop owns stdout; requests wait on pending IDs.
type ACPClient struct {
	provider            string
	cmd                 *exec.Cmd
	cmdName             string
	stdin               io.WriteCloser
	stdout              io.ReadCloser
	stderr              io.ReadCloser
	reader              *bufio.Reader
	ptyMaster           *os.File
	sessionID           string
	seq                 int64
	mu                  sync.Mutex
	writeMu             sync.Mutex
	started             bool
	notificationHandler func(*ACPMessage)
	eventHandler        func(map[string]interface{})
	customCLIPath       string
	workDir             string

	pending     map[string]*acpPendingCall
	permissions []acpPendingPermission
	loopDone    chan struct{}
	cancelLoop  context.CancelFunc
}

// NewACPClient Create ACP client for providers that speak ACP natively
// (Copilot: `copilot --acp --stdio`, OpenCode: `opencode acp`).
// Claude, Codex, and Gemini keep their own CLI protocols and are not ACP clients.
func NewACPClient(provider, customCLIPath string) (*ACPClient, error) {
	cliPath := customCLIPath
	if cliPath == "" {
		switch provider {
		case "copilot", "copilot-acp":
			cliPath = "copilot"
		case "opencode":
			cliPath = "opencode"
		default:
			return nil, fmt.Errorf("unsupported ACP provider: %s (supported: copilot, opencode)", provider)
		}
	}

	var cmd *exec.Cmd
	switch provider {
	case "copilot", "copilot-acp":
		cmd = exec.Command(cliPath, "--acp", "--stdio")
	case "opencode":
		cmd = exec.Command(cliPath, "acp")
	default:
		return nil, fmt.Errorf("unsupported ACP provider: %s (supported: copilot, opencode)", provider)
	}

	return &ACPClient{
		provider:      provider,
		cmd:           cmd,
		cmdName:       cliPath,
		customCLIPath: customCLIPath,
		pending:       make(map[string]*acpPendingCall),
	}, nil
}

// SetWorkDir sets the working directory used when spawning the agent process.
func (c *ACPClient) SetWorkDir(dir string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.workDir = dir
}

// Start Start ACP client (spawn process, start read loop, initialize)
func (c *ACPClient) Start() error {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		util.DebugLog("[DEBUG] ACP client already started, skipping")
		return nil
	}
	if c.cmd == nil {
		c.mu.Unlock()
		return fmt.Errorf("ACP client command not initialized (provider=%s): check provider configuration", c.provider)
	}
	if c.pending == nil {
		c.pending = make(map[string]*acpPendingCall)
	}

	if err := c.spawnLocked(); err != nil {
		c.mu.Unlock()
		return err
	}

	loopCtx, cancel := context.WithCancel(context.Background())
	c.cancelLoop = cancel
	c.loopDone = make(chan struct{})
	c.started = true
	c.mu.Unlock()

	go c.readLoop(loopCtx)
	if c.stderr != nil {
		go c.drainStderr()
	}

	initResult := make(map[string]interface{})
	err := c.sendRequest("initialize", map[string]interface{}{
		"protocolVersion": acpProtocolVersion,
		"clientCapabilities": map[string]interface{}{
			// Do not advertise fs/terminal: agents use their own tools.
			"fs": map[string]interface{}{
				"readTextFile":  false,
				"writeTextFile": false,
			},
			"terminal": false,
		},
		"clientInfo": map[string]interface{}{
			"name":    "openpal",
			"version": "dev",
		},
	}, &initResult, acpInitTimeout)
	if err != nil {
		c.Stop()
		return fmt.Errorf("ACP initialize failed (provider=%s, cmd=%s): %w", c.provider, c.cmdName, err)
	}

	util.DebugLog("[DEBUG] ACP initialized: %+v", initResult)
	return nil
}

func (c *ACPClient) spawnLocked() error {
	if c.workDir != "" {
		c.cmd.Dir = c.workDir
	}
	c.cmd.Env = os.Environ()

	if c.provider == "opencode" {
		var err error
		c.ptyMaster, err = pty.Start(c.cmd)
		if err != nil {
			if isExecNotFound(err) {
				return fmt.Errorf("ACP CLI not found: '%s' is not installed or not in PATH (provider=%s). Install the CLI or check PATH configuration", c.cmdName, c.provider)
			}
			return fmt.Errorf("ACP PTY start failed (provider=%s, cmd=%s): %w", c.provider, c.cmdName, err)
		}
		c.stdin = c.ptyMaster
		c.stdout = c.ptyMaster
		c.reader = bufio.NewReader(c.ptyMaster)
		util.DebugLog("[DEBUG] ACP process started with PTY: PID=%d, provider=%s, cmd=%s", c.cmd.Process.Pid, c.provider, c.cmdName)
		return nil
	}

	stdin, err := c.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("ACP stdin pipe failed (provider=%s): %w", c.provider, err)
	}
	stdout, err := c.cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return fmt.Errorf("ACP stdout pipe failed (provider=%s): %w", c.provider, err)
	}
	stderr, err := c.cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		stdout.Close()
		return fmt.Errorf("ACP stderr pipe failed (provider=%s): %w", c.provider, err)
	}

	if err := c.cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		stderr.Close()
		if isExecNotFound(err) {
			return fmt.Errorf("ACP CLI not found: '%s' is not installed or not in PATH (provider=%s). Install the CLI or check PATH configuration", c.cmdName, c.provider)
		}
		return fmt.Errorf("ACP process start failed (provider=%s, cmd=%s): %w", c.provider, c.cmdName, err)
	}

	c.stdin = stdin
	c.stdout = stdout
	c.stderr = stderr
	c.reader = bufio.NewReader(stdout)
	util.DebugLog("[DEBUG] ACP process started: PID=%d, provider=%s, cmd=%s", c.cmd.Process.Pid, c.provider, c.cmdName)
	return nil
}

func isExecNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "executable file not found")
}

// NewSession - Create new session
func (c *ACPClient) NewSession(cwd string, mcpServers []interface{}) (string, error) {
	if mcpServers == nil {
		mcpServers = []interface{}{}
	}
	var result struct {
		SessionID string `json:"sessionId"`
	}
	params := map[string]interface{}{
		"cwd":        cwd,
		"mcpServers": mcpServers,
	}
	if err := c.sendRequest("session/new", params, &result, acpSessionTimeout); err != nil {
		util.DebugLog("[DEBUG] NewSession failed: %v", err)
		return "", err
	}
	c.mu.Lock()
	c.sessionID = result.SessionID
	c.mu.Unlock()
	util.DebugLog("[DEBUG] NewSession created: %s", result.SessionID)
	return result.SessionID, nil
}

// LoadSession resumes an existing ACP session when the agent advertises loadSession.
func (c *ACPClient) LoadSession(sessionID, cwd string) error {
	params := map[string]interface{}{
		"sessionId":  sessionID,
		"cwd":        cwd,
		"mcpServers": []interface{}{},
	}
	var result map[string]interface{}
	if err := c.sendRequest("session/load", params, &result, acpSessionTimeout); err != nil {
		return err
	}
	c.mu.Lock()
	c.sessionID = sessionID
	c.mu.Unlock()
	return nil
}

// Prompt sends session/prompt. Updates stream via the notification handler; this
// call returns once the request is written so the WebSocket loop stays responsive.
func (c *ACPClient) Prompt(prompt string) error {
	c.mu.Lock()
	sessionID := c.sessionID
	c.mu.Unlock()
	if sessionID == "" {
		return fmt.Errorf("no active session")
	}

	params := map[string]interface{}{
		"sessionId": sessionID,
		"prompt": []map[string]string{
			{"type": "text", "text": prompt},
		},
	}

	go func() {
		var result map[string]interface{}
		if err := c.sendRequest("session/prompt", params, &result, 0); err != nil {
			util.DebugLog("[DEBUG] session/prompt failed: %v", err)
			c.emitEvent(map[string]interface{}{
				"type":    "error",
				"content": err.Error(),
			})
			return
		}
		c.emitEvent(map[string]interface{}{
			"type":   "result",
			"result": result,
		})
	}()
	return nil
}

// Cancel sends session/cancel and rejects outstanding permission requests.
func (c *ACPClient) Cancel() error {
	c.mu.Lock()
	sessionID := c.sessionID
	c.mu.Unlock()
	if sessionID == "" {
		return nil
	}

	c.rejectPendingPermissions()
	return c.sendNotification("session/cancel", map[string]interface{}{
		"sessionId": sessionID,
	})
}

// RespondPermission answers the oldest pending session/request_permission.
func (c *ACPClient) RespondPermission(approve bool) error {
	c.mu.Lock()
	if len(c.permissions) == 0 {
		c.mu.Unlock()
		return fmt.Errorf("no pending ACP permission request")
	}
	p := c.permissions[0]
	c.permissions = c.permissions[1:]
	c.mu.Unlock()

	outcome := permissionOutcome(p.options, approve)
	return c.sendResponse(p.id, map[string]interface{}{
		"outcome": outcome,
	})
}

func permissionOutcome(options []ACPPermissionOption, approve bool) map[string]interface{} {
	wantAllow := map[string]bool{"allow_once": true, "allow_always": true, "allow": true}
	wantReject := map[string]bool{"reject_once": true, "reject_always": true, "reject": true}
	want := wantReject
	if approve {
		want = wantAllow
	}
	for _, opt := range options {
		if want[strings.ToLower(opt.Kind)] || (approve && strings.HasPrefix(strings.ToLower(opt.OptionID), "allow")) ||
			(!approve && strings.HasPrefix(strings.ToLower(opt.OptionID), "reject")) {
			return map[string]interface{}{
				"outcome":  "selected",
				"optionId": opt.OptionID,
			}
		}
	}
	if len(options) > 0 && approve {
		return map[string]interface{}{
			"outcome":  "selected",
			"optionId": options[0].OptionID,
		}
	}
	return map[string]interface{}{"outcome": "cancelled"}
}

func (c *ACPClient) rejectPendingPermissions() {
	c.mu.Lock()
	pending := c.permissions
	c.permissions = nil
	c.mu.Unlock()
	for _, p := range pending {
		_ = c.sendResponse(p.id, map[string]interface{}{
			"outcome": map[string]interface{}{"outcome": "cancelled"},
		})
	}
}

// Listen is retained for tests. Production uses the internal read loop.
func (c *ACPClient) Listen(ctx context.Context, handler func(*ACPMessage)) error {
	c.mu.Lock()
	reader := c.reader
	c.mu.Unlock()
	if reader == nil {
		return fmt.Errorf("ACP reader not initialized")
	}
	if handler != nil {
		c.SetNotificationHandler(handler)
	}
	<-ctx.Done()
	return ctx.Err()
}

// Stop Stop ACP client
func (c *ACPClient) Stop() error {
	_ = c.Cancel()

	c.mu.Lock()
	if c.cancelLoop != nil {
		c.cancelLoop()
	}
	loopDone := c.loopDone
	c.failPendingLocked(fmt.Errorf("ACP client stopped"))
	c.started = false
	c.sessionID = ""
	c.mu.Unlock()

	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	if c.ptyMaster != nil {
		_ = c.ptyMaster.Close()
		c.ptyMaster = nil
	}
	if c.stdin != nil && c.stdin != c.ptyMaster {
		c.stdin.Close()
		c.stdin = nil
	}
	if c.stdout != nil && c.stdout != c.ptyMaster {
		c.stdout.Close()
		c.stdout = nil
	}
	if c.stderr != nil {
		c.stderr.Close()
		c.stderr = nil
	}
	if loopDone != nil {
		select {
		case <-loopDone:
		case <-time.After(2 * time.Second):
		}
	}
	return nil
}

// Pid GetProcess ID
func (c *ACPClient) Pid() int {
	if c.cmd != nil && c.cmd.Process != nil {
		return c.cmd.Process.Pid
	}
	return 0
}

// GetReader - Get the shared buffered reader for ACP output
func (c *ACPClient) GetReader() io.Reader {
	if c.reader != nil {
		return c.reader
	}
	return c.stdout
}

// GetSessionID - Get the current session ID (for recovery)
func (c *ACPClient) GetSessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID
}

// SetNotificationHandler - Set callback for notifications
func (c *ACPClient) SetNotificationHandler(handler func(*ACPMessage)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notificationHandler = handler
}

// SetEventHandler sets the parsed-event callback used by the WebSocket server.
func (c *ACPClient) SetEventHandler(handler func(map[string]interface{})) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.eventHandler = handler
}

func (c *ACPClient) sendRequest(method string, params interface{}, result interface{}, timeout time.Duration) error {
	c.mu.Lock()
	c.seq++
	id := c.seq
	idKey := strconv.FormatInt(id, 10)
	call := &acpPendingCall{ch: make(chan acpRPCMessage, 1)}
	if c.pending == nil {
		c.pending = make(map[string]*acpPendingCall)
	}
	c.pending[idKey] = call
	c.mu.Unlock()

	msg := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	if err := c.writeJSON(msg); err != nil {
		c.mu.Lock()
		delete(c.pending, idKey)
		c.mu.Unlock()
		return fmt.Errorf("ACP write error (method=%s): %w", method, err)
	}

	wait := timeout
	if wait <= 0 {
		wait = 24 * time.Hour
	}

	select {
	case resp, ok := <-call.ch:
		if !ok {
			return fmt.Errorf("ACP closed (method=%s)", method)
		}
		if resp.Error != nil {
			return fmt.Errorf("ACP error (method=%s, code=%d): %s", method, resp.Error.Code, resp.Error.Message)
		}
		if result == nil || len(resp.Result) == 0 || string(resp.Result) == "null" {
			return nil
		}
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return fmt.Errorf("ACP result unmarshal error (method=%s): %w", method, err)
		}
		return nil
	case <-time.After(wait):
		c.mu.Lock()
		delete(c.pending, idKey)
		c.mu.Unlock()
		return fmt.Errorf("ACP timeout (method=%s)", method)
	}
}

func (c *ACPClient) sendNotification(method string, params interface{}) error {
	return c.writeJSON(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

func (c *ACPClient) sendResponse(id json.RawMessage, result interface{}) error {
	return c.writeJSON(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      jsonRawOrNil(id),
		"result":  result,
	})
}

func (c *ACPClient) sendError(id json.RawMessage, code int, message string) error {
	return c.writeJSON(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      jsonRawOrNil(id),
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
	})
}

func jsonRawOrNil(id json.RawMessage) interface{} {
	if len(id) == 0 {
		return nil
	}
	var v interface{}
	if err := json.Unmarshal(id, &v); err != nil {
		return nil
	}
	return v
}

func (c *ACPClient) writeJSON(msg interface{}) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.stdin == nil {
		return fmt.Errorf("stdin closed")
	}
	_, err = c.stdin.Write(append(data, '\n'))
	return err
}

func (c *ACPClient) readLoop(ctx context.Context) {
	defer close(c.loopDone)

	c.mu.Lock()
	reader := c.reader
	c.mu.Unlock()
	if reader == nil {
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err != io.EOF {
				util.DebugLog("[DEBUG] ACP read loop error: %v", err)
			}
			c.mu.Lock()
			c.failPendingLocked(fmt.Errorf("ACP EOF"))
			c.mu.Unlock()
			return
		}
		line = bytesTrimSpace(line)
		if len(line) == 0 {
			continue
		}
		c.dispatchLine(line)
	}
}

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

func (c *ACPClient) dispatchLine(line []byte) {
	var msg acpRPCMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		util.DebugLog("[DEBUG] ACP parse error: %v", err)
		return
	}

	idKey := jsonIDKey(msg.ID)
	hasID := idKey != ""
	hasMethod := msg.Method != ""
	hasResult := len(msg.Result) > 0 || msg.Error != nil

	// OpenCode echoes client requests; skip those.
	if hasID && hasMethod && !hasResult {
		c.mu.Lock()
		_, ours := c.pending[idKey]
		c.mu.Unlock()
		if ours {
			return
		}
	}

	if hasID && !hasMethod {
		c.mu.Lock()
		call, ok := c.pending[idKey]
		if ok {
			delete(c.pending, idKey)
		}
		c.mu.Unlock()
		if ok {
			select {
			case call.ch <- msg:
			default:
			}
		}
		return
	}

	if hasMethod && hasID {
		c.handleIncomingRequest(msg)
		return
	}

	if hasMethod {
		c.handleNotification(msg)
	}
}

func (c *ACPClient) handleIncomingRequest(msg acpRPCMessage) {
	switch msg.Method {
	case "session/request_permission":
		c.handlePermissionRequest(msg)
	default:
		util.DebugLog("[DEBUG] ACP unsupported agent method %s", msg.Method)
		_ = c.sendError(msg.ID, -32601, "Method not found")
	}
}

func (c *ACPClient) handlePermissionRequest(msg acpRPCMessage) {
	var params struct {
		SessionID string                `json:"sessionId"`
		Options   []ACPPermissionOption `json:"options"`
		ToolCall  json.RawMessage       `json:"toolCall"`
	}
	_ = json.Unmarshal(msg.Params, &params)

	c.mu.Lock()
	c.permissions = append(c.permissions, acpPendingPermission{
		id:      append(json.RawMessage(nil), msg.ID...),
		options: params.Options,
		params:  msg.Params,
	})
	c.mu.Unlock()

	event := map[string]interface{}{
		"type":       "permission_request",
		"session_id": params.SessionID,
		"options":    params.Options,
	}
	if len(params.ToolCall) > 0 {
		var tool interface{}
		if json.Unmarshal(params.ToolCall, &tool) == nil {
			event["tool_call"] = tool
		}
	}
	c.emitEvent(event)
}

func (c *ACPClient) handleNotification(msg acpRPCMessage) {
	acpMsg := &ACPMessage{
		JSONRPC: msg.JSONRPC,
		Method:  msg.Method,
		Params:  msg.Params,
		Result:  msg.Result,
		Error:   msg.Error,
	}
	c.mu.Lock()
	handler := c.notificationHandler
	c.mu.Unlock()
	if handler != nil {
		handler(acpMsg)
	}
	c.emitEvent(c.ParseMessage(acpMsg))
}

func (c *ACPClient) emitEvent(event map[string]interface{}) {
	if event == nil {
		return
	}
	c.mu.Lock()
	h := c.eventHandler
	c.mu.Unlock()
	if h != nil {
		h(event)
	}
}

func (c *ACPClient) failPendingLocked(err error) {
	for key, call := range c.pending {
		select {
		case call.ch <- acpRPCMessage{Error: &ACPError{Code: -32000, Message: err.Error()}}:
		default:
		}
		delete(c.pending, key)
	}
}

func (c *ACPClient) drainStderr() {
	if c.stderr == nil {
		return
	}
	scanner := bufio.NewScanner(c.stderr)
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			util.DebugLog("[DEBUG] ACP stderr: %s", line)
		}
	}
}

func jsonIDKey(id json.RawMessage) string {
	if len(id) == 0 || string(id) == "null" {
		return ""
	}
	var n json.Number
	if err := json.Unmarshal(id, &n); err == nil {
		return n.String()
	}
	var s string
	if err := json.Unmarshal(id, &s); err == nil {
		return s
	}
	return strings.TrimSpace(string(id))
}

// ParseMessage Parse ACP Message to Bridge Event
func (c *ACPClient) ParseMessage(msg *ACPMessage) map[string]interface{} {
	if msg == nil {
		return map[string]interface{}{"type": "unknown"}
	}

	if msg.Method == "session/update" {
		return parseSessionUpdate(msg.Params)
	}

	if msg.Result != nil {
		var result map[string]interface{}
		if err := json.Unmarshal(msg.Result, &result); err == nil {
			return map[string]interface{}{
				"type":   "result",
				"result": result,
			}
		}
	}

	if msg.Error != nil {
		return map[string]interface{}{
			"type":    "error",
			"code":    msg.Error.Code,
			"message": msg.Error.Message,
		}
	}

	return map[string]interface{}{
		"type":    "unknown",
		"message": msg,
	}
}

func parseSessionUpdate(params json.RawMessage) map[string]interface{} {
	if len(params) == 0 {
		return map[string]interface{}{
			"type":    "error",
			"content": "Failed to parse update: empty params",
		}
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(params, &raw); err != nil {
		return map[string]interface{}{
			"type":    "error",
			"content": fmt.Sprintf("Failed to parse update: %v", err),
		}
	}

	updateType, content := sessionUpdateFields(raw)
	switch updateType {
	case "agent_message_chunk", "agent_thought_chunk":
		text, format := contentText(content)
		eventType := "chunk"
		if updateType == "agent_thought_chunk" {
			eventType = "thinking"
		}
		return map[string]interface{}{
			"type":    eventType,
			"content": text,
			"format":  format,
		}
	case "agent_state":
		stateType := ""
		if m, ok := content.(map[string]interface{}); ok {
			stateType, _ = m["type"].(string)
		}
		return map[string]interface{}{
			"type":  "status",
			"state": stateType,
		}
	case "tool_call", "tool_call_update":
		return map[string]interface{}{
			"type":    updateType,
			"content": content,
			"data":    raw,
		}
	default:
		if updateType == "" {
			return map[string]interface{}{
				"type":    "update",
				"content": raw,
			}
		}
		return map[string]interface{}{
			"type":    "update",
			"content": raw,
		}
	}
}

func sessionUpdateFields(raw map[string]interface{}) (string, interface{}) {
	if nested, ok := raw["update"].(map[string]interface{}); ok {
		t, _ := nested["sessionUpdate"].(string)
		return t, nested["content"]
	}
	t, _ := raw["sessionUpdate"].(string)
	return t, raw["content"]
}

func contentText(content interface{}) (string, string) {
	m, ok := content.(map[string]interface{})
	if !ok {
		return "", ""
	}
	text, _ := m["text"].(string)
	format, _ := m["type"].(string)
	return text, format
}
