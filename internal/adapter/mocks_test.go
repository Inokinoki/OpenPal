package adapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mockCLI Create a mock CLI script
func mockCLI(t *testing.T, name, script string) string {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, name)

	// Write script
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("Failed to write mock script: %v", err)
	}

	return scriptPath
}

// TestClaudeAdapterWithMock Test Claude adapter with Mock
func TestClaudeAdapterWithMock(t *testing.T) {
	// Create mock claude command
	mockScript := `#!/bin/bash
echo '{"type":"assistant","content":"Hello from mock Claude!"}'
echo '{"type":"chunk","content":"This is a test response"}'
`
	mockPath := mockCLI(t, "claude", mockScript)

	// Temporarily add mock to PATH
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", filepath.Dir(mockPath)+":"+oldPath)
	defer os.Setenv("PATH", oldPath)

	// Create adapter
	adapter := &ClaudeAdapter{
		BaseAdapter: BaseAdapter{
			config: &CLIConfig{
				Provider: "claude",
				WorkDir:  "/tmp",
				Task:     "Test task",
			},
		},
	}

	// TestCommandBuild
	cmd := adapter.BuildCommand(adapter.config)
	if cmd.Path != mockPath {
		t.Logf("Using mock claude at: %s", cmd.Path)
	}

	// TestMessageParse
	msg := `{"type":"assistant","content":"Hello"}`
	parsed, err := adapter.ParseMessage(msg)
	if err != nil {
		t.Fatalf("Failed to parse message: %v", err)
	}

	if parsed["type"] != "assistant" {
		t.Errorf("Expected type=assistant, got %v", parsed["type"])
	}

	if parsed["content"] != "Hello" {
		t.Errorf("Expected content='Hello', got %v", parsed["content"])
	}
}

// TestClaudeAdapterMockOutput Test Claude output parsing
func TestClaudeAdapterMockOutput(t *testing.T) {
	adapter := &ClaudeAdapter{
		BaseAdapter: BaseAdapter{
			config: &CLIConfig{
				Provider: "claude",
			},
		},
	}

	tests := []struct {
		name     string
		input    string
		wantType string
		wantErr  bool
	}{
		{
			name:     "assistant_message",
			input:    `{"type":"assistant","content":"Hello"}`,
			wantType: "assistant",
			wantErr:  false,
		},
		{
			name:     "chunk_message",
			input:    `{"type":"chunk","content":"Processing..."}`,
			wantType: "chunk",
			wantErr:  false,
		},
		{
			name:     "error_message",
			input:    `{"type":"error","error":"Something went wrong"}`,
			wantType: "error",
			wantErr:  false,
		},
		{
			name:     "plain_text",
			input:    "Let me think about this...",
			wantType: "chunk",
			wantErr:  false,
		},
		{
			name:     "invalid_json",
			input:    `{"type": "invalid`,
			wantType: "chunk",
			wantErr:  false, // ShouldFallbacktoTextMode
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := adapter.ParseMessage(tt.input)
			if err != nil && !tt.wantErr {
				t.Errorf("Unexpected error: %v", err)
			}

			if parsed == nil {
				t.Fatal("Expected non-nil parsed result")
			}

			if parsed["type"] != tt.wantType {
				t.Errorf("Expected type=%s, got %v", tt.wantType, parsed["type"])
			}
		})
	}
}

// TestCodexAdapterMock Use Mock Test Codex adapter with Mock
func TestCodexAdapterMock(t *testing.T) {
	// Create mock codex command
	mockScript := `#!/bin/bash
echo '{"message":{"role":"assistant","content":"Codex response"}}'
`
	mockPath := mockCLI(t, "codex", mockScript)

	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", filepath.Dir(mockPath)+":"+oldPath)
	defer os.Setenv("PATH", oldPath)

	adapter := &CodexAdapter{
		BaseAdapter: BaseAdapter{
			config: &CLIConfig{
				Provider: "codex",
				WorkDir:  "/tmp",
				Task:     "Fix bug",
			},
		},
	}

	// TestCommandBuild
	cmd := adapter.BuildCommand(adapter.config)
	if !strings.Contains(cmd.Path, "codex") {
		t.Errorf("Expected codex command, got %s", cmd.Path)
	}

	// Test Codex specificformatParse
	msg := `{"message":{"role":"assistant","content":"Here's the fix"}}`
	parsed, err := adapter.ParseMessage(msg)
	if err != nil {
		t.Fatalf("Failed to parse Codex message: %v", err)
	}

	// Codex should extract message.content
	if parsed["type"] != "chunk" {
		t.Errorf("Expected type=chunk, got %v", parsed["type"])
	}
}

