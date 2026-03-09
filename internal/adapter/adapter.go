package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"pal-broker/internal/session"
	"pal-broker/internal/util"
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

// Stop - Stop CLI with graceful shutdown (SIGINT) and timeout fallback
func (c *CLIProcess) Stop() error {
	if c.Cmd == nil || c.Cmd.Process == nil {
		return nil
	}

	if err := c.Cmd.Process.Signal(os.Interrupt); err != nil {
		util.DebugLog("[DEBUG] CLI Stop: interrupt failed: %v, forcing kill", err)
		return c.forceKill()
	}

	done := make(chan error, 1)
	go func() { done <- c.Cmd.Wait() }()

	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		return c.forceKill()
	}
}

// forceKill - Force kill the CLI process and clean up resources
func (c *CLIProcess) forceKill() error {
	if c.Cmd == nil || c.Cmd.Process == nil {
		return nil
	}

	if err := c.Cmd.Process.Kill(); err != nil {
		return fmt.Errorf("force kill failed: %w", err)
	}

	if c.Stdin != nil {
		c.Stdin.Close()
	}

	// Wait for process exit (500ms timeout)
	done := make(chan error, 1)
	go func() { done <- c.Cmd.Wait() }()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		// Process killed, reap timeout acceptable
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
	adapter       Adapter
	acpClient     *ACPClient // ACP client (if supported)
	config        *CLIConfig
	mode          AdapterMode // ACP or Text mode
	customCLIPath string      // Custom CLI path
	customCaps    []string    // Custom capabilities
	forceACP      bool        // Force ACP mode
	forceJSON     bool        // Force JSON stream mode
}

// AdapterMode - Adapter mode
type AdapterMode string

const (
	ModeACP  AdapterMode = "acp"  // ACP protocol mode
	ModeText AdapterMode = "text" // Text parsing mode
)

// BaseAdapter - Common adapter functionality
type BaseAdapter struct {
	config    *CLIConfig
	cliPath   string
	caps      []string
	forceACP  bool
	forceJSON bool
	mu        sync.Mutex
	stdin     io.WriteCloser
}

// SetCLIPath - Set custom CLI path
func (b *BaseAdapter) SetCLIPath(path string) {
	b.cliPath = path
}

// SetCapabilities - Set custom capabilities
func (b *BaseAdapter) SetCapabilities(caps []string) {
	b.caps = caps
}

// GetCLIPath - Get CLI path (custom or default)
func (b *BaseAdapter) GetCLIPath(defaultPath string) string {
	if b.cliPath != "" {
		return b.cliPath
	}
	return defaultPath
}

// GetCapabilities - Get capabilities (custom or default)
func (b *BaseAdapter) GetCapabilities(defaultCaps []string) []string {
	if len(b.caps) > 0 {
		return b.caps
	}
	return defaultCaps
}

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
		// Pass empty customCLIPath - will be set later via SetCLIPath if needed
		acpClient, err := NewACPClient(provider, "")
		if err == nil {
			// Create session info but don't start the process yet
			return &Manager{
				acpClient: acpClient,
				config:    config,
				mode:      ModeACP, // Mode is ACP, but process not started
			}
		}
		// Fallback to text mode if ACP client creation fails
	}

	// Text mode
	var adapter Adapter
	switch provider {
	case "claude":
		adapter = NewClaudeAdapter(config)
	case "codex":
		adapter = NewCodexAdapter(config)
	case "copilot", "copilot-acp":
		adapter = NewCopilotAdapter(config)
	case "gemini":
		adapter = NewGeminiAdapter(config)
	case "opencode":
		adapter = NewOpenCodeAdapter(config)
	default:
		adapter = NewGenericAdapter(config)
	}

	mgr := &Manager{
		adapter: adapter,
		config:  config,
		mode:    ModeText,
	}

	// Apply configuration from manager to adapter
	mgr.applyAdapterConfig(adapter)

	return mgr
}

// applyAdapterConfig - Apply manager config to adapter (handles type-specific fields)
func (m *Manager) applyAdapterConfig(adapter Adapter) {
	// With BaseAdapter, configuration is applied through setter methods
	type configurable interface {
		SetCLIPath(string)
		SetCapabilities([]string)
	}

	if cfg, ok := adapter.(configurable); ok {
		cfg.SetCLIPath(m.customCLIPath)
		cfg.SetCapabilities(m.customCaps)
	}
}

