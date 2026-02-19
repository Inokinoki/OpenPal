package adapter

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// CodexAdapterPure 纯 Go 实现的 Codex 适配器（不使用 Node.js SDK）
type CodexAdapterPure struct {
	config     *CLIConfig
	threadID   string
	sessionDir string
}

// NewCodexAdapterPure 创建纯 Go Codex 适配器
func NewCodexAdapterPure(config *CLIConfig, sessionDir string) *CodexAdapterPure {
	return &CodexAdapterPure{
		config:     config,
		sessionDir: sessionDir,
	}
}

// Start 启动 Codex 会话
func (a *CodexAdapterPure) Start() error {
	// 尝试加载 thread_id（恢复会话）
	return a.loadThreadID()
}

// BuildCommand 构建命令
func (a *CodexAdapterPure) BuildCommand(task string) *exec.Cmd {
	args := []string{"exec"}

	// 检查是否支持 --json
	if a.supportsJSON() {
		args = append(args, "--json")
	}

	// 如果有 thread_id，恢复会话
	if a.threadID != "" {
		args = append(args, "resume", "--last")
	}

	args = append(args, task)

	cmd := exec.Command("codex", args...)
	cmd.Dir = a.config.WorkDir
	return cmd
}

// SaveThreadID 保存 thread_id
func (a *CodexAdapterPure) SaveThreadID(id string) error {
	return os.WriteFile(
		filepath.Join(a.sessionDir, "codex_thread_id.txt"),
		[]byte(id),
		0644,
	)
}

// loadThreadID 加载 thread_id
func (a *CodexAdapterPure) loadThreadID() error {
	data, err := os.ReadFile(
		filepath.Join(a.sessionDir, "codex_thread_id.txt"),
	)
	if err != nil {
		return err
	}
	a.threadID = strings.TrimSpace(string(data))
	return nil
}

// supportsJSON 检查是否支持 --json 输出
func (a *CodexAdapterPure) supportsJSON() bool {
	cmd := exec.Command("codex", "exec", "--help")
	output, _ := cmd.CombinedOutput()
	return strings.Contains(string(output), "--json")
}

// ParseOutputLine 解析输出行
func (a *CodexAdapterPure) ParseOutputLine(line string) map[string]interface{} {
	// 尝试解析 JSON
	var msg map[string]interface{}
	if err := json.Unmarshal([]byte(line), &msg); err == nil {
		// JSON 模式
		if _, ok := msg["type"]; !ok {
			msg["type"] = "chunk"
		}

		// 提取 thread_id
		if id, ok := msg["thread_id"].(string); ok {
			a.threadID = id
			a.SaveThreadID(id)
		}

		return msg
	}

	// 文本模式
	return a.parseTextLine(line)
}

// parseTextLine 解析文本输出
func (a *CodexAdapterPure) parseTextLine(line string) map[string]interface{} {
	// 提取 thread_id
	if strings.HasPrefix(line, "thread_id:") {
		id := strings.TrimSpace(strings.TrimPrefix(line, "thread_id:"))
		a.threadID = id
		a.SaveThreadID(id)
		return map[string]interface{}{
			"type":      "thread_init",
			"thread_id": id,
		}
	}

	// 识别文件操作
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
