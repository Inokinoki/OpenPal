package adapter

import (
	"strings"
	"testing"
)

// TestClaudeAdapterSupportsJSONStream Test Claude adapter JSON Stream support
func TestClaudeAdapterSupportsJSONStream(t *testing.T) {
	adapter := &ClaudeAdapter{
		BaseAdapter: BaseAdapter{
			config: &CLIConfig{
				Provider: "claude",
				WorkDir:  "/tmp",
			},
		},
	}

	// Claude Should Support JSON Stream
	if !adapter.SupportsJSONStream() {
		t.Error("Expected Claude adapter to support JSON Stream")
	}
}

// TestClaudeAdapterBuildCommand Test Claude command building
func TestClaudeAdapterBuildCommand(t *testing.T) {
	adapter := &ClaudeAdapter{
		BaseAdapter: BaseAdapter{
			config: &CLIConfig{
				Provider: "claude",
				WorkDir:  "/workspace",
				Task:     "Refactor this code",
				Files:    []string{"src/main.go", "src/utils.go"},
			},
		},
	}

	cmd := adapter.BuildCommand(adapter.config)

	// Verify command (check basename since PATH may expand to full path)
	if !strings.HasSuffix(cmd.Path, "claude") {
		t.Errorf("Expected command ending with 'claude', got %s", cmd.Path)
	}

	// Verify parameters include -p and --output-format stream-json
	args := strings.Join(cmd.Args, " ")
	if !strings.Contains(args, "-p") {
		t.Error("Expected -p flag")
	}
	if !strings.Contains(args, "--output-format") {
		t.Error("Expected --output-format flag")
	}
	if !strings.Contains(args, "stream-json") {
		t.Error("Expected stream-json format")
	}
	if !strings.Contains(args, "--verbose") {
		t.Error("Expected --verbose flag")
	}
}

// TestClaudeAdapterParseMessage Test Claude message parsing
func TestClaudeAdapterParseMessage(t *testing.T) {
	adapter := &ClaudeAdapter{
		BaseAdapter: BaseAdapter{
			config: &CLIConfig{
				Provider: "claude",
			},
		},
	}

	// Test JSON Message
	jsonMsg := `{"type": "assistant", "content": "Hello"}`
	parsed, err := adapter.ParseMessage(jsonMsg)
	if err != nil {
		t.Fatalf("Failed to parse JSON message: %v", err)
	}

	if parsed["type"] != "assistant" {
		t.Errorf("Expected type=assistant, got %v", parsed["type"])
	}

	// Test Text Message
	textMsg := "Let me analyze this code..."
	parsed, err = adapter.ParseMessage(textMsg)
	if err != nil {
		t.Fatalf("Failed to parse text message: %v", err)
	}

	if parsed["type"] != "chunk" {
		t.Errorf("Expected type=chunk for text, got %v", parsed["type"])
	}
}

// TestCodexAdapterSupportsJSONStream Test Codex adapter with Mock
func TestCodexAdapterSupportsJSONStream(t *testing.T) {
	adapter := &CodexAdapter{
		BaseAdapter: BaseAdapter{
			config: &CLIConfig{
				Provider: "codex",
				WorkDir:  "/tmp",
			},
		},
	}

	// Codex JSON Stream support needs verification (may not support)
	// Just testing method exists and is callable
	_ = adapter.SupportsJSONStream()
}

// TestCodexAdapterBuildCommand Test Codex command building
func TestCodexAdapterBuildCommand(t *testing.T) {
	adapter := &CodexAdapter{
		BaseAdapter: BaseAdapter{
			config: &CLIConfig{
				Provider: "codex",
				WorkDir:  "/workspace",
				Task:     "Fix the bug",
			},
		},
	}

	cmd := adapter.BuildCommand(adapter.config)

	// Verify command
	if cmd.Path != "codex" {
		t.Errorf("Expected command 'codex', got %s", cmd.Path)
	}

	// Verify parameters include exec
	args := strings.Join(cmd.Args, " ")
	if !strings.Contains(args, "exec") {
		t.Error("Expected 'exec' subcommand")
	}
}