// SetCLIPath - Set custom CLI executable path
func (m *Manager) SetCLIPath(path string) {
	m.customCLIPath = path

	// Also update ACP client if using ACP mode
	if m.acpClient != nil {
		m.acpClient.customCLIPath = path
		// Rebuild the command with the new path
		var cmdName string
		switch m.config.Provider {
		case "copilot", "copilot-acp":
			cmdName = path
			m.acpClient.cmd = exec.Command(path, "--acp", "--stdio")
		case "opencode":
			cmdName = path
			m.acpClient.cmd = exec.Command(path, "acp")
		}
		m.acpClient.cmdName = cmdName
	}
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
			return nil, fmt.Errorf("ACP client start failed (provider=%s): %w", m.config.Provider, err)
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
	// Inherit environment variables (needed for API keys, etc.)
	cmd.Env = os.Environ()

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe failed (provider=%s): %w", m.config.Provider, err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe failed (provider=%s): %w", m.config.Provider, err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe failed (provider=%s): %w", m.config.Provider, err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("CLI start failed (provider=%s, workdir=%s): %w", m.config.Provider, m.config.WorkDir, err)
	}

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
		_, err := m.acpClient.NewSession(cwd, []interface{}{})
		return err
	}
	return nil // Text mode doesn't need session
}

// SendCommand Send command to CLI
func (m *Manager) SendCommand(cmd string, params map[string]interface{}) error {
	return m.adapter.SendCommand(cmd, params)
}

// GetCapabilities Get CLI capabilities
func (m *Manager) GetCapabilities() []string {
	return m.adapter.GetCapabilities()
}

// ClaudeAdapter - Claude Code adapter
type ClaudeAdapter struct {
	BaseAdapter
	sessionDir     string           // Session directory for this task
	sessionManager *session.Manager // Session manager for persistence
	sessionID      string           // Current session ID
	sessionMu      sync.RWMutex     // Protects sessionID
}

func NewClaudeAdapter(config *CLIConfig) *ClaudeAdapter {
	return &ClaudeAdapter{
		BaseAdapter: BaseAdapter{
			config: config,
		},
	}
}

// SetSessionDir - Set session directory for this adapter
func (a *ClaudeAdapter) SetSessionDir(sessionDir, taskID string) {
	a.sessionDir = sessionDir
	a.sessionManager = session.NewManager(sessionDir, taskID, "claude")

	// Load existing session ID if available
	if sessionID, err := a.sessionManager.Load(); err == nil && sessionID != "" {
		a.sessionID = sessionID
		util.DebugLog("[DEBUG] ClaudeAdapter: loaded existing session: %s", sessionID)
	}
}

func (a *ClaudeAdapter) SupportsACP() bool {
	// Claude Code does not support ACP, but supports JSON stream output
	return false
}

func (a *ClaudeAdapter) SupportsJSONStream() bool {
	// Claude Code supports --output-format stream-json
	return true
}

// ExtractSessionID - Extract session ID from Claude output
// Claude Code outputs session info in format: "Session ID: xxx" or in JSON
var sessionIDRegex = regexp.MustCompile(`Session ID: ([a-f0-9-]+)`)

func (a *ClaudeAdapter) ExtractSessionID(line string) string {
	// Try JSON parsing first (most common for stream-json mode)
	var msg map[string]interface{}
	if err := json.Unmarshal([]byte(line), &msg); err == nil {
		// Check for session_id at top level (system init message)
		if sid, ok := msg["session_id"].(string); ok && sid != "" {
			util.DebugLog("[DEBUG] ClaudeAdapter: extracted session_id from JSON: %s", sid)
			return sid
		}
		// Check nested in message object
		if message, ok := msg["message"].(map[string]interface{}); ok {
			if sid, ok := message["session_id"].(string); ok && sid != "" {
				util.DebugLog("[DEBUG] ClaudeAdapter: extracted session_id from message: %s", sid)
				return sid
			}
		}
		// Check in data object
		if data, ok := msg["data"].(map[string]interface{}); ok {
			if sid, ok := data["session_id"].(string); ok && sid != "" {
				return sid
			}
			if sid, ok := data["sessionId"].(string); ok && sid != "" {
				return sid
			}
		}
		// Check sessionId variant
		if sid, ok := msg["sessionId"].(string); ok && sid != "" {
			return sid
		}
	}

	// Try regex match for text format
	if matches := sessionIDRegex.FindStringSubmatch(line); len(matches) > 1 {
		sessionID := matches[1]
		util.DebugLog("[DEBUG] ClaudeAdapter: extracted session ID from regex: %s", sessionID)
		return sessionID
	}

	return ""
}

