package adapter

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os/exec"
	"sync"
	"time"

	"openpal/internal/util"
)

// ACPMessage ACP ProtocolMessage
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

// ACPSessionUpdate - ACP session Update notification
type ACPSessionUpdate struct {
	SessionID     string     `json:"sessionId"`
	SessionUpdate string     `json:"sessionUpdate"`
	Content       ACPContent `json:"content"`
}

// ACPContent ACP Content
type ACPContent struct {
	Type string `json:"type"` // text, diff, command, etc.
	Text string `json:"text,omitempty"`
}

// ACPClient ACP client
type ACPClient struct {
	provider            string
	cmd                 *exec.Cmd
	cmdName             string // Command name for better error messages
	stdin               io.WriteCloser
	stdout              io.ReadCloser
	reader              *bufio.Reader // Reusable buffered reader for stdout
	sessionID           string
	seq                 int64
	mu                  sync.Mutex
	started             bool              // Track if client has been started
	notificationHandler func(*ACPMessage) // Per-instance notification handler
	customCLIPath       string            // Custom CLI path (optional)
	sessionStore        *ACPSessionStore  // SQLite session persistence
	sessionDir          string            // Directory for session storage
}

// NewACPClient Create ACP client
func NewACPClient(provider, customCLIPath string) (*ACPClient, error) {
	return NewACPClientWithSessionDir(provider, customCLIPath, "")
}

// NewACPClientWithSessionDir Create ACP client with session persistence directory
func NewACPClientWithSessionDir(provider, customCLIPath, sessionDir string) (*ACPClient, error) {
	var cmd *exec.Cmd
	var cmdName string

	// Use custom CLI path if provided, otherwise use default
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
	cmdName = cliPath

	switch provider {
	case "copilot", "copilot-acp":
		cmd = exec.Command(cliPath, "--acp", "--stdio")
	case "opencode":
		cmd = exec.Command(cliPath, "acp")
	default:
		return nil, fmt.Errorf("unsupported ACP provider: %s (supported: copilot, opencode)", provider)
	}

	// Don't start the process here - wait for Start() to be called
	// This allows on-demand CLI startup

	return &ACPClient{
		provider:      provider,
		cmd:           cmd,
		cmdName:       cmdName, // Store command name for better error messages
		customCLIPath: customCLIPath,
		sessionDir:    sessionDir,
		stdin:         nil, // Will be set in Start()
		stdout:        nil, // Will be set in Start()
		seq:           0,
	}, nil
}

// Start Start ACP client (start process and initialize)
func (c *ACPClient) Start() error {
	c.mu.Lock()

	// Prevent double-start
	if c.started {
		c.mu.Unlock()
		util.DebugLog("[DEBUG] ACP client already started, skipping")
		return nil
	}

	// Validate command before starting
	if c.cmd == nil {
		c.mu.Unlock()
		return fmt.Errorf("ACP client command not initialized (provider=%s): check provider configuration", c.provider)
	}

	// Start the process if not already started
	if c.stdin == nil || c.stdout == nil {
		var err error
		c.stdin, err = c.cmd.StdinPipe()
		if err != nil {
			c.mu.Unlock()
			return fmt.Errorf("ACP stdin pipe failed (provider=%s): %w", c.provider, err)
		}

		c.stdout, err = c.cmd.StdoutPipe()
		if err != nil {
			c.stdin.Close()
			c.mu.Unlock()
			return fmt.Errorf("ACP stdout pipe failed (provider=%s): %w", c.provider, err)
		}

		// Create reusable buffered reader for efficient reading
		c.reader = bufio.NewReader(c.stdout)

		if err := c.cmd.Start(); err != nil {
			c.stdin.Close()
			c.stdout.Close()
			c.mu.Unlock()
			// Provide actionable error message
			if err.Error() == "executable file not found in $PATH" {
				return fmt.Errorf("ACP CLI not found: '%s' is not installed or not in PATH (provider=%s). Install the CLI or check PATH configuration", c.cmdName, c.provider)
			}
			return fmt.Errorf("ACP process start failed (provider=%s, cmd=%s): %w", c.provider, c.cmdName, err)
		}

		util.DebugLog("[DEBUG] ACP process started: PID=%d, provider=%s, cmd=%s", c.cmd.Process.Pid, c.provider, c.cmdName)
	}

	// Initialize session store for persistence (if sessionDir is set)
	if c.sessionDir != "" {
		var err error
		c.sessionStore, err = NewACPSessionStore(c.sessionDir, c.provider)
		if err != nil {
			c.mu.Unlock()
			c.stdin.Close()
			c.stdout.Close()
			c.cmd.Process.Kill()
			return fmt.Errorf("ACP session store initialization failed (provider=%s, dir=%s): %w", c.provider, c.sessionDir, err)
		}
		util.DebugLog("[DEBUG] ACP session store initialized for provider=%s", c.provider)
	}

	// Set up notification handler to record session data
	if c.sessionStore != nil {
		c.notificationHandler = func(msg *ACPMessage) {
			if msg.Method == "session/update" && c.sessionID != "" {
				if err := c.sessionStore.RecordSession(c.sessionID, msg); err != nil {
					util.WarnLog("[WARN] ACP session record failed: %v", err)
				}
			}
		}
	}

	// Mark as started BEFORE releasing lock (to prevent re-entrancy during sendRequest)
	c.started = true
	c.mu.Unlock()

	// Send initialize request and read response (MUST be outside lock to avoid deadlock)
	initializeResult := make(map[string]interface{})
	err := c.sendRequest("initialize", map[string]interface{}{
		"protocolVersion":    1, // ACP protocol version (must be <= 65535)
		"clientCapabilities": map[string]interface{}{},
	}, &initializeResult)

	if err != nil {
		// Clean up on initialization failure
		c.mu.Lock()
		c.started = false
		c.mu.Unlock()

		if c.cmd.Process != nil {
			c.cmd.Process.Kill()
		}
		if c.stdin != nil {
			c.stdin.Close()
		}
		if c.stdout != nil {
			c.stdout.Close()
		}
		c.stdout = nil
		c.stdin = nil
		return fmt.Errorf("ACP initialize failed (provider=%s, cmd=%s): %w", c.provider, c.cmdName, err)
	}

	util.DebugLog("[DEBUG] ACP initialized: %+v", initializeResult)
	return nil
}