// TestCopilotAdapterMock Test Copilot adapter with Mock
func TestCopilotAdapterMock(t *testing.T) {
	adapter := &CopilotAdapter{
		BaseAdapter: BaseAdapter{
			config: &CLIConfig{
				Provider: "copilot",
			},
		},
	}

	// Copilot does not support ACP
	if adapter.SupportsACP() {
		t.Error("Copilot should not support ACP")
	}

	// TestCommandBuild
	cmd := adapter.BuildCommand(adapter.config)
	if !strings.Contains(cmd.Path, "copilot") {
		t.Errorf("Expected copilot command, got %s", cmd.Path)
	}

	// TestMessageParse
	tests := []struct {
		name  string
		input string
	}{
		{"json", `{"type":"response","data":"test"}`},
		{"text", "Copilot is thinking..."},
		{"code_block", "```go\nfmt.Println(\"Hello\")\n```"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := adapter.ParseMessage(tt.input)
			if err != nil {
				t.Logf("Parse error (may be expected): %v", err)
			}
			if parsed == nil {
				t.Error("Expected non-nil result")
			}
		})
	}
}

// TestGenericAdapterMock Test generic adapter
func TestGenericAdapterMock(t *testing.T) {
	adapter := &GenericAdapter{
		BaseAdapter: BaseAdapter{
			config: &CLIConfig{
				Provider: "unknown-cli",
			},
		},
	}

	// GenericAdapterNotSupportspecial features
	if adapter.SupportsACP() {
		t.Error("Generic adapter should not support ACP")
	}
	if adapter.SupportsJSONStream() {
		t.Error("Generic adapter should not support JSON Stream")
	}

	// TestCommandBuild
	cmd := adapter.BuildCommand(adapter.config)
	if cmd.Path != "unknown-cli" {
		t.Errorf("Expected unknown-cli, got %s", cmd.Path)
	}
}

// TestAdapterWithRealisticOutput Test realistic output
func TestAdapterWithRealisticOutput(t *testing.T) {
	adapter := &ClaudeAdapter{
		BaseAdapter: BaseAdapter{
			config: &CLIConfig{
				Provider: "claude",
			},
		},
	}

	// Simulate realistic Claude output stream
	realisticOutputs := []string{
		`{"type":"system","message":"Starting task..."}`,
		`{"type":"assistant","content":"I'll help you with that."}`,
		`{"type":"chunk","content":"Let me analyze the code..."}`,
		`{"type":"chunk","content":"I found the issue."}`,
		`{"type":"tool_use","name":"edit","input":{"file":"main.go"}}`,
		`{"type":"chunk","content":"The fix has been applied."}`,
		`{"type":"assistant","content":"Task completed!"}`,
	}

	for i, output := range realisticOutputs {
		parsed, err := adapter.ParseMessage(output)
		if err != nil {
			t.Errorf("Output %d failed to parse: %v", i, err)
			continue
		}

		if parsed == nil {
			t.Errorf("Output %d returned nil", i)
		}

		t.Logf("Output %d: type=%v", i, parsed["type"])
	}
}

// TestAdapterErrorHandling Test error handler
func TestAdapterErrorHandling(t *testing.T) {
	adapter := &ClaudeAdapter{
		BaseAdapter: BaseAdapter{
			config: &CLIConfig{
				Provider: "claude",
			},
		},
	}

	// TestVariousErrorScenario
	errorCases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"null", "null"},
		{"array", `[]`},
		{"number", `123`},
		{"malformed", `{type: invalid}`},
		{"unclosed", `{"type": "chunk"`},
	}

	for _, tc := range errorCases {
		t.Run(tc.name, func(t *testing.T) {
			// Should not panic
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("ParseMessage panicked: %v", r)
				}
			}()

			parsed, _ := adapter.ParseMessage(tc.input)
			// Should return result even on error (fallback to text)
			if parsed == nil {
				t.Error("Expected non-nil result even on error")
			}
		})
	}
}

// TestAdapterCommandBuilder Test command buildinger
func TestAdapterCommandBuilder(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		config   *CLIConfig
		wantArgs []string
	}{
		{
			name:     "claude_basic",
			provider: "claude",
			config: &CLIConfig{
				Provider: "claude",
				WorkDir:  "/workspace",
				Task:     "Test",
			},
			wantArgs: []string{"-p", "--output-format", "stream-json"},
		},
		{
			name:     "codex_basic",
			provider: "codex",
			config: &CLIConfig{
				Provider: "codex",
				WorkDir:  "/workspace",
				Task:     "Test",
			},
			wantArgs: []string{"exec"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var adapter Adapter
			switch tt.provider {
			case "claude":
				adapter = &ClaudeAdapter{BaseAdapter: BaseAdapter{config: tt.config}}
			case "codex":
				adapter = &CodexAdapter{BaseAdapter: BaseAdapter{config: tt.config}}
			default:
				adapter = &GenericAdapter{BaseAdapter: BaseAdapter{config: tt.config}}
			}

			cmd := adapter.BuildCommand(tt.config)
			argsStr := strings.Join(cmd.Args, " ")

			for _, wantArg := range tt.wantArgs {
				if !strings.Contains(argsStr, wantArg) {
					t.Errorf("Expected arg '%s' in command: %s", wantArg, argsStr)
				}
			}
		})
	}
}

