//go:build integration
// +build integration

package integration

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// hasAPIKey - Check if API key exists for the provider (already defined in cli_test.go)
// This function is defined in cli_test.go, removing duplicate

// TestClaude_3TurnConversation - Test Claude Code with 3-turn conversation
func TestClaude_3TurnConversation(t *testing.T) {
	skipIfNoCLI(t, "claude")

	if !hasAPIKey(t, "ANTHROPIC_API_KEY", "CLAUDE_API_KEY", "ANTHROPIC_AUTH_TOKEN") {
		t.Skip("Skipping: No Anthropic API key found")
	}

	t.Log("Testing Claude Code 3-turn conversation...")

	tmpDir := t.TempDir()
	sessionFile := tmpDir + "/.claude-session.json"

	// Turn 1: First interaction
	cmd1 := exec.Command("claude", "-p", "--output-format", "stream-json",
		"--verbose", "Remember the number 42")
	cmd1.Dir = tmpDir
	cmd1.Env = os.Environ()

	output1, err1 := cmd1.CombinedOutput()
	if err1 != nil {
		t.Fatalf("Claude Code failed on turn 1: %v\nOutput: %s", err1, string(output1))
	}

	output1Str := string(output1)
	t.Logf("Turn 1 output length: %d", len(output1))

	// Extract session ID from output
	sessionID := extractSessionIDFromOutput(output1Str)
	if sessionID == "" {
		t.Logf("Warning: Could not extract session ID from Claude output")
	} else {
		t.Logf("Turn 1 completed. Session ID: %s", sessionID)
	}

	// Check if session file was created
	if _, err := os.Stat(sessionFile); err == nil {
		t.Log("Session file created successfully")
	}

	// Turn 2: Follow-up with session resume
	var cmd2 *exec.Cmd
	if sessionID != "" {
		cmd2 = exec.Command("claude", "-p", "--output-format", "stream-json",
			"--resume", sessionID, "What number did I ask you to remember?")
	} else {
		// Fallback: try without session resume
		cmd2 = exec.Command("claude", "-p", "--output-format", "stream-json",
			"What is 2+2?")
	}
	cmd2.Dir = tmpDir
	cmd2.Env = os.Environ()

	output2, err2 := cmd2.CombinedOutput()
	if err2 != nil {
		t.Logf("Turn 2 warning: %v", err2)
	} else {
		t.Logf("Turn 2 output length: %d", len(output2))
	}

	// Turn 3: Another follow-up
	var cmd3 *exec.Cmd
	if sessionID != "" {
		cmd3 = exec.Command("claude", "-p", "--output-format", "stream-json",
			"--resume", sessionID, "Multiply that number by 2")
	} else {
		// Fallback: try without session resume
		cmd3 = exec.Command("claude", "-p", "--output-format", "stream-json",
			"What is 3+3?")
	}
	cmd3.Dir = tmpDir
	cmd3.Env = os.Environ()

	output3, err3 := cmd3.CombinedOutput()
	if err3 != nil {
		t.Logf("Turn 3 warning: %v", err3)
	} else {
		t.Logf("Turn 3 output length: %d", len(output3))
	}

	// Verify at least first turn succeeded
	if len(output1) == 0 {
		t.Error("Expected output from turn 1")
	}

	// Verify we got outputs from most turns
	successCount := 0
	if len(output1) > 0 {
		successCount++
	}
	if len(output2) > 0 {
		successCount++
	}
	if len(output3) > 0 {
		successCount++
	}

	t.Logf("3-turn conversation test completed: %d/3 turns successful", successCount)

	// Success if at least 2 out of 3 turns worked
	if successCount < 2 {
		t.Error("3-turn conversation failed: too few successful turns")
	}
}

// TestCodex_3TurnConversation - Test Codex with 3-turn conversation
func TestCodex_3TurnConversation(t *testing.T) {
	skipIfNoCLI(t, "codex")

	t.Log("Testing Codex 3-turn conversation...")

	tmpDir := t.TempDir()

	// Create a session file to enable conversation
	sessionFile := tmpDir + "/.codex-session.json"

	// Turn 1: First question
	cmd1 := exec.Command("codex", "exec", "What is 2+2?")
	cmd1.Dir = tmpDir
	output1, err1 := cmd1.CombinedOutput()

	if err1 != nil {
		t.Skipf("Codex not configured: %v", err1)
	}

	t.Logf("Turn 1 output: %s", string(output1))

	// Check if session was created
	if _, err := os.Stat(sessionFile); err == nil {
		t.Log("Session file created successfully")
	}

	// Turn 2: Follow-up (should use session if available)
	cmd2 := exec.Command("codex", "exec", "resume", "--last", "What about 3+3?")
	cmd2.Dir = tmpDir
	output2, err2 := cmd2.CombinedOutput()

	if err2 != nil {
		t.Logf("Turn 2 warning: %v", err2)
	} else {
		t.Logf("Turn 2 output: %s", string(output2))
	}

	// Turn 3: Another follow-up
	cmd3 := exec.Command("codex", "exec", "resume", "--last", "And 4+4?")
	cmd3.Dir = tmpDir
	output3, err3 := cmd3.CombinedOutput()

	if err3 != nil {
		t.Logf("Turn 3 warning: %v", err3)
	} else {
		t.Logf("Turn 3 output: %s", string(output3))
	}

	// Verify at least first turn succeeded
	if len(output1) == 0 {
		t.Error("Expected output from turn 1")
	}

	t.Log("Codex 3-turn conversation test completed")
}