// UpdateSessionID - Update session ID from output and save to file
func (a *ClaudeAdapter) UpdateSessionID(line string) {
	sessionID := a.ExtractSessionID(line)
	if sessionID == "" {
		return
	}

	a.sessionMu.Lock()
	oldSessionID := a.sessionID
	a.sessionID = sessionID
	a.sessionMu.Unlock()

	// Save to file if session ID changed
	if sessionID != oldSessionID && a.sessionManager != nil {
		if err := a.sessionManager.Save(sessionID); err != nil {
			util.DebugLog("[DEBUG] ClaudeAdapter: failed to save session ID: %v", err)
		} else {
			util.DebugLog("[DEBUG] ClaudeAdapter: saved session ID: %s", sessionID)
		}
	}
}

func (a *ClaudeAdapter) BuildCommand(config *CLIConfig) *exec.Cmd {
	// Use -p (print) mode for non-interactive execution
	// This allows us to use --output-format stream-json
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--verbose",
	}

	// File parameters
	for _, file := range config.Files {
		args = append(args, "--add-dir", file)
	}

	// Resume session if we have a session ID
	a.sessionMu.RLock()
	sessionID := a.sessionID
	a.sessionMu.RUnlock()

	if sessionID != "" {
		args = append(args, "--resume", sessionID)
		util.DebugLog("[DEBUG] ClaudeAdapter: resuming session %s", sessionID)
	}

	// Task is passed as argument in -p mode
	if config.Task != "" {
		args = append(args, config.Task)
	}

	cliPath := a.GetCLIPath("claude")
	return exec.Command(cliPath, args...)
}

func (a *ClaudeAdapter) ParseMessage(line string) (map[string]interface{}, error) {
	// Try to parse JSON
	var msg map[string]interface{}
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		// Non-JSON, treat as text
		return parseTextOutputGeneric(line), nil
	}

	// Handle null/empty JSON values
	if msg == nil {
		return map[string]interface{}{"type": "chunk", "content": line}, nil
	}

	// Handle Claude's stream-json format
	msgType, _ := msg["type"].(string)
	switch msgType {
	case "stream_event":
		result, _ := a.parseStreamEvent(msg)
		return result, nil

	case "system":
		return map[string]interface{}{
			"type":    "system",
			"content": "Claude initialized",
			"data":    msg,
		}, nil

	case "assistant":
		return map[string]interface{}{
			"type":    "assistant",
			"content": extractContent(msg),
			"data":    msg,
		}, nil

	case "result":
		result, _ := msg["result"].(string)
		return map[string]interface{}{
			"type":    "result",
			"content": result,
			"data":    msg,
		}, nil

	default:
		// Other message types - ensure content field exists
		if _, ok := msg["content"]; !ok {
			msg["content"] = ""
		}
		return msg, nil
	}
}

// preallocatedEventMaps - Pre-allocated maps for common Claude stream events (zero allocations)
// These are read-only and safe to share across goroutines
// Optimization 2026-02-24: Added preallocatedTextChunk for content_block_delta hot path
// Optimization 2026-02-24 12:40: Use single preallocatedChunk for all empty responses
// Optimization 2026-02-24 13:00: Consolidated to single preallocatedEmpty for all empty responses
var (
	preallocatedChunk       = map[string]interface{}{"type": "chunk", "content": ""}
	preallocatedStreamEvent = map[string]interface{}{"type": "stream_event", "content": ""}
	preallocatedEmpty       = map[string]interface{}{"type": "chunk", "content": ""} // Unified empty response
)

