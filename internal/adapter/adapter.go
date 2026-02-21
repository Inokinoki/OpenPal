package adapter

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
)

// CLIConfig - CLI configuration
type CLIConfig struct {
	Provider string
	WorkDir  string
	Task     string
	Files    []string
	Options  map[string]string
}

// CLIProcess - CLI process
type CLIProcess struct {
	Cmd    *exec.Cmd
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
	Stderr io.ReadCloser
	Pid    int
}

// Stop - Stop the CLI process
func (c *CLIProcess) Stop() error {
	if c.Cmd != nil && c.Cmd.Process != nil {
		return c.Cmd.Process.Kill()
	}
	return nil
}

// Adapter - CLI adapter interface
type Adapter interface {
	SupportsACP() bool
	SupportsJSONStream() bool // Supports JSON Stream output
	BuildCommand(config *CLIConfig) *exec.Cmd
	ParseMessage(line string) (map[string]interface{}, error)
	SendCommand(cmd string, params map[string]interface{}) error
	GetCapabilities() []string
}

// Manager - Adapter manager
type Manager struct {
	adapter        Adapter
	acpClient      *ACPClient       // ACP client (if supported)
	config         *CLIConfig
	mode           AdapterMode      // ACP or Text mode
	customCLIPath  string           // Custom CLI path
	customCaps     []string         // Custom capabilities
	forceACP       bool             // Force ACP mode
	forceJSON      bool             // Force JSON stream mode
}

// AdapterMode - Adapter mode
type AdapterMode string

const (
	ModeACP  AdapterMode = "acp"  // ACP protocol mode
	ModeText AdapterMode = "text" // Text parsing mode
)

// NewAdapter - Create a new adapter
func NewAdapter(provider, workDir string) *Manager {
	config := &CLIConfig{
		Provider: provider,
		WorkDir:  workDir,
		Options:  make(map[string]string),
	}

	// Check if ACP is supported - create client but DON'T start yet
	// CLI will be started on-demand when start_task is received
	if supportsACP(provider) {
		acpClient, err := NewACPClient(provider)
		if err == nil {
			// Create session info but don't start the process yet
			return &Manager{
				acpClient: acpClient,
				config:    config,
				mode:      ModeACP,  // Mode is ACP, but process not started
			}
		}
		// Fallback to text mode if ACP client creation fails
	}

	// Text mode
	var adapter Adapter
	switch provider {
	case "claude":
		adapter = &ClaudeAdapter{config: config}
	case "codex":
		adapter = &CodexAdapter{config: config}
	case "copilot", "copilot-acp":
		adapter = &CopilotAdapter{config: config}
	default:
		adapter = &GenericAdapter{config: config}
	}

	mgr := &Manager{
		adapter: adapter,
		config:  config,
		mode:    ModeText,
	}

	// Apply configuration from manager to adapter
	if claudeAdapter, ok := adapter.(*ClaudeAdapter); ok {
		claudeAdapter.cliPath = mgr.customCLIPath
		claudeAdapter.caps = mgr.customCaps
		claudeAdapter.forceACP = mgr.forceACP
		claudeAdapter.forceJSON = mgr.forceJSON
	}

	if codexAdapter, ok := adapter.(*CodexAdapter); ok {
		codexAdapter.cliPath = mgr.customCLIPath
		codexAdapter.caps = mgr.customCaps
		codexAdapter.forceACP = mgr.forceACP
		codexAdapter.forceJSON = mgr.forceJSON
	}

	if copilotAdapter, ok := adapter.(*CopilotAdapter); ok {
		copilotAdapter.cliPath = mgr.customCLIPath
		copilotAdapter.caps = mgr.customCaps
		copilotAdapter.forceACP = mgr.forceACP
		copilotAdapter.forceJSON = mgr.forceJSON
	}

	return mgr
}

// SetCLIPath - Set custom CLI executable path
func (m *Manager) SetCLIPath(path string) {
	m.customCLIPath = path
}