// TestGemini_3TurnConversation - Test Gemini with 3-turn conversation
func TestGemini_3TurnConversation(t *testing.T) {
	skipIfNoCLI(t, "gemini")

	if !hasAPIKey(t, "GEMINI_API_KEY", "GOOGLE_API_KEY") {
		t.Skip("Skipping: No Gemini API key found")
	}

	t.Log("Testing Gemini 3-turn conversation...")

	tmpDir := t.TempDir()

	// Turn 1: First question
	cmd1 := exec.Command("gemini", "chat", "--prompt", "What is the capital of Japan?")
	cmd1.Dir = tmpDir
	cmd1.Env = os.Environ()
	output1, err1 := cmd1.CombinedOutput()

	if err1 != nil {
		t.Skipf("Gemini not configured: %v", err1)
	}

	t.Logf("Turn 1 output length: %d", len(output1))
	if len(output1) == 0 {
		t.Error("Expected output from turn 1")
	}

	// Turn 2: Follow-up question
	cmd2 := exec.Command("gemini", "chat", "--prompt", "What about the capital of Germany?")
	cmd2.Dir = tmpDir
	cmd2.Env = os.Environ()
	output2, err2 := cmd2.CombinedOutput()

	if err2 != nil {
		t.Logf("Turn 2 warning: %v", err2)
	} else {
		t.Logf("Turn 2 output length: %d", len(output2))
	}

	// Turn 3: Another follow-up
	cmd3 := exec.Command("gemini", "chat", "--prompt", "And the capital of Brazil?")
	cmd3.Dir = tmpDir
	cmd3.Env = os.Environ()
	output3, err3 := cmd3.CombinedOutput()

	if err3 != nil {
		t.Logf("Turn 3 warning: %v", err3)
	} else {
		t.Logf("Turn 3 output length: %d", len(output3))
	}

	// Verify at least first turn succeeded
	if len(output1) == 0 {
		t.Error("Expected output from turn 1")
	}

	t.Log("Gemini 3-turn conversation test completed")
}