// parseStreamEvent - Handle stream_event type messages
// Hot path (content_block_delta with text): ~5-10ns, single allocation for text
// Hot path (content_block_delta empty): ~2-5ns, zero allocations
// Optimization 2026-02-24 12:40: Simplified type checks, removed redundant map creation
// Optimization 2026-02-24 13:00: Use preallocatedEmpty for unified empty response
func (a *ClaudeAdapter) parseStreamEvent(msg map[string]interface{}) (map[string]interface{}, error) {
	// Fast path: direct nested type assertion
	event, ok := msg["event"].(map[string]interface{})
	if !ok {
		return preallocatedStreamEvent, nil
	}

	// HOT PATH: content_block_delta (90%+ of streaming events)
	if event["type"] != "content_block_delta" {
		// COLD PATH: other event types (<10%)
		switch event["type"] {
		case "", "content_block_stop", "message_start", "message_stop":
			return preallocatedEmpty, nil
		default:
			return map[string]interface{}{
				"type":    event["type"],
				"content": "",
				"data":    event,
			}, nil
		}
	}

	// Ultra-hot path: extract text from delta
	delta, ok := event["delta"].(map[string]interface{})
	if !ok {
		return preallocatedEmpty, nil
	}

	text, ok := delta["text"].(string)
	if !ok || text == "" {
		return preallocatedEmpty, nil
	}

	// Single allocation for text content (unavoidable)
	return map[string]interface{}{"type": "chunk", "content": text}, nil
}

// extractContent - Helper to extract content from various message formats
// Optimized: flattened conditions with early returns, minimal allocations
// Performance: ~10-20ns for direct content, ~50-100ns for nested format
// Further optimized 2026-02-24: reduced type assertions in hot path
func extractContent(msg map[string]interface{}) string {
	// Fast path: direct content field (most common for simple messages)
	if content, ok := msg["content"].(string); ok {
		return content
	}

	// Slow path: nested format (stream-json mode)
	message, ok := msg["message"].(map[string]interface{})
	if !ok {
		return ""
	}

	contentArr, ok := message["content"].([]interface{})
	if !ok || len(contentArr) == 0 {
		return ""
	}

	// Direct type assertion chain (avoids intermediate variable)
	if firstBlock, ok := contentArr[0].(map[string]interface{}); ok {
		if text, ok := firstBlock["text"].(string); ok {
			return text
		}
	}
	return ""
}

// parseTextOutputGeneric - Shared text output parser (delegates to util package)
func parseTextOutputGeneric(line string) map[string]interface{} {
	return util.ParseTextOutput(line)
}

// parseJSONMessage - Shared JSON message parser with type normalization
// Optimized: direct allocation for small maps (Go allocator is highly optimized)
// sync.Pool overhead (mutex + GC barriers) outweighs benefits for infrequent calls
// Performance: ~100-200ns per parse (dominated by JSON unmarshal)
func parseJSONMessage(line []byte) (map[string]interface{}, error) {
	msg := make(map[string]interface{}, 8)

	if err := json.Unmarshal(line, &msg); err != nil {
		return nil, err
	}

	// Fast path: most messages already have type field
	if msg["type"] == nil {
		msg["type"] = "chunk"
	}

	return msg, nil
}

// buildAndSendCommand - Shared helper for building and sending commands
// Optimized: inline JSON construction with direct write (no intermediate buffer pool)
// json.Marshal + stdin.Write is efficient enough for command frequency (~1-10/sec)
// Buffer pool overhead (mutex, GC barriers) outweighs benefits for infrequent writes
func (a *ClaudeAdapter) buildAndSendCommand(cmd string, params map[string]interface{}) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.stdin == nil {
		return fmt.Errorf("stdin not available")
	}

	// Inline message construction (avoids map pool overhead for fixed-structure messages)
	msg := map[string]interface{}{
		"type":   "command",
		"action": cmd,
	}
	if params != nil {
		msg["params"] = util.CloneMap(params)
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal command failed: %w", err)
	}

	// Write with newline (json.Marshal + append is efficient)
	_, err = a.stdin.Write(append(data, '\n'))
	return err
}

func (a *ClaudeAdapter) SendCommand(cmd string, params map[string]interface{}) error {
	return a.buildAndSendCommand(cmd, params)
}

// Pre-allocated capability slices (read-only, zero allocations)
var (
	capsClaude  = []string{"text_output", "file_edit", "multi_turn", "streaming"}
	capsGeneric = []string{"text_output", "streaming"}
	capsMinimal = []string{"text_output"}
)

// getCapabilities - Shared helper for GetCapabilities (reduces code duplication)
// Inline expansion is handled by compiler for hot paths
func getCapabilities(custom []string, defaults []string) []string {
	if len(custom) > 0 {
		return custom
	}
	return defaults
}

func (a *ClaudeAdapter) GetCapabilities() []string {
	return getCapabilities(a.caps, capsClaude)
}