// SetCapabilities - Set custom capabilities
func (m *Manager) SetCapabilities(caps []string) {
	m.customCaps = caps
}

// SetTask - Set task description
func (m *Manager) SetTask(task string) {
	m.config.Task = task
}

// EnableACP - Force enable ACP mode
func (m *Manager) EnableACP() {
	m.forceACP = true
}

// EnableJSONStream - Force enable JSON stream mode
func (m *Manager) EnableJSONStream() {
	m.forceJSON = true
}

// supportsACP - Check if provider supports ACP
func supportsACP(provider string) bool {
	switch provider {
	case "copilot", "copilot-acp":
		return true // GitHub Copilot supports ACP
	case "opencode":
		return true // OpenCode supports ACP
	default:
		return false
	}
}

// Start Start - Start CLI
func (m *Manager) Start() (*CLIProcess, error) {
	// ACP mode
	if m.mode == ModeACP && m.acpClient != nil {
		// Start ACP process and initialize
		if err := m.acpClient.Start(); err != nil {
			return nil, err
		}
		
		return &CLIProcess{
			Cmd:    m.acpClient.cmd,
			Stdin:  m.acpClient.stdin,
			Stdout: m.acpClient.stdout,
			Stderr: nil, // ACP usually does not use stderr
			Pid:    m.acpClient.Pid(),
		}, nil
	}

	// Text mode
	cmd := m.adapter.BuildCommand(m.config)
	cmd.Dir = m.config.WorkDir

	log.Printf("[DEBUG] Starting CLI command: %s %v", cmd.Path, cmd.Args)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	log.Printf("[DEBUG] CLI started with PID: %d", cmd.Process.Pid)

	return &CLIProcess{
		Cmd:    cmd,
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
		Pid:    cmd.Process.Pid,
	}, nil
}

// CreateSession Create ACP session (ACP mode only)
func (m *Manager) CreateSession(cwd string) error {
	if m.mode == ModeACP && m.acpClient != nil {
		sessionID, err := m.acpClient.NewSession(cwd, []interface{}{})
		if err != nil {
			log.Printf("[DEBUG] CreateSession: failed to create session: %v", err)
			return err
		}
		log.Printf("[DEBUG] CreateSession: session created: %s", sessionID)
		return err
	}
	return nil // Text mode doesn't need session
}

// SendCommand SendCommand - Send command to CLI
func (m *Manager) SendCommand(cmd string, params map[string]interface{}) error {
	return m.adapter.SendCommand(cmd, params)
}

// GetCapabilities GetCapabilities - Get CLI capabilities
func (m *Manager) GetCapabilities() []string {
	return m.adapter.GetCapabilities()
}

// ClaudeAdapter - Claude Code adapter
type ClaudeAdapter struct {
	config    *CLIConfig
	mu        sync.Mutex
	stdin     io.WriteCloser
	cliPath   string // Custom CLI path
	caps      []string
	forceACP  bool
	forceJSON bool
}

func (a *ClaudeAdapter) SupportsACP() bool {
	// Claude Code does not support ACP, but supports JSON stream output
	return false
}

func (a *ClaudeAdapter) SupportsJSONStream() bool {
	// Claude Code supports --output-format stream-json
	return true
}

func (a *ClaudeAdapter) BuildCommand(config *CLIConfig) *exec.Cmd {
	args := []string{
		// Interactive mode (no -p flag)
		// Allows continuous conversation via stdin/stdout
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
	}

	// File parameters
	for _, file := range config.Files {
		args = append(args, "--add-dir", file)
	}

	// Note: In interactive mode, task is sent via stdin
	// Don't pass it as command line argument

	// Use custom CLI path if specified
	cliPath := a.cliPath
	if cliPath == "" {
		cliPath = "claude"
	}

	log.Printf("[DEBUG] ClaudeAdapter: Building command: %s %v", cliPath, args)
	return exec.Command(cliPath, args...)
}