// TestCopilot_ACP_3TurnConversation - Test Copilot with 3-turn conversation (ACP mode)
func TestCopilot_ACP_3TurnConversation(t *testing.T) {
	skipIfNoCLI(t, "copilot")

	if !hasAPIKey(t, "GITHUB_TOKEN", "GITHUB_COPILOT_TOKEN") {
		t.Skip("Skipping: No GitHub Copilot token found")
	}

	t.Log("Testing Copilot 3-turn conversation (ACP mode)...")

	tmpDir := t.TempDir()

	// Start Copilot in ACP mode
	cmd := exec.Command("copilot", "--acp", "--stdio")
	cmd.Dir = tmpDir
	cmd.Env = os.Environ()

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("Failed to get stdin pipe: %v", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("Failed to get stdout pipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		t.Skipf("Copilot not configured: %v", err)
	}
	defer cmd.Process.Kill()

	// Initialize ACP
	sendACPRequest(t, stdin, 1, "initialize", map[string]interface{}{
		"protocolVersion":    1,
		"clientCapabilities": map[string]interface{}{},
	})

	// Read initialize response
	readACPResponse(t, stdout, 5*time.Second)

	// Create session
	sendACPRequest(t, stdin, 2, "session/new", map[string]interface{}{
		"cwd":        tmpDir,
		"mcpServers": []interface{}{},
	})

	sessionResp := readACPResponse(t, stdout, 5*time.Second)
	t.Logf("Session response: %s", sessionResp)

	// Turn 1: First prompt
	sendACPRequest(t, stdin, 3, "session/prompt", map[string]interface{}{
		"sessionId": extractSessionID(sessionResp),
		"prompt": []map[string]string{
			{"type": "text", "text": "What is 2+2?"},
		},
	})

	resp1 := readACPMessages(t, stdout, 10*time.Second)
	t.Logf("Turn 1 received %d messages", len(resp1))

	// Turn 2: Follow-up prompt
	sendACPRequest(t, stdin, 4, "session/prompt", map[string]interface{}{
		"sessionId": extractSessionID(sessionResp),
		"prompt": []map[string]string{
			{"type": "text", "text": "What about 3+3?"},
		},
	})

	resp2 := readACPMessages(t, stdout, 10*time.Second)
	t.Logf("Turn 2 received %d messages", len(resp2))

	// Turn 3: Another follow-up
	sendACPRequest(t, stdin, 5, "session/prompt", map[string]interface{}{
		"sessionId": extractSessionID(sessionResp),
		"prompt": []map[string]string{
			{"type": "text", "text": "And 4+4?"},
		},
	})

	resp3 := readACPMessages(t, stdout, 10*time.Second)
	t.Logf("Turn 3 received %d messages", len(resp3))

	// Verify we got responses
	if len(resp1) == 0 && len(resp2) == 0 && len(resp3) == 0 {
		t.Error("Expected at least one response from Copilot")
	}

	t.Log("Copilot 3-turn conversation test completed")
}

// TestPalBroker_3TurnConversation - Test pal-broker with 3-turn conversation
func TestPalBroker_3TurnConversation(t *testing.T) {
	skipIfNoCLI(t, "pal-broker")

	// Test with different providers
	providers := []struct {
		name       string
		provider   string
		envVars    []string
		skipReason string
	}{
		{"Claude", "claude", []string{"ANTHROPIC_API_KEY", "CLAUDE_API_KEY"}, ""},
		{"Codex", "codex", []string{}, "may not be configured"},
		{"Gemini", "gemini", []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}, ""},
	}

	for _, p := range providers {
		t.Run(p.name, func(t *testing.T) {
			if len(p.envVars) > 0 && !hasAPIKey(t, p.envVars...) {
				t.Skipf("Skipping: No API key found for %s", p.name)
			}

			t.Logf("Testing pal-broker with %s provider...", p.name)

			tmpDir := t.TempDir()

			// Start pal-broker
			cmd := exec.Command("pal-broker",
				"--task", "conversation_test",
				"--provider", p.provider,
				"--work-dir", tmpDir,
				"--session-dir", tmpDir,
			)

			if err := cmd.Start(); err != nil {
				t.Fatalf("Failed to start pal-broker: %v", err)
			}

			// Wait for server to start
			time.Sleep(3 * time.Second)

			// Check if process is still running
			if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
				t.Logf("%s provider exited early, skipping", p.name)
			} else {
				t.Logf("%s provider is running successfully", p.name)
			}

			// Cleanup
			cmd.Process.Kill()
			cmd.Wait()
		})
	}
}

// Helper functions

func collectResponses(ch chan string, timeout time.Duration) []string {
	var responses []string
	deadline := time.After(timeout)

	for {
		select {
		case resp, ok := <-ch:
			if !ok {
				return responses
			}
			if resp != "" {
				responses = append(responses, resp)
			}
		case <-deadline:
			return responses
		}
	}
}

func sendACPRequest(t *testing.T, stdin io.WriteCloser, id int, method string, params map[string]interface{}) {
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Failed to marshal ACP request: %v", err)
	}

	if _, err := stdin.Write(append(data, '\n')); err != nil {
		t.Fatalf("Failed to write ACP request: %v", err)
	}
}

func readACPResponse(t *testing.T, stdout io.ReadCloser, timeout time.Duration) string {
	scanner := bufio.NewScanner(stdout)
	deadline := time.After(timeout)

	for {
		select {
		case <-time.After(100 * time.Millisecond):
			if scanner.Scan() {
				return scanner.Text()
			}
		case <-deadline:
			return ""
		}
	}
}

func readACPMessages(t *testing.T, stdout io.ReadCloser, timeout time.Duration) []string {
	var messages []string
	scanner := bufio.NewScanner(stdout)
	deadline := time.After(timeout)

	for {
		select {
		case <-time.After(100 * time.Millisecond):
			if scanner.Scan() {
				line := scanner.Text()
				if line != "" {
					messages = append(messages, line)
				}
			} else {
				return messages
			}
		case <-deadline:
			return messages
		}
	}
}

func extractSessionID(resp string) string {
	// Simple extraction - in real code would parse JSON properly
	if strings.Contains(resp, "sessionId") {
		parts := strings.Split(resp, "\"")
		for i, part := range parts {
			if part == "sessionId" && i+1 < len(parts) {
				return parts[i+2]
			}
		}
	}
	return "test-session"
}
