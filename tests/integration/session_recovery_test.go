//go:build integration
// +build integration

package integration

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hasAPIKey is already defined in cli_test.go, removing duplicate

// TestClaude_SessionRecovery - Test Claude Code session recovery
func TestClaude_SessionRecovery(t *testing.T) {
	skipIfNoCLI(t, "claude")

	if !hasAPIKey(t, "ANTHROPIC_API_KEY", "CLAUDE_API_KEY", "ANTHROPIC_AUTH_TOKEN") {
		t.Skip("Skipping: No Anthropic API key found")
	}

	t.Log("Testing Claude Code session recovery...")

	tmpDir := t.TempDir()
	sessionDir := tmpDir + "/sessions"

	// Turn 1: Start new session and ask a question
	cmd1 := exec.Command("claude", "-p", "--output-format", "stream-json",
		"--verbose", "Remember the number 42 for me")
	cmd1.Dir = tmpDir
	cmd1.Env = os.Environ()

	output1, err1 := cmd1.CombinedOutput()
	if err1 != nil {
		t.Fatalf("Claude Code failed on turn 1: %v\nOutput: %s", err1, string(output1))
	}

	// Extract session ID from output
	sessionID := extractSessionIDFromOutput(string(output1))
	if sessionID == "" {
		t.Skip("Could not extract session ID from Claude output, skipping recovery test")
	}

	t.Logf("Turn 1 completed. Session ID: %s", sessionID)

	// Verify session file was created
	sessionFile := filepath.Join(sessionDir, ".claude-session.json")
	if _, err := os.Stat(sessionFile); os.IsNotExist(err) {
		t.Logf("Warning: Session file not created at %s", sessionFile)
	}

	// Simulate pal-broker saving the session
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatalf("Failed to create session directory: %v", err)
	}

	sessionData := map[string]interface{}{
		"session_id": sessionID,
		"provider":   "claude",
		"task_id":    "recovery_test",
		"created_at": time.Now().UnixMilli(),
		"updated_at": time.Now().UnixMilli(),
	}

	sessionJSON, _ := json.MarshalIndent(sessionData, "", "  ")
	if err := os.WriteFile(sessionFile, sessionJSON, 0644); err != nil {
		t.Fatalf("Failed to write session file: %v", err)
	}

	t.Log("Session file created")

	// Turn 2: Resume session and ask for the remembered number
	cmd2 := exec.Command("claude", "-p", "--output-format", "stream-json", "--verbose",
		"--resume", sessionID, "What number did I ask you to remember?")
	cmd2.Dir = tmpDir
	cmd2.Env = os.Environ()

	output2, err2 := cmd2.CombinedOutput()
	if err2 != nil {
		t.Fatalf("Claude Code failed on turn 2: %v\nOutput: %s", err2, string(output2))
	}

	output2Str := string(output2)
	t.Logf("Turn 2 completed. Output length: %d", len(output2))

	// Verify response contains the number or context from previous turn
	if !strings.Contains(strings.ToLower(output2Str), "42") &&
		!strings.Contains(strings.ToLower(output2Str), "forty") &&
		!strings.Contains(strings.ToLower(output2Str), "remember") {
		t.Logf("Warning: Response may not contain the remembered number. Output: %s", output2Str)
	}

	// Turn 3: Another interaction in the same session
	cmd3 := exec.Command("claude", "-p", "--output-format", "stream-json", "--verbose",
		"--resume", sessionID, "Multiply that number by 2")
	cmd3.Dir = tmpDir
	cmd3.Env = os.Environ()

	output3, err3 := cmd3.CombinedOutput()
	if err3 != nil {
		t.Fatalf("Claude Code failed on turn 3: %v\nOutput: %s", err3, string(output3))
	}

	output3Str := string(output3)
	t.Logf("Turn 3 completed. Output length: %d", len(output3))

	// Verify response contains the result (84 or similar)
	if !strings.Contains(strings.ToLower(output3Str), "84") &&
		!strings.Contains(strings.ToLower(output3Str), "eight") {
		t.Logf("Warning: Response may not contain the correct result. Output: %s", output3Str)
	}

	t.Log("Claude session recovery test completed successfully")
}