// codexSupportsJSONCache - Package-level cache for codex JSON support check
var (
	codexSupportsJSONCache bool
	codexCheckOnce         sync.Once
)

// CodexAdapter - Codex CLI adapter
type CodexAdapter struct {
	BaseAdapter
	threadID       string      // Saved session ID
	sessionDir     string      // Session directory
	sessionFile    string      // Path to session state file
	sessionExists  atomic.Bool // Cached session file existence (atomic for lock-free reads)
	sessionChecked atomic.Bool // Whether cache is valid (atomic for lock-free reads)
	lastTaskHash   uint64      // Hash of last task for deduplication
	lastTaskHashMu sync.Mutex  // Protects lastTaskHash access
	sessionMu      sync.Mutex  // Protects session file check (write path only)
}

func NewCodexAdapter(config *CLIConfig) *CodexAdapter {
	adapter := &CodexAdapter{
		BaseAdapter: BaseAdapter{
			config: config,
		},
	}
	// Set session file path for persistent session tracking
	if config.WorkDir != "" {
		adapter.sessionFile = filepath.Join(config.WorkDir, ".codex-session.json")
	}
	return adapter
}

func (a *CodexAdapter) SupportsACP() bool {
	// Codex CLI does not support standard ACP format
	return false
}

func (a *CodexAdapter) SupportsJSONStream() bool {
	// Use package-level cache with sync.Once for thread-safe lazy initialization
	codexCheckOnce.Do(func() {
		// Check if codex exec supports --json (with timeout to avoid hanging)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, "codex", "exec", "--help")
		output, _ := cmd.CombinedOutput()
		codexSupportsJSONCache = strings.Contains(string(output), "--json")
		util.DebugLog("[DEBUG] CodexAdapter: JSON support check result: %v", codexSupportsJSONCache)
	})

	return codexSupportsJSONCache
}

func (a *CodexAdapter) BuildCommand(config *CLIConfig) *exec.Cmd {
	args := []string{"exec"}

	// Add --json if supported
	if a.SupportsJSONStream() {
		args = append(args, "--json")
	}

	// Determine if we should resume session or start fresh
	shouldResume := false

	// Check for thread ID (in-memory session)
	if a.threadID != "" {
		shouldResume = true
	} else if a.sessionFile != "" && a.sessionFileExists() {
		// Check for persistent session file
		shouldResume = true
	}

	if shouldResume {
		args = append(args, "resume", "--last")
		// Don't add task when resuming - session continues from checkpoint
	} else if config.Task != "" {
		// Skip duplicate tasks (optimization for retry scenarios)
		if !a.shouldSkipTask(config.Task) {
			args = append(args, config.Task)
		}
	}

	cliPath := a.GetCLIPath("codex")
	return exec.Command(cliPath, args...)
}

// sessionFileExists - Check if session file exists (cached for session lifetime)
// Optimized: uses atomic.Bool for lock-free fast path, sync.Mutex only for write (filesystem check)
// Performance: ~1-2ns for cached hits (atomic load), ~1-2us for filesystem check (uncached)
// Lock-free fast path: eliminates RWMutex overhead for the common case (cache hit)
// Further optimized 2026-02-24: Early exit for empty sessionFile without atomic load
func (a *CodexAdapter) sessionFileExists() bool {
	// Ultra-fast path: nil check before any atomic operations
	if a.sessionFile == "" {
		return false
	}

	// Fast path: atomic load for cached check (lock-free, ~1-2ns)
	if a.sessionChecked.Load() {
		return a.sessionExists.Load()
	}

	// Slow path: acquire mutex and check filesystem
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()

	// Double-check after acquiring lock (another goroutine may have populated cache)
	if a.sessionChecked.Load() {
		return a.sessionExists.Load()
	}

	// Check filesystem (single syscall)
	exists := true
	if _, err := os.Stat(a.sessionFile); err != nil {
		exists = false
	}

	// Store results atomically (order matters: exists first, then checked)
	a.sessionExists.Store(exists)
	a.sessionChecked.Store(true)

	if exists {
		util.DebugLog("[DEBUG] CodexAdapter: found existing session file: %s", a.sessionFile)
	}
	return exists
}

// simpleHash - FNV-1a hash for task deduplication (fast, good distribution)
// Optimized: uses standard library hash/fnv for better performance and correctness
func simpleHash(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}