// NewSession - Create new session
func (c *ACPClient) NewSession(cwd string, mcpServers []interface{}) (string, error) {
	var result struct {
		SessionID string `json:"sessionId"`
	}

	params := map[string]interface{}{
		"cwd":        cwd,
		"mcpServers": mcpServers,
	}

	err := c.sendRequest("session/new", params, &result)
	if err != nil {
		util.WarnLog("[WARN] NewSession failed: %v", err)
		return "", err
	}

	util.DebugLog("[DEBUG] NewSession created: %s", result.SessionID)
	c.sessionID = result.SessionID

	// Record session creation in SQLite
	if c.sessionStore != nil {
		if err := c.sessionStore.RecordContent(result.SessionID, "session_created", "New ACP session created"); err != nil {
			util.WarnLog("[WARN] Failed to record session creation: %v", err)
		}
	}

	return result.SessionID, nil
}

// Prompt Sendprompt
func (c *ACPClient) Prompt(prompt string) error {
	if c.sessionID == "" {
		return fmt.Errorf("no active session")
	}

	params := map[string]interface{}{
		"sessionId": c.sessionID,
		"prompt": []map[string]string{
			{"type": "text", "text": prompt},
		},
	}

	return c.sendRequest("session/prompt", params, nil)
}

func (c *ACPClient) Listen(ctx context.Context, handler func(*ACPMessage)) error {
	reader := c.reader
	if reader == nil {
		return fmt.Errorf("ACP reader not initialized")
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}

		if len(line) == 0 {
			continue
		}

		var msg ACPMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			util.DebugLog("[DEBUG] Listen parse error: %v", err)
			continue
		}

		handler(&msg)
	}
}

// Stop Stop ACP client
func (c *ACPClient) Stop() error {
	if c.cmd != nil && c.cmd.Process != nil {
		if err := c.cmd.Process.Kill(); err != nil {
			return fmt.Errorf("kill ACP process (PID=%d): %w", c.cmd.Process.Pid, err)
		}
	}

	if c.stdin != nil {
		c.stdin.Close()
		c.stdin = nil
	}

	if c.stdout != nil {
		c.stdout.Close()
		c.stdout = nil
	}

	if c.sessionStore != nil {
		if err := c.sessionStore.Close(); err != nil {
			util.WarnLog("[WARN] ACP session store close failed: %v", err)
		}
	}

	return nil
}

// Pid GetProcess ID
func (c *ACPClient) Pid() int {
	if c.cmd != nil {
		return c.cmd.Process.Pid
	}
	return 0
}

// GetSessionID - Get the current session ID (for recovery)
func (c *ACPClient) GetSessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID
}

// GetSessionStore - Get the session store (for recovery)
func (c *ACPClient) GetSessionStore() *ACPSessionStore {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionStore
}

