package adapter

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// CLIConfig CLI 配置
type CLIConfig struct {
	Provider string
	WorkDir  string
	Task     string
	Files    []string
	Options  map[string]string
}

// CLIProcess CLI 进程
type CLIProcess struct {
	Cmd    *exec.Cmd
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
	Stderr io.ReadCloser
	Pid    int
}

// Stop 停止 CLI 进程
func (c *CLIProcess) Stop() error {
	if c.Cmd != nil && c.Cmd.Process != nil {
		return c.Cmd.Process.Kill()
	}
	return nil
}

// Adapter CLI 适配器接口
type Adapter interface {
	SupportsACP() bool
	SupportsJSONStream() bool // 支持 JSON Stream 输出
	BuildCommand(config *CLIConfig) *exec.Cmd
	ParseMessage(line string) (map[string]interface{}, error)
	SendCommand(cmd string, params map[string]interface{}) error
	GetCapabilities() []string
}

// Manager 适配器管理器
type Manager struct {
	adapter   Adapter
	acpClient *ACPClient // ACP 客户端（如果支持）
	config    *CLIConfig
	mode      AdapterMode // ACP 或 Text 模式
}

// AdapterMode 适配器模式
type AdapterMode string

const (
	ModeACP  AdapterMode = "acp"  // ACP 协议模式
	ModeText AdapterMode = "text" // 文本解析模式
)

// NewAdapter 创建适配器
func NewAdapter(provider, workDir string) *Manager {
	config := &CLIConfig{
		Provider: provider,
		WorkDir:  workDir,
		Options:  make(map[string]string),
	}

	// 检查是否支持 ACP
	if supportsACP(provider) {
		acpClient, err := NewACPClient(provider)
		if err == nil {
			// 初始化 ACP 客户端
			if err := acpClient.Start(); err == nil {
				// 创建会话
				_, err := acpClient.NewSession(workDir, []interface{}{})
				if err == nil {
					return &Manager{
						acpClient: acpClient,
						config:    config,
						mode:      ModeACP,
					}
				}
			}
		}
		// ACP 初始化失败，降级到文本模式
	}

	// 文本模式
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

	return &Manager{
		adapter: adapter,
		config:  config,
		mode:    ModeText,
	}
}

// supportsACP 检查 provider 是否支持 ACP
func supportsACP(provider string) bool {
	switch provider {
	case "copilot", "copilot-acp":
		return true // GitHub Copilot 支持 ACP
	case "opencode":
		return true // OpenCode 支持 ACP
	default:
		return false
	}
}

// Start 启动 CLI
func (m *Manager) Start() (*CLIProcess, error) {
	// ACP 模式
	if m.mode == ModeACP && m.acpClient != nil {
		return &CLIProcess{
			Cmd:    m.acpClient.cmd,
			Stdin:  m.acpClient.stdin,
			Stdout: m.acpClient.stdout,
			Stderr: nil, // ACP 通常不使用 stderr
			Pid:    m.acpClient.Pid(),
		}, nil
	}

	// 文本模式
	cmd := m.adapter.BuildCommand(m.config)
	cmd.Dir = m.config.WorkDir

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

	return &CLIProcess{
		Cmd:    cmd,
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
		Pid:    cmd.Process.Pid,
	}, nil
}

// SendCommand 发送命令到 CLI
func (m *Manager) SendCommand(cmd string, params map[string]interface{}) error {
	return m.adapter.SendCommand(cmd, params)
}

// GetCapabilities 获取 CLI 能力
func (m *Manager) GetCapabilities() []string {
	return m.adapter.GetCapabilities()
}

// ClaudeAdapter Claude Code 适配器
type ClaudeAdapter struct {
	config *CLIConfig
	mu     sync.Mutex
	stdin  io.WriteCloser
}

func (a *ClaudeAdapter) SupportsACP() bool {
	// Claude Code 不支持 ACP，但支持 JSON stream 输出
	return false
}

func (a *ClaudeAdapter) SupportsJSONStream() bool {
	// Claude Code 支持 --output-format stream-json
	return true
}

func (a *ClaudeAdapter) BuildCommand(config *CLIConfig) *exec.Cmd {
	args := []string{
		"-p",                             // print mode (非交互)
		"--output-format", "stream-json", // JSON stream 输出
	}

	// 文件参数
	for _, file := range config.Files {
		args = append(args, "--add-dir", file)
	}

	// 任务描述作为最后一个参数
	if config.Task != "" {
		args = append(args, config.Task)
	}

	return exec.Command("claude", args...)
}

func (a *ClaudeAdapter) ParseMessage(line string) (map[string]interface{}, error) {
	// Claude Code 输出为人类可读文本，需要模式匹配解析

	// 尝试解析 JSON（如果 CLI 支持）
	var msg map[string]interface{}
	if err := json.Unmarshal([]byte(line), &msg); err == nil {
		// 处理 null、数组、数字等非对象类型
		if msg == nil {
			return map[string]interface{}{
				"type":    "chunk",
				"content": line,
			}, nil
		}
		if _, ok := msg["type"]; !ok {
			msg["type"] = "chunk"
		}
		return msg, nil
	}

	// 文本模式匹配
	parsed := a.parseTextOutput(line)
	return parsed, nil
}

