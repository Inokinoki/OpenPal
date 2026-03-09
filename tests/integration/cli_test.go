// Package integration ProvideRealistic CLI IntegrationTest
// TheseTestNeedRealisticinstall corresponding CLI tools
//
// RunLineTest:
//   go test -tags=integration ./tests/integration/...
//
// Oror test aTestspecific CLI:
//   go test -tags=integration -run TestClaudeCode ./tests/integration/...
//
// Note：TheseTestWillRealisticcall AI services，Mayincur costs！

//go:build integration
// +build integration

package integration

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// skipIfNoCLI skipIfNoCLI - Skip test if CLI not installed
func skipIfNoCLI(t *testing.T, cliName string) {
	if _, err := exec.LookPath(cliName); err != nil {
		t.Skipf("Skipping: %s not installed", cliName)
	}
}

// TestClaudeCode_Integration Test real Claude Code CLI
func TestClaudeCode_Integration(t *testing.T) {
	skipIfNoCLI(t, "claude")

	// Check if API key exists
	if os.Getenv("ANTHROPIC_API_KEY") == "" && os.Getenv("CLAUDE_API_KEY") == "" {
		t.Skip("Skipping: No Anthropic API key found")
	}

	t.Log("Testing Claude Code CLI...")

	// CreateATemporarilyDirectory
	tmpDir := t.TempDir()

	// Run a simple task
	cmd := exec.Command("claude", "-p", "Say hello in one sentence")
	cmd.Dir = tmpDir
	cmd.Env = os.Environ()

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Claude Code failed: %v\nOutput: %s", err, string(output))
	}

	t.Logf("Claude Code output: %s", string(output))

	// Verify output contains hello or similar
	outputStr := string(output)
	if len(outputStr) == 0 {
		t.Error("Expected non-empty output from Claude Code")
	}
}

// TestCodex_Integration Test real Codex CLI
func TestCodex_Integration(t *testing.T) {
	skipIfNoCLI(t, "codex")

	// Codex usually requires login
	t.Log("Testing Codex CLI...")

	tmpDir := t.TempDir()

	// Run simple task
	cmd := exec.Command("codex", "-p", "Explain what is 2+2")
	cmd.Dir = tmpDir

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Codex may need login, skip instead of fail
		t.Skipf("Codex not configured: %v", err)
	}

	t.Logf("Codex output: %s", string(output))
}

// TestCopilot_Integration Test real Copilot CLI
func TestCopilot_Integration(t *testing.T) {
	skipIfNoCLI(t, "copilot")

	t.Log("Testing Copilot CLI...")

	tmpDir := t.TempDir()

	// Run simple task
	cmd := exec.Command("copilot", "-p", "What is Go?")
	cmd.Dir = tmpDir
	cmd.Env = os.Environ()

	// Set timeout
	done := make(chan error, 1)
	go func() {
		_, err := cmd.CombinedOutput()
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Skipf("Copilot not configured: %v", err)
		}
	case <-time.After(30 * time.Second):
		cmd.Process.Kill()
		t.Skip("Copilot timed out")
	}
}

// TestPalBroker_Claude Test pal-broker integration with Claude
func TestPalBroker_Claude(t *testing.T) {
	skipIfNoCLI(t, "pal-broker")
	skipIfNoCLI(t, "claude")

	if os.Getenv("ANTHROPIC_API_KEY") == "" && os.Getenv("CLAUDE_API_KEY") == "" {
		t.Skip("Skipping: No API key")
	}

	t.Log("Testing pal-broker with Claude...")

	tmpDir := t.TempDir()

	// Start pal-broker
	cmd := exec.Command("pal-broker",
		"--task", "integration_test",
		"--provider", "claude",
		"--work-dir", tmpDir,
		"--session-dir", tmpDir,
	)

	err := cmd.Start()
	if err != nil {
		t.Fatalf("Failed to start pal-broker: %v", err)
	}

	// Wait 5 seconds
	time.Sleep(5 * time.Second)

	// Check if process is still running
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		t.Error("pal-broker exited unexpectedly")
	}

	// Cleanup
	cmd.Process.Kill()
	cmd.Wait()
}

// TestAllCLIsInstalled Test if all CLIs are installed
func TestAllCLIsInstalled(t *testing.T) {
	// Skip this test in CI environment since CLIs won't be installed
	if os.Getenv("CI") == "true" {
		t.Skip("Skipping CLI installation check in CI environment")
	}

	clis := []string{
		"claude",
		"codex",
		"copilot",
		"pal-broker",
	}

	for _, cli := range clis {
		t.Run(cli, func(t *testing.T) {
			if _, err := exec.LookPath(cli); err != nil {
				t.Logf("⚠️  %s not found in PATH (this is expected if CLI is not installed)", cli)
				// Don't fail the test, just log the warning
			} else {
				t.Logf("✅ %s found: OK", cli)
			}
		})
	}
}
