package adapter

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sync"
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
	provider  string
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	sessionID string
	seq       int64
	mu        sync.Mutex
}

// NewACPClient Create ACP client
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

	// Don't start the process here - wait for Start() to be called
	// This allows on-demand CLI startup

	return &ACPClient{
		provider: provider,
		cmd:      cmd,
		stdin:    nil,  // Will be set in Start()
		stdout:   nil,  // Will be set in Start()
		seq:      0,
	}, nil
}

// Start Start ACP client (start process and initialize)
func (c *ACPClient) Start() error {
	// Start the process if not already started
	if c.stdin == nil || c.stdout == nil {
		var err error
		c.stdin, err = c.cmd.StdinPipe()
		if err != nil {
			return fmt.Errorf("ACP stdin pipe failed: %w", err)
		}

		c.stdout, err = c.cmd.StdoutPipe()
		if err != nil {
			return fmt.Errorf("ACP stdout pipe failed: %w", err)
		}

		if err := c.cmd.Start(); err != nil {
			return fmt.Errorf("ACP process start failed: %w", err)
		}

		log.Printf("[DEBUG] ACP process started: PID=%d, provider=%s", c.cmd.Process.Pid, c.provider)
	}

	// Send initialize request and read response
	initializeResult := make(map[string]interface{})
	err := c.sendRequest("initialize", map[string]interface{}{
		"protocolVersion":    1,  // ACP protocol version (must be <= 65535)
		"clientCapabilities": map[string]interface{}{},
	}, &initializeResult)

	if err != nil {
		return fmt.Errorf("ACP initialize failed: %w", err)
	}

	log.Printf("[DEBUG] ACP initialized: %+v", initializeResult)

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
		log.Printf("[DEBUG] NewSession: session/new failed: %v", err)
		return "", err
	}

	log.Printf("[DEBUG] NewSession: raw result: %+v", result)
	log.Printf("[DEBUG] NewSession: sessionID: '%s'", result.SessionID)
	
	c.sessionID = result.SessionID
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

// Listen Listen ACP Message
func (c *ACPClient) Listen(handler func(*ACPMessage)) error {
	scanner := bufio.NewScanner(c.stdout)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var msg ACPMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			// ParseFail，Skip
			continue
		}

		handler(&msg)
	}

	return scanner.Err()
}

// Stop Stop ACP client
func (c *ACPClient) Stop() error {
	if c.cmd != nil && c.cmd.Process != nil {
		return c.cmd.Process.Kill()
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

	log.Printf("[DEBUG] sendRequest: sent %s (id=%d)", method, id)

	// Read response if result is expected
	if result != nil {
		reader := bufio.NewReader(c.stdout)
		line, err := reader.ReadBytes('\n')
		if err != nil {
			log.Printf("[DEBUG] sendRequest: read response failed: %v", err)
			return fmt.Errorf("read response failed: %w", err)
		}

		log.Printf("[DEBUG] sendRequest: received %d bytes", len(line))

		// Parse response
		var response map[string]interface{}
		if err := json.Unmarshal(line, &response); err != nil {
			log.Printf("[DEBUG] sendRequest: parse response failed: %v", err)
			return fmt.Errorf("parse response failed: %w", err)
		}

		log.Printf("[DEBUG] sendRequest: response: %+v", response)

		// Check for error
		if errMsg, ok := response["error"]; ok && errMsg != nil {
			log.Printf("[DEBUG] sendRequest: response error: %+v", errMsg)
			return fmt.Errorf("ACP error: %+v", errMsg)
		}

		// Extract result
		if respResult, ok := response["result"]; ok {
			resultData, err := json.Marshal(respResult)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(resultData, result); err != nil {
				log.Printf("[DEBUG] sendRequest: unmarshal result failed: %v", err)
				return err
			}
			log.Printf("[DEBUG] sendRequest: result unmarshaled: %+v", result)
		}
	}

	return nil
}

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
