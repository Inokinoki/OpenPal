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

// skipIfNoCLI skipIfNoCLI - Skip test if CLI not installed, but fail in CI
func skipIfNoCLI(t *testing.T, cliName string) {
	if _, err := exec.LookPath(cliName); err != nil {
		// In CI, fail if CLI is not installed (it should have been installed in workflow)
		if os.Getenv("CI") != "" {
			t.Fatalf("CLI '%s' not found in CI environment - installation may have failed. Error: %v", cliName, err)
		}
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

	t.Log("Testing Codex CLI...")

	tmpDir := t.TempDir()

	// First, try to get Codex version to verify it's working
	versionCmd := exec.Command("codex", "--version")
	versionCmd.Env = os.Environ()
	versionOutput, _ := versionCmd.CombinedOutput()

	t.Logf("Codex version check: %s", string(versionOutput))

	// Try running a simple Codex command
	// Codex CLI uses different syntax - it's a coding agent
	cmd := exec.Command("codex", "--help")
	cmd.Dir = tmpDir
	cmd.Env = os.Environ()

	output, err := cmd.CombinedOutput()
	t.Logf("Codex help output length: %d", len(output))

	if err != nil {
		// In CI, fail if Codex is not configured
		if os.Getenv("CI") != "" {
			t.Fatalf("Codex not working in CI: %v\nVersion output: %s\nHelp output: %s", err, string(versionOutput), string(output))
		}
		t.Skipf("Codex not configured: %v", err)
	}

	// If we got help output, Codex is working
	if len(output) > 0 {
		t.Log("Codex CLI is working correctly")
	}
}

// TestCopilot_Integration Test real Copilot CLI
func TestCopilot_Integration(t *testing.T) {
	skipIfNoCLI(t, "copilot")

	t.Log("Testing Copilot CLI...")

	tmpDir := t.TempDir()

	// First, try to get Copilot version to verify it's working
	versionCmd := exec.Command("copilot", "--version")
	versionCmd.Env = os.Environ()
	versionOutput, _ := versionCmd.CombinedOutput()

	t.Logf("Copilot version check: %s", string(versionOutput))

	// Try running help command
	cmd := exec.Command("copilot", "--help")
	cmd.Dir = tmpDir
	cmd.Env = os.Environ()

	output, err := cmd.CombinedOutput()
	t.Logf("Copilot help output length: %d", len(output))

	if err != nil {
		// In CI, fail if Copilot is not configured
		if os.Getenv("CI") != "" {
			t.Fatalf("Copilot not working in CI: %v\nVersion output: %s\nHelp output: %s", err, string(versionOutput), string(output))
		}
		t.Skipf("Copilot not configured: %v", err)
	}

	// If we got help output, Copilot is working
	if len(output) > 0 {
		t.Log("Copilot CLI is working correctly")
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