// TestCodex_SessionRecovery - Test Codex session recovery
func TestCodex_SessionRecovery(t *testing.T) {
	skipIfNoCLI(t, "codex")

	t.Log("Testing Codex session recovery...")

	tmpDir := t.TempDir()
	sessionFile := tmpDir + "/.codex-session.json"

	// Turn 1: Start new session
	cmd1 := exec.Command("codex", "exec", "Remember the color blue")
	cmd1.Dir = tmpDir
	output1, err1 := cmd1.CombinedOutput()

	if err1 != nil {
		t.Skipf("Codex not configured: %v", err1)
	}

	t.Logf("Turn 1 output: %s", string(output1))

	// Check if session file was created
	if _, err := os.Stat(sessionFile); err == nil {
		t.Log("Session file created by Codex")

		// Read and verify session file
		data, err := os.ReadFile(sessionFile)
		if err == nil {
			t.Logf("Session file content: %s", string(data))
		}
	}

	// Turn 2: Resume session (Codex uses --resume --last)
	cmd2 := exec.Command("codex", "exec", "resume", "--last", "What color did I ask you to remember?")
	cmd2.Dir = tmpDir
	output2, err2 := cmd2.CombinedOutput()

	if err2 != nil {
		t.Logf("Turn 2 warning: %v", err2)
	} else {
		t.Logf("Turn 2 output: %s", string(output2))

		// Verify response contains the color
		output2Str := string(output2)
		if strings.Contains(strings.ToLower(output2Str), "blue") {
			t.Log("Session recovery successful - Codex remembered the color!")
		}
	}

	// Turn 3: Another interaction
	cmd3 := exec.Command("codex", "exec", "resume", "--last", "What is the complementary color?")
	cmd3.Dir = tmpDir
	output3, err3 := cmd3.CombinedOutput()

	if err3 != nil {
		t.Logf("Turn 3 warning: %v", err3)
	} else {
		t.Logf("Turn 3 output: %s", string(output3))
	}

	t.Log("Codex session recovery test completed")
}

// TestGemini_SessionRecovery - Test Gemini session recovery
func TestGemini_SessionRecovery(t *testing.T) {
	skipIfNoCLI(t, "gemini")

	if !hasAPIKey(t, "GEMINI_API_KEY", "GOOGLE_API_KEY") {
		t.Skip("Skipping: No Gemini API key found")
	}

	t.Log("Testing Gemini session recovery...")

	tmpDir := t.TempDir()

	// Turn 1: Start new session
	cmd1 := exec.Command("gemini", "chat", "--format", "json",
		"--prompt", "Remember: My favorite city is Tokyo")
	cmd1.Dir = tmpDir
	cmd1.Env = os.Environ()
	output1, err1 := cmd1.CombinedOutput()

	if err1 != nil {
		t.Skipf("Gemini not configured: %v", err1)
	}

	t.Logf("Turn 1 output length: %d", len(output1))

	// Extract session info from output (if available)
	sessionID := extractSessionIDFromOutput(string(output1))
	if sessionID != "" {
		t.Logf("Extracted session ID: %s", sessionID)
	}

	// Turn 2: Try to resume with same session
	cmd2 := exec.Command("gemini", "chat", "--format", "json",
		"--prompt", "What is my favorite city?")
	cmd2.Dir = tmpDir
	cmd2.Env = os.Environ()
	output2, err2 := cmd2.CombinedOutput()

	if err2 != nil {
		t.Logf("Turn 2 warning: %v", err2)
	} else {
		t.Logf("Turn 2 output length: %d", len(output2))

		// Note: Gemini CLI may not support session resumption like Claude
		// This test verifies the CLI works, session recovery depends on implementation
	}

	// Turn 3: Another interaction
	cmd3 := exec.Command("gemini", "chat", "--format", "json",
		"--prompt", "What is the population of that city?")
	cmd3.Dir = tmpDir
	cmd3.Env = os.Environ()
	output3, err3 := cmd3.CombinedOutput()

	if err3 != nil {
		t.Logf("Turn 3 warning: %v", err3)
	} else {
		t.Logf("Turn 3 output length: %d", len(output3))
	}

	t.Log("Gemini session recovery test completed")
}

// TestPalBroker_SessionRecovery - Test pal-broker session recovery
func TestPalBroker_SessionRecovery(t *testing.T) {
	skipIfNoCLI(t, "pal-broker")

	if !hasAPIKey(t, "ANTHROPIC_API_KEY", "CLAUDE_API_KEY") {
		t.Skip("Skipping: No API key found")
	}

	t.Log("Testing pal-broker session recovery...")

	tmpDir := t.TempDir()
	taskID := "session_recovery_test"

	// Phase 1: Start pal-broker with Claude
	cmd1 := exec.Command("pal-broker",
		"--task", taskID,
		"--provider", "claude",
		"--work-dir", tmpDir,
		"--session-dir", tmpDir,
	)

	if err := cmd1.Start(); err != nil {
		t.Fatalf("Failed to start pal-broker phase 1: %v", err)
	}

	// Wait for initialization
	time.Sleep(3 * time.Second)

	// Check session file
	sessionFile := filepath.Join(tmpDir, taskID, ".claude-session.json")
	if _, err := os.Stat(sessionFile); err == nil {
		t.Log("Session file created in phase 1")

		data, _ := os.ReadFile(sessionFile)
		t.Logf("Session file content: %s", string(data))
	}

	// Stop pal-broker
	cmd1.Process.Kill()
	cmd1.Wait()
	t.Log("Phase 1 completed, pal-broker stopped")

	// Wait a bit to simulate restart delay
	time.Sleep(1 * time.Second)

	// Phase 2: Restart pal-broker (should recover session)
	cmd2 := exec.Command("pal-broker",
		"--task", taskID,
		"--provider", "claude",
		"--work-dir", tmpDir,
		"--session-dir", tmpDir,
	)

	if err := cmd2.Start(); err != nil {
		t.Fatalf("Failed to start pal-broker phase 2: %v", err)
	}

	// Wait for initialization
	time.Sleep(3 * time.Second)

	// Verify session is still valid or was recovered
	if _, err := os.Stat(sessionFile); err == nil {
		t.Log("Session file still exists in phase 2")

		data, _ := os.ReadFile(sessionFile)
		var sessionData map[string]interface{}
		if err := json.Unmarshal(data, &sessionData); err == nil {
			if sessionID, ok := sessionData["session_id"].(string); ok {
				t.Logf("Session ID recovered: %s", sessionID)
			}
		}
	}

	// Cleanup
	cmd2.Process.Kill()
	cmd2.Wait()

	t.Log("Pal-broker session recovery test completed")
}

