package adapter

import (
	"strings"
	"testing"
)

// TestClaudeAdapterSupportsJSONStream 测试 Claude 适配器 JSON Stream 支持
func TestClaudeAdapterSupportsJSONStream(t *testing.T) {
	adapter := &ClaudeAdapter{
		config: &CLIConfig{
			Provider: "claude",
			WorkDir:  "/tmp",
		},
	}

	// Claude 应该支持 JSON Stream
	if !adapter.SupportsJSONStream() {
		t.Error("Expected Claude adapter to support JSON Stream")
	}
}

// TestClaudeAdapterBuildCommand 测试 Claude 命令构建
func TestClaudeAdapterBuildCommand(t *testing.T) {
	adapter := &ClaudeAdapter{
		config: &CLIConfig{
			Provider: "claude",
			WorkDir:  "/workspace",
			Task:     "Refactor this code",
			Files:    []string{"src/main.go", "src/utils.go"},
		},
	}

	cmd := adapter.BuildCommand(adapter.config)

	// 验证命令
	if cmd.Path != "claude" {
		t.Errorf("Expected command 'claude', got %s", cmd.Path)
	}

	// 验证参数包含 -p 和 --output-format
	args := strings.Join(cmd.Args, " ")
	if !strings.Contains(args, "-p") {
		t.Error("Expected -p flag for print mode")
	}
	if !strings.Contains(args, "--output-format") {
		t.Error("Expected --output-format flag")
	}
	if !strings.Contains(args, "stream-json") {
		t.Error("Expected stream-json format")
	}
}

// TestClaudeAdapterParseMessage 测试 Claude 消息解析
func TestClaudeAdapterParseMessage(t *testing.T) {
	adapter := &ClaudeAdapter{
		config: &CLIConfig{
			Provider: "claude",
		},
	}

	// 测试 JSON 消息
	jsonMsg := `{"type": "assistant", "content": "Hello"}`
	parsed, err := adapter.ParseMessage(jsonMsg)
	if err != nil {
		t.Fatalf("Failed to parse JSON message: %v", err)
	}

	if parsed["type"] != "assistant" {
		t.Errorf("Expected type=assistant, got %v", parsed["type"])
	}

	// 测试文本消息
	textMsg := "Let me analyze this code..."
	parsed, err = adapter.ParseMessage(textMsg)
	if err != nil {
		t.Fatalf("Failed to parse text message: %v", err)
	}

	if parsed["type"] != "chunk" {
		t.Errorf("Expected type=chunk for text, got %v", parsed["type"])
	}
}

// TestCodexAdapterSupportsJSONStream 测试 Codex 适配器
func TestCodexAdapterSupportsJSONStream(t *testing.T) {
	adapter := &CodexAdapter{
		config: &CLIConfig{
			Provider: "codex",
			WorkDir:  "/tmp",
		},
	}

	// Codex 的 JSON Stream 支持需要验证（可能不支持）
	// 这里只是测试方法存在且可调用
	_ = adapter.SupportsJSONStream()
}

// TestCodexAdapterBuildCommand 测试 Codex 命令构建
func TestCodexAdapterBuildCommand(t *testing.T) {
	adapter := &CodexAdapter{
		config: &CLIConfig{
			Provider: "codex",
			WorkDir:  "/workspace",
			Task:     "Fix the bug",
		},
	}

	cmd := adapter.BuildCommand(adapter.config)

	// 验证命令
	if cmd.Path != "codex" {
		t.Errorf("Expected command 'codex', got %s", cmd.Path)
	}

	// 验证参数包含 exec
	args := strings.Join(cmd.Args, " ")
	if !strings.Contains(args, "exec") {
		t.Error("Expected 'exec' subcommand")
	}
}

// TestCopilotAdapterACP 测试 Copilot ACP 支持
func TestCopilotAdapterACP(t *testing.T) {
	adapter := &CopilotAdapter{
		config: &CLIConfig{
			Provider: "copilot",
		},
	}

	// Copilot 不支持 ACP（会降级到 text 模式）
	if adapter.SupportsACP() {
		t.Error("Copilot adapter should not support ACP in text mode")
	}
}

// TestGenericAdapter 测试通用适配器
func TestGenericAdapter(t *testing.T) {
	adapter := &GenericAdapter{
		config: &CLIConfig{
			Provider: "unknown-cli",
		},
	}

	// 通用适配器不支持 ACP 或 JSON Stream
	if adapter.SupportsACP() {
		t.Error("Generic adapter should not support ACP")
	}

	if adapter.SupportsJSONStream() {
		t.Error("Generic adapter should not support JSON Stream")
	}

	// 测试命令构建
	cmd := adapter.BuildCommand(adapter.config)
	if cmd.Path != "unknown-cli" {
		t.Errorf("Expected command 'unknown-cli', got %s", cmd.Path)
	}
}

// TestAdapterModeConstants 测试适配器模式常量
func TestAdapterModeConstants(t *testing.T) {
	if ModeACP != "acp" {
		t.Errorf("Expected ModeACP='acp', got %s", ModeACP)
	}

	if ModeText != "text" {
		t.Errorf("Expected ModeText='text', got %s", ModeText)
	}
}

// TestCLIProcessStop 测试 CLI 进程停止
func TestCLIProcessStop(t *testing.T) {
	// 创建一个 mock 进程
	process := &CLIProcess{
		Pid: 12345,
	}

	// Stop 方法不应该 panic
	err := process.Stop()
	// 注意：因为没有实际进程，可能会失败，但不应该 panic
	_ = err
}

// TestCLIConfig 测试 CLI 配置
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

// TestAdapterInterface 测试适配器接口实现
func TestAdapterInterface(t *testing.T) {
	// 验证所有适配器都实现了 Adapter 接口
	var _ Adapter = &ClaudeAdapter{}
	var _ Adapter = &CodexAdapter{}
	var _ Adapter = &CopilotAdapter{}
	var _ Adapter = &GenericAdapter{}
}

// TestParseMessageError 测试错误消息解析
func TestParseMessageError(t *testing.T) {
	adapter := &ClaudeAdapter{
		config: &CLIConfig{},
	}

	// 测试无效 JSON
	invalidJSON := `{"type": "chunk", invalid}`
	parsed, err := adapter.ParseMessage(invalidJSON)
	if err != nil {
		// 应该降级到文本模式，不返回错误
		t.Logf("Got error (expected): %v", err)
	}

	// 即使解析失败，也应该返回某种结果
	if parsed == nil {
		t.Error("Expected non-nil result even on parse error")
	}
}

// TestSupportsACPFunction 测试 supportsACP 函数
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
