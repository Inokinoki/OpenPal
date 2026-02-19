package adapter

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// ACPMessage ACP 协议消息
type ACPMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *ACPError       `json:"error,omitempty"`
}

// ACPError ACP 错误
type ACPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

// ACPSessionUpdate ACP 会话更新通知
type ACPSessionUpdate struct {
	SessionID     string     `json:"sessionId"`
	SessionUpdate string     `json:"sessionUpdate"`
	Content       ACPContent `json:"content"`
}

// ACPContent ACP 内容
type ACPContent struct {
	Type string `json:"type"` // text, diff, command, etc.
	Text string `json:"text,omitempty"`
}

// ACPClient ACP 客户端
type ACPClient struct {
	provider  string
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	sessionID string
	seq       int64
	mu        sync.Mutex
}

// NewACPClient 创建 ACP 客户端
func NewACPClient(provider string) (*ACPClient, error) {
	var cmd *exec.Cmd

	switch provider {
	case "copilot", "copilot-acp":
		cmd = exec.Command("copilot", "--acp", "--stdio")
	case "opencode":
		cmd = exec.Command("opencode", "acp")
	default:
		return nil, fmt.Errorf("unsupported ACP provider: %s", provider)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	return &ACPClient{
		provider: provider,
		cmd:      cmd,
		stdin:    stdin,
		stdout:   stdout,
		seq:      0,
	}, nil
}

// Start 启动 ACP 客户端（初始化）
func (c *ACPClient) Start() error {
	// 发送 initialize 请求
	err := c.sendRequest("initialize", map[string]interface{}{
		"protocolVersion":    "2025-06-18",
		"clientCapabilities": map[string]interface{}{},
	}, nil)

	if err != nil {
		return fmt.Errorf("ACP initialize failed: %w", err)
	}

	return nil
}

// NewSession 创建新会话
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
		return "", err
	}

	c.sessionID = result.SessionID
	return result.SessionID, nil
}

// Prompt 发送提示
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

// Listen 监听 ACP 消息
func (c *ACPClient) Listen(handler func(*ACPMessage)) error {
	scanner := bufio.NewScanner(c.stdout)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var msg ACPMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			// 解析失败，跳过
			continue
		}

		handler(&msg)
	}

	return scanner.Err()
}

// Stop 停止 ACP 客户端
func (c *ACPClient) Stop() error {
	if c.cmd != nil && c.cmd.Process != nil {
		return c.cmd.Process.Kill()
	}
	return nil
}

// Pid 获取进程 ID
func (c *ACPClient) Pid() int {
	if c.cmd != nil {
		return c.cmd.Process.Pid
	}
	return 0
}

func (c *ACPClient) sendRequest(method string, params interface{}, result interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.seq++
	id := c.seq

	msg := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	_, err = c.stdin.Write(append(data, '\n'))
	if err != nil {
		return err
	}

	// 如果需要结果，等待响应（简化实现）
	// 实际实现需要异步处理和请求 ID 匹配
	if result != nil {
		// 简单延迟等待（实际应该用 channel 和 select）
		time.Sleep(100 * time.Millisecond)
		// TODO: 实现正确的响应等待机制
	}

	return nil
}

// ParseMessage 解析 ACP 消息为 Bridge Event
func (c *ACPClient) ParseMessage(msg *ACPMessage) map[string]interface{} {
	// 处理通知
	if msg.Method == "session/update" {
		var update ACPSessionUpdate
		if err := json.Unmarshal(msg.Params, &update); err != nil {
			return map[string]interface{}{
				"type":    "error",
				"content": fmt.Sprintf("Failed to parse update: %v", err),
			}
		}

		// 根据更新类型返回不同格式
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

	// 处理响应
	if msg.Result != nil {
		var result map[string]interface{}
		if err := json.Unmarshal(msg.Result, &result); err == nil {
			return map[string]interface{}{
				"type":   "result",
				"result": result,
			}
		}
	}

	// 处理错误
	if msg.Error != nil {
		return map[string]interface{}{
			"type":    "error",
			"code":    msg.Error.Code,
			"message": msg.Error.Message,
		}
	}

	// 未知消息类型
	return map[string]interface{}{
		"type":    "unknown",
		"message": msg,
	}
}