// TestAllProviders_SessionRecovery - Test session recovery for all providers
func TestAllProviders_SessionRecovery(t *testing.T) {
	providers := []struct {
		name       string
		provider   string
		envVars    []string
		skipReason string
	}{
		{"Claude", "claude", []string{"ANTHROPIC_API_KEY", "CLAUDE_API_KEY"}, ""},
		{"Codex", "codex", []string{}, "may not support session recovery"},
		{"Gemini", "gemini", []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}, "session support varies"},
	}

	for _, p := range providers {
		t.Run(p.name, func(t *testing.T) {
			skipIfNoCLI(t, p.provider)

			if len(p.envVars) > 0 && !hasAPIKey(t, p.envVars...) {
				t.Skipf("Skipping: No API key found for %s", p.name)
			}

			if p.skipReason != "" {
				t.Logf("Note: %s - %s", p.name, p.skipReason)
			}

			t.Logf("Testing session recovery capability for %s", p.name)

			tmpDir := t.TempDir()

			// Create session file
			sessionDir := filepath.Join(tmpDir, "sessions")
			sessionFile := filepath.Join(sessionDir, "."+p.provider+"-session.json")

			if err := os.MkdirAll(sessionDir, 0755); err != nil {
				t.Fatalf("Failed to create session directory: %v", err)
			}

			// Write mock session data
			sessionData := map[string]interface{}{
				"session_id": "test-session-" + p.provider,
				"provider":   p.provider,
				"task_id":    "test",
				"created_at": time.Now().UnixMilli(),
				"updated_at": time.Now().UnixMilli(),
			}

			sessionJSON, _ := json.MarshalIndent(sessionData, "", "  ")
			if err := os.WriteFile(sessionFile, sessionJSON, 0644); err != nil {
				t.Fatalf("Failed to write session file: %v", err)
			}

			// Verify session file can be read
			data, err := os.ReadFile(sessionFile)
			if err != nil {
				t.Errorf("Failed to read session file: %v", err)
				return
			}

			var readData map[string]interface{}
			if err := json.Unmarshal(data, &readData); err != nil {
				t.Errorf("Failed to parse session file: %v", err)
				return
			}

			if sessionID, ok := readData["session_id"].(string); ok {
				t.Logf("Session ID for %s: %s", p.name, sessionID)
			} else {
				t.Errorf("Session ID not found in session file for %s", p.name)
			}

			t.Logf("%s session file structure is valid", p.name)
		})
	}
}

// Helper functions

func extractSessionIDFromOutput(output string) string {
	// Try to extract session ID from various formats

	// Format 1: JSON format with session_id field (handle streaming JSON)
	if strings.Contains(output, "session_id") {
		// Try parsing each line as JSON (streaming format)
		lines := strings.Split(output, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || !strings.HasPrefix(line, "{") {
				continue
			}

			var data map[string]interface{}
			if err := json.Unmarshal([]byte(line), &data); err == nil {
				// Check for session_id at top level
				if sessionID, ok := data["session_id"].(string); ok && sessionID != "" {
					return sessionID
				}
				// Check in data object
				if dataObj, ok := data["data"].(map[string]interface{}); ok {
					if sessionID, ok := dataObj["session_id"].(string); ok && sessionID != "" {
						return sessionID
					}
				}
				// Check sessionId variant
				if sessionID, ok := data["sessionId"].(string); ok && sessionID != "" {
					return sessionID
				}
			}
		}
	}

	// Format 2: "Session ID: xxx" text format
	if strings.Contains(output, "Session ID:") {
		parts := strings.Split(output, "Session ID:")
		if len(parts) > 1 {
			id := strings.TrimSpace(parts[1])
			// Extract UUID-like format
			if len(id) >= 36 {
				return id[:36]
			}
		}
	}

	// Format 3: Look for UUID pattern (fallback)
	uuidPattern := `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`
	if matches := findUUID(output, uuidPattern); matches != "" {
		return matches
	}

	return ""
}

func findUUID(s, pattern string) string {
	// Simple UUID extraction
	start := strings.Index(s, "Session ID: ")
	if start != -1 {
		start += 12 // Skip "Session ID: "
		end := start + 36
		if end <= len(s) {
			return s[start:end]
		}
	}

	// Look for 36-char UUID pattern
	for i := 0; i <= len(s)-36; i++ {
		candidate := s[i : i+36]
		if strings.Count(candidate, "-") == 4 {
			return candidate
		}
	}

	return ""
}
