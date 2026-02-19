package adapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mockCLI 创建一个 mock CLI 脚本
func mockCLI(t *testing.T, name, script string) string {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, name)

	// 写入脚本
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("Failed to write mock script: %v", err)
	}

	return scriptPath
}

// TestClaudeAdapterWithMock 使用 Mock 测试 Claude 适配器
func TestClaudeAdapterWithMock(t *testing.T) {
	// 创建 mock claude 命令
	mockScript := `#!/bin/bash
echo '{"type":"assistant","content":"Hello from mock Claude!"}'
echo '{"type":"chunk","content":"This is a test response"}'
`
	mockPath := mockCLI(t, "claude", mockScript)

	// 临时添加 mock 到 PATH
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", filepath.Dir(mockPath)+":"+oldPath)
	defer os.Setenv("PATH", oldPath)

	// 创建适配器
	adapter := &ClaudeAdapter{
		config: &CLIConfig{
			Provider: "claude",
			WorkDir:  "/tmp",
			Task:     "Test task",
		},
	}

	// 测试命令构建
	cmd := adapter.BuildCommand(adapter.config)
	if cmd.Path != mockPath {
		t.Logf("Using mock claude at: %s", cmd.Path)
	}

	// 测试消息解析
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

// TestClaudeAdapterMockOutput 测试 Claude 输出解析
func TestClaudeAdapterMockOutput(t *testing.T) {
	adapter := &ClaudeAdapter{
		config: &CLIConfig{
			Provider: "claude",
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
			wantErr:  false, // 应该降级到文本模式
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

// TestCodexAdapterMock 使用 Mock 测试 Codex 适配器
func TestCodexAdapterMock(t *testing.T) {
	// 创建 mock codex 命令
	mockScript := `#!/bin/bash
echo '{"message":{"role":"assistant","content":"Codex response"}}'
`
	mockPath := mockCLI(t, "codex", mockScript)

	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", filepath.Dir(mockPath)+":"+oldPath)
	defer os.Setenv("PATH", oldPath)

	adapter := &CodexAdapter{
		config: &CLIConfig{
			Provider: "codex",
			WorkDir:  "/tmp",
			Task:     "Fix bug",
		},
	}

	// 测试命令构建
	cmd := adapter.BuildCommand(adapter.config)
	if !strings.Contains(cmd.Path, "codex") {
		t.Errorf("Expected codex command, got %s", cmd.Path)
	}

	// 测试 Codex 特定格式解析
	msg := `{"message":{"role":"assistant","content":"Here's the fix"}}`
	parsed, err := adapter.ParseMessage(msg)
	if err != nil {
		t.Fatalf("Failed to parse Codex message: %v", err)
	}

	// Codex 应该提取 message.content
	if parsed["type"] != "chunk" {
		t.Errorf("Expected type=chunk, got %v", parsed["type"])
	}
}

// TestCopilotAdapterMock 使用 Mock 测试 Copilot 适配器
func TestCopilotAdapterMock(t *testing.T) {
	adapter := &CopilotAdapter{
		config: &CLIConfig{
			Provider: "copilot",
		},
	}

	// Copilot 不支持 ACP
	if adapter.SupportsACP() {
		t.Error("Copilot should not support ACP")
	}

	// 测试命令构建
	cmd := adapter.BuildCommand(adapter.config)
	if !strings.Contains(cmd.Path, "copilot") {
		t.Errorf("Expected copilot command, got %s", cmd.Path)
	}

	// 测试消息解析
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

// TestGenericAdapterMock 测试通用适配器
func TestGenericAdapterMock(t *testing.T) {
	adapter := &GenericAdapter{
		config: &CLIConfig{
			Provider: "unknown-cli",
		},
	}

	// 通用适配器不支持特殊功能
	if adapter.SupportsACP() {
		t.Error("Generic adapter should not support ACP")
	}
	if adapter.SupportsJSONStream() {
		t.Error("Generic adapter should not support JSON Stream")
	}

	// 测试命令构建
	cmd := adapter.BuildCommand(adapter.config)
	if cmd.Path != "unknown-cli" {
		t.Errorf("Expected unknown-cli, got %s", cmd.Path)
	}
}

// TestAdapterWithRealisticOutput 测试真实场景输出
func TestAdapterWithRealisticOutput(t *testing.T) {
	adapter := &ClaudeAdapter{
		config: &CLIConfig{
			Provider: "claude",
		},
	}

	// 模拟真实的 Claude 输出流
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

// TestAdapterErrorHandling 测试错误处理
func TestAdapterErrorHandling(t *testing.T) {
	adapter := &ClaudeAdapter{
		config: &CLIConfig{
			Provider: "claude",
		},
	}

	// 测试各种错误场景
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
			// 不应该 panic
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("ParseMessage panicked: %v", r)
				}
			}()

			parsed, _ := adapter.ParseMessage(tc.input)
			// 即使解析失败，也应该返回某种结果（降级到文本模式）
			if parsed == nil {
				t.Error("Expected non-nil result even on error")
			}
		})
	}
}

// TestAdapterCommandBuilder 测试命令构建器
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
				adapter = &ClaudeAdapter{config: tt.config}
			case "codex":
				adapter = &CodexAdapter{config: tt.config}
			default:
				adapter = &GenericAdapter{config: tt.config}
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

// TestAdapterCapabilities 测试适配器能力
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
			adapter:        &ClaudeAdapter{config: &CLIConfig{}},
			wantACP:        false,
			wantJSONStream: true,
		},
		{
			name:           "codex",
			adapter:        &CodexAdapter{config: &CLIConfig{}},
			wantACP:        false,
			wantJSONStream: false,
		},
		{
			name:           "copilot",
			adapter:        &CopilotAdapter{config: &CLIConfig{}},
			wantACP:        false,
			wantJSONStream: false,
		},
		{
			name:           "generic",
			adapter:        &GenericAdapter{config: &CLIConfig{}},
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

// TestAdapterModeDetection 测试适配器模式检测
func TestAdapterModeDetection(t *testing.T) {
	// 测试 supportsACP 函数
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

// TestAdapterJSONParsing 测试 JSON 解析
func TestAdapterJSONParsing(t *testing.T) {
	adapter := &ClaudeAdapter{
		config: &CLIConfig{},
	}

	// 测试复杂的 JSON 结构
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

	// 验证可以序列化回 JSON
	_, err = json.Marshal(parsed)
	if err != nil {
		t.Errorf("Failed to marshal parsed result: %v", err)
	}
}

// TestAdapterStreamProcessing 测试流处理
func TestAdapterStreamProcessing(t *testing.T) {
	adapter := &ClaudeAdapter{
		config: &CLIConfig{},
	}

	// 模拟流式输出
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

// TestAdapterTimeout 测试超时处理
func TestAdapterTimeout(t *testing.T) {
	// 创建一个会超时的 mock CLI
	mockScript := `#!/bin/bash
sleep 10
echo "This should not appear"
`
	mockPath := mockCLI(t, "slow-cli", mockScript)

	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", filepath.Dir(mockPath)+":"+oldPath)
	defer os.Setenv("PATH", oldPath)

	adapter := &GenericAdapter{
		config: &CLIConfig{
			Provider: "slow-cli",
		},
	}

	cmd := adapter.BuildCommand(adapter.config)

	// 启动命令
	if err := cmd.Start(); err != nil {
		t.Fatalf("Failed to start command: %v", err)
	}

	// 设置超时
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case <-time.After(2 * time.Second):
		// 超时，杀死进程
		cmd.Process.Kill()
		t.Log("Command timed out as expected")
	case err := <-done:
		t.Errorf("Command finished unexpectedly: %v", err)
	}
}