// parseTextOutput 解析 Claude Code 文本输出
func (a *ClaudeAdapter) parseTextOutput(line string) map[string]interface{} {
	// 识别代码块
	if strings.HasPrefix(line, "```") {
		return map[string]interface{}{
			"type":    "code_block",
			"content": line,
		}
	}

	// 识别文件操作
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

	// 识别命令执行
	if strings.Contains(lower, "running") ||
		strings.Contains(lower, "executing") {
		return map[string]interface{}{
			"type":    "command",
			"content": line,
		}
	}

	// 默认为文本输出
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

// CodexAdapter Codex CLI 适配器
type CodexAdapter struct {
	config     *CLIConfig
	threadID   string // 保存的会话 ID
	sessionDir string // 会话目录
}

func (a *CodexAdapter) SupportsACP() bool {
	// 根据调研，Codex CLI 不支持标准 ACP 格式
	return false
}

func (a *CodexAdapter) SupportsJSONStream() bool {
	// 检查 codex exec 是否支持 --json
	cmd := exec.Command("codex", "exec", "--help")
	output, _ := cmd.CombinedOutput()
	return strings.Contains(string(output), "--json")
}

func (a *CodexAdapter) BuildCommand(config *CLIConfig) *exec.Cmd {
	args := []string{"exec"}

	// 如果支持 JSON，添加 --json
	if a.SupportsJSONStream() {
		args = append(args, "--json")
	}

	// 如果有 thread_id，使用 resume 恢复会话
	if a.threadID != "" {
		args = append(args, "resume", "--last")
	}

	if config.Task != "" {
		args = append(args, config.Task)
	}

	return exec.Command("codex", args...)
}

func (a *CodexAdapter) ParseMessage(line string) (map[string]interface{}, error) {
	// 尝试解析 JSON
	var msg map[string]interface{}
	if err := json.Unmarshal([]byte(line), &msg); err == nil {
		if _, ok := msg["type"]; !ok {
			msg["type"] = "chunk"
		}
		return msg, nil
	}

	// 文本模式匹配
	parsed := a.parseTextOutput(line)
	return parsed, nil
}

// parseTextOutput 解析 Codex 文本输出
func (a *CodexAdapter) parseTextOutput(line string) map[string]interface{} {
	// 类似 Claude 的解析逻辑
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
	// Codex CLI 可能不支持交互式命令
	return fmt.Errorf("Codex CLI does not support interactive commands")
}

func (a *CodexAdapter) GetCapabilities() []string {
	return []string{"text_output", "streaming"}
}

// CopilotAdapter GitHub Copilot CLI 适配器
type CopilotAdapter struct {
	config *CLIConfig
}

func (a *CopilotAdapter) SupportsACP() bool {
	// Copilot 支持 ACP，在 NewAdapter 中会优先使用 ACP 模式
	// 如果降级到 Text 模式，返回 false
	return false
}

func (a *CopilotAdapter) SupportsJSONStream() bool {
	// 待确认，先返回 false
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
	// 尝试解析 JSON
	var msg map[string]interface{}
	if err := json.Unmarshal([]byte(line), &msg); err == nil {
		if _, ok := msg["type"]; !ok {
			msg["type"] = "chunk"
		}
		return msg, nil
	}

	// 文本模式匹配
	parsed := a.parseTextOutput(line)
	return parsed, nil
}

// parseTextOutput 解析 Copilot 文本输出
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

// GenericAdapter 通用适配器（用于未知 CLI）
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

// StreamForwarder 流转发器
type StreamForwarder struct {
	reader  io.Reader
	handler func(string)
	done    chan struct{}
}

// NewStreamForwarder 创建流转发器
func NewStreamForwarder(reader io.Reader, handler func(string)) *StreamForwarder {
	return &StreamForwarder{
		reader:  reader,
		handler: handler,
		done:    make(chan struct{}),
	}
}

// Start 开始转发
func (f *StreamForwarder) Start() {
	go func() {
		defer close(f.done)

		scanner := bufio.NewScanner(f.reader)
		for scanner.Scan() {
			f.handler(scanner.Text())
		}
	}()
}

// Done 等待完成
func (f *StreamForwarder) Done() <-chan struct{} {
	return f.done
}

// ACPMessageHandler ACP 消息处理器
type ACPMessageHandler struct {
	client  *ACPClient
	handler func(map[string]interface{})
}

// NewACPMessageHandler 创建 ACP 消息处理器
func NewACPMessageHandler(client *ACPClient, handler func(map[string]interface{})) *ACPMessageHandler {
	return &ACPMessageHandler{
		client:  client,
		handler: handler,
	}
}

// Start 开始处理 ACP 消息
func (h *ACPMessageHandler) Start() {
	h.client.Listen(func(msg *ACPMessage) {
		parsed := h.client.ParseMessage(msg)
		h.handler(parsed)
	})
}

// SendCommand 发送命令到 ACP Server
func (m *Manager) SendACPPrompt(prompt string) error {
	if m.mode != ModeACP || m.acpClient == nil {
		return fmt.Errorf("ACP mode not enabled")
	}
	return m.acpClient.Prompt(prompt)
}

// GetMode 获取当前模式
func (m *Manager) GetMode() AdapterMode {
	return m.mode
}