func (a *ClaudeAdapter) ParseMessage(line string) (map[string]interface{}, error) {
	// Try to parse JSON
	var msg map[string]interface{}
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		// Non-JSON, treat as text
		return a.parseTextOutput(line), nil
	}

	// Handle Claude's stream-json format
	if msgType, ok := msg["type"].(string); ok {
		switch msgType {
		case "stream_event":
			// Extract nested event
			if event, ok := msg["event"].(map[string]interface{}); ok {
				if eventType, ok := event["type"].(string); ok {
					switch eventType {
					case "content_block_delta":
						// Extract text delta
						if delta, ok := event["delta"].(map[string]interface{}); ok {
							if text, ok := delta["text"].(string); ok {
								return map[string]interface{}{
									"type":    "chunk",
									"content": text,
								}, nil
							}
						}
					case "content_block_stop":
						// Content block completed
						return map[string]interface{}{
							"type":    "content_stop",
							"content": "",
						}, nil
					case "message_start":
						// Message started
						return map[string]interface{}{
							"type":    "message_start",
							"content": "",
						}, nil
					case "message_delta":
						// Message delta (usage, stop_reason, etc.)
						return map[string]interface{}{
							"type":    "message_delta",
							"content": "",
							"data":    event,
						}, nil
					case "message_stop":
						// Message completed
						return map[string]interface{}{
							"type":    "message_stop",
							"content": "",
						}, nil
					}
				}
			}
			// Unknown stream_event type
			return map[string]interface{}{
				"type":    "stream_event",
				"content": "",
				"data":    msg,
			}, nil

		case "system":
			// System initialization message
			return map[string]interface{}{
				"type":    "system",
				"content": "Claude initialized",
				"data":    msg,
			}, nil

		case "assistant":
			// Final assistant message
			content := ""
			if message, ok := msg["message"].(map[string]interface{}); ok {
				if contentArr, ok := message["content"].([]interface{}); ok && len(contentArr) > 0 {
					if firstBlock, ok := contentArr[0].(map[string]interface{}); ok {
						if text, ok := firstBlock["text"].(string); ok {
							content = text
						}
					}
				}
			}
			return map[string]interface{}{
				"type":    "assistant",
				"content": content,
				"data":    msg,
			}, nil

		case "result":
			// Final result
			content := ""
			if result, ok := msg["result"].(string); ok {
				content = result
			}
			return map[string]interface{}{
				"type":    "result",
				"content": content,
				"data":    msg,
			}, nil

		default:
			// Other message types
			if _, ok := msg["content"]; !ok {
				msg["content"] = ""
			}
			return msg, nil
		}
	}

	// Default
	return map[string]interface{}{
		"type":    "chunk",
		"content": line,
	}, nil
}

// parseTextOutput parseTextOutput - Parse Claude Code text output
func (a *ClaudeAdapter) parseTextOutput(line string) map[string]interface{} {
	// Identify code blocks
	if strings.HasPrefix(line, "```") {
		return map[string]interface{}{
			"type":    "code_block",
			"content": line,
		}
	}

	// Identify file operations
	lower := strings.ToLower(line)
	if strings.Contains(lower, "editing") ||
		strings.Contains(lower, "creating") ||
		strings.Contains(lower, "deleting") ||
		strings.Contains(lower, "reading") {
		return map[string]interface{}{
			"type":    "file_operation",
			"content": line,
		}
	}

	// Identify command execution
	if strings.Contains(lower, "running") ||
		strings.Contains(lower, "executing") {
		return map[string]interface{}{
			"type":    "command",
			"content": line,
		}
	}

	// Default to text output
	return map[string]interface{}{
		"type":    "chunk",
		"content": line,
	}
}