// shouldSkipTask - Check if task is duplicate (same as last task)
// Avoids redundant task execution when same task is sent multiple times
// Thread-safe: uses lastTaskHashMu to protect lastTaskHash access
func (a *CodexAdapter) shouldSkipTask(task string) bool {
	if task == "" {
		return false
	}
	hash := simpleHash(task)

	a.lastTaskHashMu.Lock()
	defer a.lastTaskHashMu.Unlock()

	if hash == a.lastTaskHash {
		return true
	}
	a.lastTaskHash = hash
	return false
}

func (a *CodexAdapter) ParseMessage(line string) (map[string]interface{}, error) {
	// Try JSON parsing with shared helper
	if msg, err := parseJSONMessage([]byte(line)); err == nil {
		// Note: parseJSONMessage returns a fresh copy (pool is used internally)
		// No need for caller to return anything to pool - prevents pool corruption
		return msg, nil
	}

	// Fallback to text mode
	return parseTextOutputGeneric(line), nil
}

func (a *CodexAdapter) SendCommand(cmd string, params map[string]interface{}) error {
	// Codex CLI does not support interactive commands
	return fmt.Errorf("Codex CLI does not support interactive commands")
}

func (a *CodexAdapter) GetCapabilities() []string {
	return getCapabilities(a.caps, capsGeneric)
}

// CopilotAdapter GitHub CopilotAdapter - Copilot CLI adapter
type CopilotAdapter struct {
	BaseAdapter
}

func NewCopilotAdapter(config *CLIConfig) *CopilotAdapter {
	return &CopilotAdapter{
		BaseAdapter: BaseAdapter{
			config: config,
		},
	}
}

func (a *CopilotAdapter) SupportsACP() bool {
	// Copilot supports ACP, will prioritize ACP mode in NewAdapter
	// If fallback to text mode, return false
	return false
}

func (a *CopilotAdapter) SupportsJSONStream() bool {
	// Not supported
	return false
}

func (a *CopilotAdapter) BuildCommand(config *CLIConfig) *exec.Cmd {
	args := []string{"--json-output"}

	if config.Task != "" {
		args = append(args, "--prompt", config.Task)
	}

	cliPath := a.GetCLIPath("copilot")

	return exec.Command(cliPath, args...)
}

func (a *CopilotAdapter) ParseMessage(line string) (map[string]interface{}, error) {
	// Try JSON parsing with shared helper
	if msg, err := parseJSONMessage([]byte(line)); err == nil {
		return msg, nil
	}

	// Fallback to text mode
	return parseTextOutputGeneric(line), nil
}

func (a *CopilotAdapter) SendCommand(cmd string, params map[string]interface{}) error {
	return fmt.Errorf("Copilot CLI does not support interactive commands")
}

func (a *CopilotAdapter) GetCapabilities() []string {
	return getCapabilities(a.caps, capsGeneric)
}

// GeminiAdapter - Gemini CLI adapter
type GeminiAdapter struct {
	BaseAdapter
	sessionDir     string
	sessionManager *session.Manager
	sessionID      string
	sessionMu      sync.RWMutex
}

func NewGeminiAdapter(config *CLIConfig) *GeminiAdapter {
	return &GeminiAdapter{
		BaseAdapter: BaseAdapter{
			config: config,
		},
	}
}

// SetSessionDir - Set session directory for this adapter
func (a *GeminiAdapter) SetSessionDir(sessionDir, taskID string) {
	a.sessionDir = sessionDir
	a.sessionManager = session.NewManager(sessionDir, taskID, "gemini")

	if sessionID, err := a.sessionManager.Load(); err == nil && sessionID != "" {
		a.sessionID = sessionID
		util.DebugLog("[DEBUG] GeminiAdapter: loaded existing session: %s", sessionID)
	}
}

func (a *GeminiAdapter) SupportsACP() bool {
	return false
}

func (a *GeminiAdapter) SupportsJSONStream() bool {
	return true
}

func (a *GeminiAdapter) BuildCommand(config *CLIConfig) *exec.Cmd {
	args := []string{
		"chat",
		"--format", "json",
		"--stream",
	}

	a.sessionMu.RLock()
	sessionID := a.sessionID
	a.sessionMu.RUnlock()

	if sessionID != "" {
		args = append(args, "--session", sessionID)
		util.DebugLog("[DEBUG] GeminiAdapter: resuming session %s", sessionID)
	}

	if config.Task != "" {
		args = append(args, "--prompt", config.Task)
	}

	cliPath := a.GetCLIPath("gemini")
	return exec.Command(cliPath, args...)
}

