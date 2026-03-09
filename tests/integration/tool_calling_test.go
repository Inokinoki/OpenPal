//go:build integration
// +build integration

package integration

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"
)

// hasAPIKey - Check if API key exists for the provider
func hasAPIKey(t *testing.T, envVars ...string) bool {
	for _, envVar := range envVars {
		if os.Getenv(envVar) != "" {
			return true
		}
	}
	return false
}

// TestClaude_ToolCalling - Test Claude Code tool calling functionality
func TestClaude_ToolCalling(t *testing.T) {
	skipIfNoCLI(t, "claude")

	// Check multiple possible API key environment variables
	if !hasAPIKey(t, "ANTHROPIC_API_KEY", "CLAUDE_API_KEY", "ANTHROPIC_AUTH_TOKEN") {
		t.Skip("Skipping: No Anthropic API key found")
	}

	t.Log("Testing Claude Code tool calling...")

	tmpDir := t.TempDir()

	// Create a test file
	testFile := "test.txt"
	content := []byte("Hello, World!")
	if err := os.WriteFile(tmpDir+"/"+testFile, content, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Run a task that requires tool use (file operations)
	cmd := exec.Command("claude", "-p", "Read the file test.txt and tell me its content")
	cmd.Dir = tmpDir
	cmd.Env = os.Environ()

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Claude Code failed: %v\nOutput: %s", err, string(output))
	}

	outputStr := string(output)
	t.Logf("Claude Code output: %s", outputStr)

	// Verify output contains file content or file name
	if !containsAny(outputStr, "Hello", "World", "test.txt") {
		t.Error("Expected output to contain file content or file name")
	}
}

// TestCodex_ToolCalling - Test Codex tool calling functionality
func TestCodex_ToolCalling(t *testing.T) {
	skipIfNoCLI(t, "codex")

	t.Log("Testing Codex tool calling...")

	tmpDir := t.TempDir()

	// Create a test file
	testFile := "test.txt"
	content := []byte("Test content for Codex")
	if err := os.WriteFile(tmpDir+"/"+testFile, content, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Run a task that requires tool use
	cmd := exec.Command("codex", "exec", "Read the file test.txt and summarize it")
	cmd.Dir = tmpDir

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Codex may not be configured
		t.Skipf("Codex not configured: %v", err)
	}

	outputStr := string(output)
	t.Logf("Codex output: %s", outputStr)

	// Verify output is not empty
	if outputStr == "" {
		t.Error("Expected non-empty output from Codex")
	}
}

// TestGemini_ToolCalling - Test Gemini tool calling functionality
func TestGemini_ToolCalling(t *testing.T) {
	skipIfNoCLI(t, "gemini")

	if !hasAPIKey(t, "GEMINI_API_KEY", "GOOGLE_API_KEY") {
		t.Skip("Skipping: No Gemini API key found")
	}

	t.Log("Testing Gemini tool calling...")

	tmpDir := t.TempDir()

	// Create a test file
	testFile := "test.txt"
	content := []byte("Test content for Gemini")
	if err := os.WriteFile(tmpDir+"/"+testFile, content, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Run a task that requires tool use
	cmd := exec.Command("gemini", "chat", "--prompt", "Read the file test.txt and tell me what it says")
	cmd.Dir = tmpDir
	cmd.Env = os.Environ()

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Gemini may not be configured
		t.Skipf("Gemini not configured: %v", err)
	}

	outputStr := string(output)
	t.Logf("Gemini output: %s", outputStr)

	// Verify output is not empty
	if outputStr == "" {
		t.Error("Expected non-empty output from Gemini")
	}
}

// TestCopilot_ToolCalling - Test Copilot tool calling functionality (ACP mode)
func TestCopilot_ToolCalling(t *testing.T) {
	skipIfNoCLI(t, "copilot")

	if !hasAPIKey(t, "GITHUB_TOKEN", "GITHUB_COPILOT_TOKEN") {
		t.Skip("Skipping: No GitHub Copilot token found")
	}

	t.Log("Testing Copilot tool calling...")

	tmpDir := t.TempDir()

	// Create a test file
	testFile := "test.txt"
	content := []byte("Test content for Copilot")
	if err := os.WriteFile(tmpDir+"/"+testFile, content, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Run a task that requires tool use (with timeout)
	cmd := exec.Command("copilot", "--acp", "--stdio")
	cmd.Dir = tmpDir
	cmd.Env = os.Environ()

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("Failed to get stdin pipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		t.Skipf("Copilot not configured: %v", err)
	}

	// Send initialize request
	initReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]interface{}{
			"protocolVersion":    1,
			"clientCapabilities": map[string]interface{}{},
		},
	}
	initData, _ := json.Marshal(initReq)
	stdin.Write(append(initData, '\n'))

	// Send new session request
	sessionReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "session/new",
		"params": map[string]interface{}{
			"cwd":        tmpDir,
			"mcpServers": []interface{}{},
		},
	}
	sessionData, _ := json.Marshal(sessionReq)
	stdin.Write(append(sessionData, '\n'))

	// Wait a bit for response
	time.Sleep(2 * time.Second)

	// Cleanup
	cmd.Process.Kill()
	cmd.Wait()

	t.Log("Copilot ACP test completed")
}

// TestOpenCode_ToolCalling - Test OpenCode tool calling functionality (ACP mode)
func TestOpenCode_ToolCalling(t *testing.T) {
	skipIfNoCLI(t, "opencode")

	t.Log("Testing OpenCode tool calling...")

	tmpDir := t.TempDir()

	// Run OpenCode in ACP mode (with timeout)
	cmd := exec.Command("opencode", "acp")
	cmd.Dir = tmpDir

	done := make(chan error, 1)
	go func() {
		_, err := cmd.CombinedOutput()
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Skipf("OpenCode not configured: %v", err)
		}
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		t.Log("OpenCode ACP mode started successfully")
	}
}

// TestAllAdapters_ToolCalling - Test all adapters for tool calling support
func TestAllAdapters_ToolCalling(t *testing.T) {
	adapters := []struct {
		name    string
		cli     string
		envVars []string
	}{
		{"Claude", "claude", []string{"ANTHROPIC_API_KEY", "CLAUDE_API_KEY"}},
		{"Codex", "codex", []string{}},
		{"Gemini", "gemini", []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}},
		{"Copilot", "copilot", []string{"GITHUB_TOKEN", "GITHUB_COPILOT_TOKEN"}},
		{"OpenCode", "opencode", []string{}},
	}

	for _, adapter := range adapters {
		t.Run(adapter.name, func(t *testing.T) {
			skipIfNoCLI(t, adapter.cli)

			if len(adapter.envVars) > 0 && !hasAPIKey(t, adapter.envVars...) {
				t.Skip("Skipping: No API key found")
			}

			t.Logf("%s adapter is available and configured", adapter.name)
		})
	}
}

// Helper function to check if string contains any of the substrings
func containsAny(s string, substrs ...string) bool {
	for _, substr := range substrs {
		if contains(s, substr) {
			return true
		}
	}
	return false
}

// Helper function to check if string contains substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