// TestAdapterCapabilities Test adapter capabilities
func TestAdapterCapabilities(t *testing.T) {
	tests := []struct {
		name             string
		adapter          Adapter
		wantACP          bool
		wantJSONStream   bool
		wantCapabilities []string
	}{
		{
			name:           "claude",
			adapter:        &ClaudeAdapter{BaseAdapter: BaseAdapter{config: &CLIConfig{}}},
			wantACP:        false,
			wantJSONStream: true,
		},
		{
			name:           "codex",
			adapter:        &CodexAdapter{BaseAdapter: BaseAdapter{config: &CLIConfig{}}},
			wantACP:        false,
			wantJSONStream: false,
		},
		{
			name:           "copilot",
			adapter:        &CopilotAdapter{BaseAdapter: BaseAdapter{config: &CLIConfig{}}},
			wantACP:        false,
			wantJSONStream: false,
		},
		{
			name:           "generic",
			adapter:        &GenericAdapter{BaseAdapter: BaseAdapter{config: &CLIConfig{}}},
			wantACP:        false,
			wantJSONStream: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.adapter.SupportsACP(); got != tt.wantACP {
				t.Errorf("SupportsACP() = %v, want %v", got, tt.wantACP)
			}

			if got := tt.adapter.SupportsJSONStream(); got != tt.wantJSONStream {
				t.Errorf("SupportsJSONStream() = %v, want %v", got, tt.wantJSONStream)
			}

			caps := tt.adapter.GetCapabilities()
			if caps == nil {
				t.Error("GetCapabilities() should not return nil")
			}
		})
	}
}

// TestAdapterModeDetection Test adapter mode detection
func TestAdapterModeDetection(t *testing.T) {
	// Test supportsACP function
	tests := []struct {
		provider string
		wantACP  bool
	}{
		{"copilot", true},
		{"copilot-acp", true},
		{"opencode", true},
		{"claude", false},
		{"codex", false},
		{"unknown", false},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			if got := supportsACP(tt.provider); got != tt.wantACP {
				t.Errorf("supportsACP(%s) = %v, want %v", tt.provider, got, tt.wantACP)
			}
		})
	}
}

// TestAdapterJSONParsing Test JSON parsing
func TestAdapterJSONParsing(t *testing.T) {
	adapter := &ClaudeAdapter{
		BaseAdapter: BaseAdapter{
			config: &CLIConfig{},
		},
	}

	// TestComplex JSON Structure
	complexJSON := `{
		"type": "tool_use",
		"name": "edit",
		"input": {
			"file": "main.go",
			"changes": [
				{"line": 10, "action": "replace", "content": "fmt.Println(\"new\")"}
			]
		}
	}`

	parsed, err := adapter.ParseMessage(complexJSON)
	if err != nil {
		t.Fatalf("Failed to parse complex JSON: %v", err)
	}

	if parsed["type"] != "tool_use" {
		t.Errorf("Expected type=tool_use, got %v", parsed["type"])
	}

	if parsed["name"] != "edit" {
		t.Errorf("Expected name=edit, got %v", parsed["name"])
	}

	// Verify can serialize back to JSON
	_, err = json.Marshal(parsed)
	if err != nil {
		t.Errorf("Failed to marshal parsed result: %v", err)
	}
}

// TestAdapterStreamProcessing Test stream processing
func TestAdapterStreamProcessing(t *testing.T) {
	adapter := &ClaudeAdapter{
		BaseAdapter: BaseAdapter{
			config: &CLIConfig{},
		},
	}

	// Simulate stream output
	stream := []string{
		`{"type":"chunk","content":"H"}`,
		`{"type":"chunk","content":"e"}`,
		`{"type":"chunk","content":"l"}`,
		`{"type":"chunk","content":"l"}`,
		`{"type":"chunk","content":"o"}`,
	}

	var fullMessage strings.Builder
	for _, chunk := range stream {
		parsed, _ := adapter.ParseMessage(chunk)
		if content, ok := parsed["content"].(string); ok {
			fullMessage.WriteString(content)
		}
	}

	if fullMessage.String() != "Hello" {
		t.Errorf("Expected 'Hello', got '%s'", fullMessage.String())
	}
}

// TestAdapterTimeout Test timeout handling
func TestAdapterTimeout(t *testing.T) {
	// Create a mock CLI that times out
	mockScript := `#!/bin/bash
sleep 10
echo "This should not appear"
`
	mockPath := mockCLI(t, "slow-cli", mockScript)

	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", filepath.Dir(mockPath)+":"+oldPath)
	defer os.Setenv("PATH", oldPath)

	adapter := &GenericAdapter{
		BaseAdapter: BaseAdapter{
			config: &CLIConfig{
				Provider: "slow-cli",
			},
		},
	}

	cmd := adapter.BuildCommand(adapter.config)

	// StartCommand
	if err := cmd.Start(); err != nil {
		t.Fatalf("Failed to start command: %v", err)
	}

	// Set timeout
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case <-time.After(2 * time.Second):
		// Timeout，killProcess
		cmd.Process.Kill()
		t.Log("Command timed out as expected")
	case err := <-done:
		t.Errorf("Command finished unexpectedly: %v", err)
	}
}