func (a *ClaudeAdapter) SendCommand(cmd string, params map[string]interface{}) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.stdin == nil {
		return fmt.Errorf("stdin not available")
	}

	command := map[string]interface{}{
		"type":   "command",
		"action": cmd,
		"params": params,
	}

	data, _ := json.Marshal(command)
	_, err := a.stdin.Write(append(data, '\n'))
	return err
}

func (a *ClaudeAdapter) GetCapabilities() []string {
	return []string{"text_output", "file_edit", "multi_turn", "streaming"}
}

// CodexAdapter - Codex CLI adapter
type CodexAdapter struct {
	config     *CLIConfig
	threadID   string // Saved session ID
	sessionDir string // Session directory
	cliPath    string // Custom CLI path
	caps       []string
	forceACP   bool
	forceJSON  bool
}

func (a *CodexAdapter) SupportsACP() bool {
	// According to research, Codex CLI does not support standard ACP format
	return false
}

func (a *CodexAdapter) SupportsJSONStream() bool {
	// Check if codex exec supports --json
	cmd := exec.Command("codex", "exec", "--help")
	output, _ := cmd.CombinedOutput()
	return strings.Contains(string(output), "--json")
}

func (a *CodexAdapter) BuildCommand(config *CLIConfig) *exec.Cmd {
	args := []string{"exec"}

	// Add --json if supported
	if a.SupportsJSONStream() {
		args = append(args, "--json")
	}

	// Use resume to restore session if thread_id exists
	if a.threadID != "" {
		args = append(args, "resume", "--last")
	}

	if config.Task != "" {
		args = append(args, config.Task)
	}

	return exec.Command("codex", args...)
}

func (a *CodexAdapter) ParseMessage(line string) (map[string]interface{}, error) {
	// Try to parse JSON
	var msg map[string]interface{}
	if err := json.Unmarshal([]byte(line), &msg); err == nil {
		if _, ok := msg["type"]; !ok {
			msg["type"] = "chunk"
		}
		return msg, nil
	}

	// Text modeMatch
	parsed := a.parseTextOutput(line)
	return parsed, nil
}

// parseTextOutput Parse Codex TextOutput
func (a *CodexAdapter) parseTextOutput(line string) map[string]interface{} {
	// Similar Claude ParseLogic
	lower := strings.ToLower(line)

	if strings.Contains(lower, "editing") || strings.Contains(lower, "creating") {
		return map[string]interface{}{
			"type":    "file_operation",
			"content": line,
		}
	}

	if strings.Contains(lower, "running") || strings.Contains(lower, "executing") {
		return map[string]interface{}{
			"type":    "command",
			"content": line,
		}
	}

	return map[string]interface{}{
		"type":    "chunk",
		"content": line,
	}
}

func (a *CodexAdapter) SendCommand(cmd string, params map[string]interface{}) error {
	// Codex CLI MayNotSupportinteractiveCommand
	return fmt.Errorf("Codex CLI does not support interactive commands")
}

func (a *CodexAdapter) GetCapabilities() []string {
	return []string{"text_output", "streaming"}
}

// CopilotAdapter GitHub CopilotAdapter - Copilot CLI adapter
type CopilotAdapter struct {
	config    *CLIConfig
	cliPath   string // Custom CLI path
	caps      []string
	forceACP  bool
	forceJSON bool
}

func (a *CopilotAdapter) SupportsACP() bool {
	// Copilot supports ACP, will prioritize ACP mode in NewAdapter
	// IfFallbackto Text Mode，Return false
	return false
}

func (a *CopilotAdapter) SupportsJSONStream() bool {
	// To be confirmed,Return false
	return false
}

func (a *CopilotAdapter) BuildCommand(config *CLIConfig) *exec.Cmd {
	args := []string{"--json-output"}

	if config.Task != "" {
		args = append(args, "--prompt", config.Task)
	}

	return exec.Command("copilot", args...)
}