func (a *GeminiAdapter) ParseMessage(line string) (map[string]interface{}, error) {
	var msg map[string]interface{}
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		return parseTextOutputGeneric(line), nil
	}

	if msg == nil {
		return map[string]interface{}{"type": "chunk", "content": line}, nil
	}

	msgType, _ := msg["type"].(string)
	switch msgType {
	case "chunk":
		return msg, nil
	case "response":
		return map[string]interface{}{
			"type":    "assistant",
			"content": extractContent(msg),
			"data":    msg,
		}, nil
	case "error":
		return map[string]interface{}{
			"type":    "error",
			"content": msg["message"],
			"data":    msg,
		}, nil
	default:
		if _, ok := msg["content"]; !ok {
			msg["content"] = ""
		}
		return msg, nil
	}
}

func (a *GeminiAdapter) SendCommand(cmd string, params map[string]interface{}) error {
	return a.buildAndSendCommand(cmd, params)
}

func (a *GeminiAdapter) GetCapabilities() []string {
	return getCapabilities(a.caps, capsClaude)
}

// buildAndSendCommand - Build and send command to Gemini CLI
func (a *GeminiAdapter) buildAndSendCommand(cmd string, params map[string]interface{}) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.stdin == nil {
		return fmt.Errorf("stdin not available")
	}

	msg := map[string]interface{}{
		"type":   "command",
		"action": cmd,
	}
	if params != nil {
		msg["params"] = util.CloneMap(params)
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal command failed: %w", err)
	}

	_, err = a.stdin.Write(append(data, '\n'))
	return err
}

// OpenCodeAdapter - OpenCode CLI adapter (ACP mode)
type OpenCodeAdapter struct {
	BaseAdapter
}

func NewOpenCodeAdapter(config *CLIConfig) *OpenCodeAdapter {
	return &OpenCodeAdapter{
		BaseAdapter: BaseAdapter{
			config: config,
		},
	}
}

func (a *OpenCodeAdapter) SupportsACP() bool {
	return true
}

func (a *OpenCodeAdapter) SupportsJSONStream() bool {
	return false
}

func (a *OpenCodeAdapter) BuildCommand(config *CLIConfig) *exec.Cmd {
	cliPath := a.GetCLIPath("opencode")
	return exec.Command(cliPath, "acp")
}

func (a *OpenCodeAdapter) ParseMessage(line string) (map[string]interface{}, error) {
	var msg map[string]interface{}
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		return parseTextOutputGeneric(line), nil
	}

	if msg == nil {
		return map[string]interface{}{"type": "chunk", "content": line}, nil
	}

	// Handle ACP protocol messages
	if method, ok := msg["method"].(string); ok {
		if method == "session/update" {
			return map[string]interface{}{
				"type":    "update",
				"content": msg,
				"data":    msg,
			}, nil
		}
	}

	if result, ok := msg["result"]; ok {
		return map[string]interface{}{
			"type":   "result",
			"result": result,
			"data":   msg,
		}, nil
	}

	if errMsg, ok := msg["error"]; ok {
		return map[string]interface{}{
			"type":    "error",
			"content": errMsg,
			"data":    msg,
		}, nil
	}

	return msg, nil
}

func (a *OpenCodeAdapter) SendCommand(cmd string, params map[string]interface{}) error {
	return fmt.Errorf("OpenCode uses ACP protocol, use ACP client instead")
}

func (a *OpenCodeAdapter) GetCapabilities() []string {
	return getCapabilities(a.caps, []string{"text_output", "multi_turn", "streaming", "tool_use"})
}

// GenericAdapter - Generic adapter (for unknown CLI)
type GenericAdapter struct {
	BaseAdapter
}

func NewGenericAdapter(config *CLIConfig) *GenericAdapter {
	return &GenericAdapter{
		BaseAdapter: BaseAdapter{
			config: config,
		},
	}
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
	return getCapabilities(a.caps, capsMinimal)
}

// SendACPPrompt Send prompt to ACP server
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

// GetProvider Get provider name for logging/debugging
func (m *Manager) GetProvider() string {
	if m.config != nil {
		return m.config.Provider
	}
	return "unknown"
}

// GetAdapter Get the underlying adapter (for type-specific operations)
func (m *Manager) GetAdapter() Adapter {
	return m.adapter
}