// SetNotificationHandler - Set callback for notifications (called when notifications arrive during request)
// This is a per-instance handler, allowing different handlers for different ACP clients
func (c *ACPClient) SetNotificationHandler(handler func(*ACPMessage)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notificationHandler = handler
}

const sendRequestTimeout = 30 * time.Second

func (c *ACPClient) sendRequest(method string, params interface{}, result interface{}) error {
	c.mu.Lock()
	c.seq++
	id := c.seq

	reader := c.reader
	handler := c.notificationHandler
	c.mu.Unlock()

	msg := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("ACP marshal error (method=%s): %w", method, err)
	}

	if _, err := c.stdin.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("ACP write error (method=%s): stdin closed", method)
	}

	if result == nil {
		return nil
	}

	if reader == nil {
		return fmt.Errorf("ACP reader not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), sendRequestTimeout)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("ACP timeout (method=%s): %w", method, ctx.Err())
		default:
		}

		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				return fmt.Errorf("ACP EOF (method=%s)", method)
			}
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			return fmt.Errorf("ACP read error (method=%s): %w", method, err)
		}

		if len(line) == 0 {
			continue
		}

		var response map[string]interface{}
		if err := json.Unmarshal(line, &response); err != nil {
			return fmt.Errorf("ACP parse error (method=%s)", method)
		}

		idVal, hasID := response["id"]
		if !hasID || idVal == nil {
			if handler != nil {
				if methodVal, ok := response["method"].(string); ok {
					notif := ACPMessage{Method: methodVal}
					if paramsRaw, ok := response["params"]; ok {
						notif.Params, _ = json.Marshal(paramsRaw)
					}
					handler(&notif)
				}
			}
			continue
		}

		if respID, ok := idVal.(float64); !ok || respID != float64(id) {
			continue
		}

		if errMsg, ok := response["error"]; ok && errMsg != nil {
			if errMap, ok := errMsg.(map[string]interface{}); ok {
				return fmt.Errorf("ACP error (method=%s, code=%d): %s", method,
					int(util.GetFloat64(errMap, "code")), util.GetString(errMap, "message"))
			}
			return fmt.Errorf("ACP error (method=%s): %v", method, errMsg)
		}

		if respResult, ok := response["result"]; ok && respResult != nil {
			if resultPtr, ok := result.(*map[string]interface{}); ok {
				if resultMap, ok := respResult.(map[string]interface{}); ok {
					*resultPtr = util.CloneMap(resultMap)
					return nil
				}
			}
			resultData, err := json.Marshal(respResult)
			if err != nil {
				return fmt.Errorf("ACP result marshal error (method=%s): %w", method, err)
			}
			if err := json.Unmarshal(resultData, result); err != nil {
				return fmt.Errorf("ACP result unmarshal error (method=%s): %w", method, err)
			}
		}

		return nil
	}
}

// Note: setReadDeadline removed - was a no-op since pipes don't support deadlines
// The context timeout in sendRequest provides sufficient application-level timeout
// Removing this function eliminates ~5ns call overhead per read iteration

// ParseMessage Parse ACP Messageto Bridge Event
func (c *ACPClient) ParseMessage(msg *ACPMessage) map[string]interface{} {
	// HandleNotification
	if msg.Method == "session/update" {
		var update ACPSessionUpdate
		if err := json.Unmarshal(msg.Params, &update); err != nil {
			return map[string]interface{}{
				"type":    "error",
				"content": fmt.Sprintf("Failed to parse update: %v", err),
			}
		}

		// According to new type, return not same format
		switch update.SessionUpdate {
		case "agent_message_chunk":
			return map[string]interface{}{
				"type":    "chunk",
				"content": update.Content.Text,
				"format":  update.Content.Type, // text, markdown, etc.
			}

		case "agent_state":
			return map[string]interface{}{
				"type":  "status",
				"state": update.Content.Type,
			}

		default:
			return map[string]interface{}{
				"type":    "update",
				"content": update,
			}
		}
	}

	// HandleResponse
	if msg.Result != nil {
		var result map[string]interface{}
		if err := json.Unmarshal(msg.Result, &result); err == nil {
			return map[string]interface{}{
				"type":   "result",
				"result": result,
			}
		}
	}

	// Handle error
	if msg.Error != nil {
		return map[string]interface{}{
			"type":    "error",
			"code":    msg.Error.Code,
			"message": msg.Error.Message,
		}
	}

	// UnknownMessagetype
	return map[string]interface{}{
		"type":    "unknown",
		"message": msg,
	}
}