func (a *CopilotAdapter) ParseMessage(line string) (map[string]interface{}, error) {
	// Try to parse JSON
	var msg map[string]interface{}
	if err := json.Unmarshal([]byte(line), &msg); err == nil {
		if _, ok := msg["type"]; !ok {
			msg["type"] = "chunk"
		}
		return msg, nil
	}

	// Text modeMatch
	parsed := a.parseTextOutput(line)
	return parsed, nil
}

// parseTextOutput Parse Copilot TextOutput
func (a *CopilotAdapter) parseTextOutput(line string) map[string]interface{} {
	lower := strings.ToLower(line)

	if strings.Contains(lower, "editing") || strings.Contains(lower, "creating") {
		return map[string]interface{}{
			"type":    "file_operation",
			"content": line,
		}
	}

	if strings.Contains(lower, "running") || strings.Contains(lower, "executing") {
		return map[string]interface{}{
			"type":    "command",
			"content": line,
		}
	}

	return map[string]interface{}{
		"type":    "chunk",
		"content": line,
	}
}

func (a *CopilotAdapter) SendCommand(cmd string, params map[string]interface{}) error {
	return fmt.Errorf("Copilot CLI does not support interactive commands")
}

func (a *CopilotAdapter) GetCapabilities() []string {
	return []string{"text_output", "streaming"}
}

// GenericAdapter - Generic adapter (for unknown CLI)
type GenericAdapter struct {
	config *CLIConfig
}

func (a *GenericAdapter) SupportsACP() bool {
	return false
}

func (a *GenericAdapter) SupportsJSONStream() bool {
	return false
}

func (a *GenericAdapter) BuildCommand(config *CLIConfig) *exec.Cmd {
	return exec.Command(config.Provider)
}

func (a *GenericAdapter) ParseMessage(line string) (map[string]interface{}, error) {
	return map[string]interface{}{
		"type":    "chunk",
		"content": line,
	}, nil
}

func (a *GenericAdapter) SendCommand(cmd string, params map[string]interface{}) error {
	return fmt.Errorf("Generic adapter does not support commands")
}

func (a *GenericAdapter) GetCapabilities() []string {
	return []string{"text_output"}
}

// StreamForwarder StreamForwarder
type StreamForwarder struct {
	reader  io.Reader
	handler func(string)
	done    chan struct{}
}

// NewStreamForwarder CreateStreamForwarder
func NewStreamForwarder(reader io.Reader, handler func(string)) *StreamForwarder {
	return &StreamForwarder{
		reader:  reader,
		handler: handler,
		done:    make(chan struct{}),
	}
}

// Start - Start forwarding
func (f *StreamForwarder) Start() {
	go func() {
		defer close(f.done)

		scanner := bufio.NewScanner(f.reader)
		for scanner.Scan() {
			f.handler(scanner.Text())
		}
	}()
}

// Done WaitComplete
func (f *StreamForwarder) Done() <-chan struct{} {
	return f.done
}

// ACPMessageHandler ACP MessageHandleer
type ACPMessageHandler struct {
	client  *ACPClient
	handler func(map[string]interface{})
}

// NewACPMessageHandler Create ACP MessageHandleer
func NewACPMessageHandler(client *ACPClient, handler func(map[string]interface{})) *ACPMessageHandler {
	return &ACPMessageHandler{
		client:  client,
		handler: handler,
	}
}

// Start - Start handling ACP messages
func (h *ACPMessageHandler) Start() {
	h.client.Listen(func(msg *ACPMessage) {
		parsed := h.client.ParseMessage(msg)
		h.handler(parsed)
	})
}

// SendCommand SendCommandto ACP Server
func (m *Manager) SendACPPrompt(prompt string) error {
	if m.mode != ModeACP || m.acpClient == nil {
		return fmt.Errorf("ACP mode not enabled")
	}
	return m.acpClient.Prompt(prompt)
}

// GetMode GetCurrentMode
func (m *Manager) GetMode() AdapterMode {
	return m.mode
}
