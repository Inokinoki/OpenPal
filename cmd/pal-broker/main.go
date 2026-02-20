package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"pal-broker/internal/adapter"
	"pal-broker/internal/server"
	"pal-broker/internal/state"
)

var (
	taskID       = flag.String("task", "", "task ID (alias: --quest-id)")
	questID      = flag.String("quest-id", "", "quest ID (alias: --task)")
	provider     = flag.String("provider", "", "AI provider (claude, codex, copilot)")
	cliPath      = flag.String("cli-path", "", "path to AI CLI executable")
	workDir      = flag.String("work-dir", "", "working directory for AI CLI")
	sessionDir   = flag.String("session-dir", "/tmp/pal-broker", "session directory")
	portFlag     = flag.String("port", ":0", "WebSocket port (default: random)")
	capabilities = flag.String("capabilities", "", "comma-separated list of capabilities")
	supportsACP  = flag.Bool("supports-acp", false, "CLI supports ACP protocol")
	supportsJSON = flag.Bool("supports-json", false, "CLI supports JSON stream output")
	envFile      = flag.String("env-file", ".env", "path to .env file")
)

func main() {
	flag.Parse()

	// Load environment variables from .env file
	if *envFile != "" {
		loadEnvFile(*envFile)
	}

	// Support both --task and --quest-id flags
	id := *taskID
	if id == "" {
		id = *questID
	}

	if id == "" {
		log.Fatal("task/quest ID required (use --task or --quest-id)")
	}

	// Use provider from flag or environment
	prov := *provider
	if prov == "" {
		prov = os.Getenv("PAL_PROVIDER")
	}
	if prov == "" {
		prov = "claude" // Default provider
	}

	// Auto-detect provider settings if not specified
	autoDetectProvider(prov)

	// Override with command-line flags
	if *cliPath != "" {
		// CLI path from flag
	} else if path := os.Getenv("PAL_" + strings.ToUpper(prov) + "_PATH"); path != "" {
		cliPath = &path
	}

	if *workDir == "" {
		if envWorkDir := os.Getenv("PAL_WORK_DIR"); envWorkDir != "" {
			*workDir = envWorkDir
		} else {
			*workDir = "."
		}
	}

	if *capabilities == "" {
		if caps := os.Getenv("PAL_CAPABILITIES"); caps != "" {
			capabilities = &caps
		}
	}

	// Create session directory
	dir := filepath.Join(*sessionDir, id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Fatalf("Failed to create session directory %s: %v", dir, err)
	}

	// Log configuration
	log.Printf("Starting pal-broker for quest %s", id)
	log.Printf("Provider: %s", prov)
	if *cliPath != "" {
		log.Printf("CLI Path: %s", *cliPath)
	}
	log.Printf("Work Dir: %s", *workDir)
	if *capabilities != "" {
		log.Printf("Capabilities: %s", *capabilities)
	}
	log.Printf("Supports ACP: %v", *supportsACP)
	log.Printf("Supports JSON: %v", *supportsJSON)

	// Initialize state manager
	stateMgr := state.NewManager(*sessionDir)
	if err := stateMgr.CreateTask(id, prov); err != nil {
		log.Fatalf("Failed to create task state: %v", err)
	}

	// Initialize status manager
	statusMgr := state.NewStatusManager(*sessionDir)
	if err := statusMgr.Initialize(id, prov, os.Getpid()); err != nil {
		log.Printf("Warning: failed to initialize status: %v", err)
	}

	// Save pal-broker PID
	pidFile := filepath.Join(dir, "bridge.pid")
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", os.Getpid())), 0644); err != nil {
		log.Printf("Warning: failed to write PID file: %v", err)
	}

	// Initialize CLI adapter
	cliAdapter := adapter.NewAdapter(prov, *workDir)

	// Set CLI path if specified
	if *cliPath != "" {
		cliAdapter.SetCLIPath(*cliPath)
	}

	// Set capabilities if specified
	if *capabilities != "" {
		caps := strings.Split(*capabilities, ",")
		cliAdapter.SetCapabilities(caps)
	}

	// Set feature flags
	if *supportsACP {
		cliAdapter.EnableACP()
	}
	if *supportsJSON {
		cliAdapter.EnableJSONStream()
	}

	// Start AI CLI
	cli, err := cliAdapter.Start()
	if err != nil {
		log.Fatalf("Failed to start AI CLI: %v", err)
	}

	log.Printf("AI CLI started (PID: %d)", cli.Pid)

	// Save AI CLI PID
	cliPidFile := filepath.Join(dir, "cli.pid")
	if err := os.WriteFile(cliPidFile, []byte(fmt.Sprintf("%d", cli.Pid)), 0644); err != nil {
		log.Printf("Warning: failed to write CLI PID file: %v", err)
	}

	// Update status with CLI PID
	statusMgr.UpdateAgentStatus("running", cli.Pid, 0)

	// Start WebSocket server
	wsServer := server.NewWebSocketServer(stateMgr, id, cli)
	port, err := wsServer.Start(*portFlag)
	if err != nil {
		log.Fatalf("Failed to start WebSocket server on %s: %v", *portFlag, err)
	}

	log.Printf("WebSocket server listening on port %d", port)

	// Save WebSocket port to file
	portFile := filepath.Join(dir, "ws_port")
	if err := os.WriteFile(portFile, []byte(fmt.Sprintf("%d", port)), 0644); err != nil {
		log.Fatalf("Failed to write port file %s: %v", portFile, err)
	}
	log.Printf("Port file written to: %s", portFile)

	// Update status with WebSocket port
	statusMgr.UpdateAgentStatus("running", cli.Pid, port)

	// Forward CLI output to state manager
	go wsServer.ForwardOutput(cli.Stdout, cli.Stderr)

	// Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigChan
	log.Printf("Received signal %v, shutting down...", sig)

	// Stop AI CLI
	if err := cli.Stop(); err != nil {
		log.Printf("Warning: failed to stop AI CLI: %v", err)
	}

	// Update status
	statusMgr.SetStopped()
	stateMgr.UpdateStatus(id, "stopped")

	log.Println("Cleanup completed, exiting")
	os.Exit(0)
}

// loadEnvFile - Load environment variables from .env file
func loadEnvFile(path string) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			// .env file not found, that's OK
			return
		}
		log.Printf("Warning: failed to open .env file: %v", err)
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		
		// Skip comments and empty lines
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse KEY=VALUE
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		// Remove quotes if present
		value = strings.Trim(value, "'\"")

		// Set environment variable if not already set
		if os.Getenv(key) == "" {
			os.Setenv(key, value)
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("Warning: failed to read .env file: %v", err)
	}
}

// autoDetectProvider - Auto-detect provider settings
func autoDetectProvider(provider string) {
	// Auto-detect ACP support
	if !*supportsACP {
		switch provider {
		case "copilot", "copilot-acp":
			*supportsACP = true // Copilot supports ACP
		}
	}

	// Auto-detect JSON stream support
	if !*supportsJSON {
		switch provider {
		case "claude":
			*supportsJSON = true // Claude supports JSON stream
		}
	}
}

// stringPtr - Helper to create string pointer
func stringPtr(s string) *string {
	return &s
}