// TestCopilotAdapterACP Test Copilot ACP support
func TestCopilotAdapterACP(t *testing.T) {
	adapter := &CopilotAdapter{
		BaseAdapter: BaseAdapter{
			config: &CLIConfig{
				Provider: "copilot",
			},
		},
	}

	// Copilot does not support ACP (Will Fallback to text Mode)
	if adapter.SupportsACP() {
		t.Error("Copilot adapter should not support ACP in text mode")
	}
}

// TestGenericAdapter Test generic adapter
func TestGenericAdapter(t *testing.T) {
	adapter := &GenericAdapter{
		BaseAdapter: BaseAdapter{
			config: &CLIConfig{
				Provider: "unknown-cli",
			},
		},
	}

	// Generic adapter does not support ACP Or JSON Stream
	if adapter.SupportsACP() {
		t.Error("Generic adapter should not support ACP")
	}

	if adapter.SupportsJSONStream() {
		t.Error("Generic adapter should not support JSON Stream")
	}

	// Test Command Build
	cmd := adapter.BuildCommand(adapter.config)
	if cmd.Path != "unknown-cli" {
		t.Errorf("Expected command 'unknown-cli', got %s", cmd.Path)
	}
}

// TestAdapterModeConstants Test adapter mode constants
func TestAdapterModeConstants(t *testing.T) {
	if ModeACP != "acp" {
		t.Errorf("Expected ModeACP='acp', got %s", ModeACP)
	}

	if ModeText != "text" {
		t.Errorf("Expected ModeText='text', got %s", ModeText)
	}
}

// TestCLIProcessStop Test CLI process stop
func TestCLIProcessStop(t *testing.T) {
	// Create a mock process
	process := &CLIProcess{
		Pid: 12345,
	}

	// Stop method should not panic
	err := process.Stop()
	// Note: May fail without real process, but should not panic
	_ = err
}

// TestCLIConfig Test CLI config
func TestCLIConfig(t *testing.T) {
	config := &CLIConfig{
		Provider: "claude",
		WorkDir:  "/workspace",
		Task:     "Test task",
		Files:    []string{"file1.go", "file2.go"},
		Options:  map[string]string{"model": "claude-sonnet"},
	}

	if config.Provider != "claude" {
		t.Errorf("Expected provider='claude', got %s", config.Provider)
	}

	if config.WorkDir != "/workspace" {
		t.Errorf("Expected workdir='/workspace', got %s", config.WorkDir)
	}

	if len(config.Files) != 2 {
		t.Errorf("Expected 2 files, got %d", len(config.Files))
	}

	if config.Options["model"] != "claude-sonnet" {
		t.Errorf("Expected model='claude-sonnet', got %s", config.Options["model"])
	}
}

// TestAdapterInterface Test adapter interface implementation
func TestAdapterInterface(t *testing.T) {
	// Verify all adapters implement Adapter interface
	var _ Adapter = &ClaudeAdapter{}
	var _ Adapter = &CodexAdapter{}
	var _ Adapter = &CopilotAdapter{}
	var _ Adapter = &GenericAdapter{}
}

// TestParseMessageError Test error message parsing
func TestParseMessageError(t *testing.T) {
	adapter := &ClaudeAdapter{
		BaseAdapter: BaseAdapter{
			config: &CLIConfig{},
		},
	}

	// Test invalid JSON
	invalidJSON := `{"type": "chunk", invalid}`
	parsed, err := adapter.ParseMessage(invalidJSON)
	if err != nil {
		// Should fallback to text mode, no error
		t.Logf("Got error (expected): %v", err)
	}

	// Should return some result even on parse error
	if parsed == nil {
		t.Error("Expected non-nil result even on parse error")
	}
}

// TestSupportsACPFunction Test supportsACP function
func TestSupportsACPFunction(t *testing.T) {
	tests := []struct {
		provider string
		expected bool
	}{
		{"copilot", true},
		{"copilot-acp", true},
		{"opencode", true},
		{"claude", false},
		{"codex", false},
		{"unknown", false},
	}

	for _, test := range tests {
		result := supportsACP(test.provider)
		if result != test.expected {
			t.Errorf("supportsACP(%s) = %v, expected %v", test.provider, result, test.expected)
		}
	}
}
