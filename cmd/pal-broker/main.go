package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"pal-broker/internal/adapter"
	"pal-broker/internal/server"
	"pal-broker/internal/state"
)

// Build metadata (set via ldflags during build)
var (
	Version   = "dev"
	BuildTime = "unknown"
	GitCommit = "unknown"
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
	saveHistory  = flag.Bool("save-history", false, "save CLI interaction history to file")
	envFile      = flag.String("env-file", ".env", "path to .env file")
	showVersion  = flag.Bool("version", false, "show version and build info")
)

func main() {
	flag.Parse()

	// Show version info if requested
	if *showVersion {
		printVersion()
		os.Exit(0)
	}

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

	// Log startup info with structured format
	log.Print(formatStartupLog(id, prov, *workDir, *supportsACP, *supportsJSON))

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
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
		log.Printf("Warning: failed to write PID file: %v", err)
	}

	// Initialize CLI adapter
	cliAdapter := adapter.NewAdapter(prov, *workDir)
	log.Printf("Adapter initialized: provider=%s, mode=%v, supportsACP=%v", 
		prov, cliAdapter.GetMode(), cliAdapter.GetMode() == adapter.ModeACP)

	// Set CLI path if specified
	if *cliPath != "" {
		cliAdapter.SetCLIPath(*cliPath)
		log.Printf("Custom CLI path set: %s", *cliPath)
	}

	// Set capabilities if specified
	if *capabilities != "" {
		caps := strings.Split(*capabilities, ",")
		cliAdapter.SetCapabilities(caps)
		log.Printf("Custom capabilities set: %v", caps)
	}

	// Set feature flags
	if *supportsACP {
		cliAdapter.EnableACP()
		log.Printf("ACP mode enabled via flag")
	}
	if *supportsJSON {
		cliAdapter.EnableJSONStream()
		log.Printf("JSON stream mode enabled via flag")
	}

	// Start WebSocket server (CLI not started yet)
	wsServer := server.NewWebSocketServer(stateMgr, id, cliAdapter, *saveHistory, *sessionDir)
	port, err := wsServer.Start(*portFlag)
	if err != nil {
		log.Fatalf("Failed to start WebSocket server on %s: %v", *portFlag, err)
	}

	log.Printf("WebSocket server listening on port %d", port)

	// Save WebSocket port to file
	portFile := filepath.Join(dir, "ws_port")
	if err := os.WriteFile(portFile, []byte(strconv.Itoa(port)), 0644); err != nil {
		log.Fatalf("Failed to write port file %s: %v", portFile, err)
	}
	log.Printf("Port file written to: %s", portFile)

	// Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigChan
	log.Printf("Received signal %v, shutting down...", sig)

	// Stop WebSocket server (which will stop CLI if running)
	wsServer.Stop()

	// Update status
	statusMgr.SetStopped()

	// Clean up status manager resources (flush pending writes, stop timer)
	statusMgr.Close()

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
		case "opencode":
			*supportsACP = true // OpenCode supports ACP
		}
	}

	// Auto-detect JSON stream support
	if !*supportsJSON {
		switch provider {
		case "claude":
			*supportsJSON = true // Claude supports JSON stream
		case "codex":
			*supportsJSON = true // Codex may support JSON
		}
	}
}

// printVersion - Print version and build information
func printVersion() {
	fmt.Printf("pal-broker version %s\n", Version)
	fmt.Printf("  Build time: %s\n", BuildTime)
	fmt.Printf("  Git commit: %s\n", GitCommit)
	fmt.Printf("  Go version: %s\n", runtime.Version())
	fmt.Printf("  Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
}

// formatStartupLog - Format startup log with structured info
func formatStartupLog(id, provider, workDir string, supportsACP, supportsJSON bool) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🚀 pal-broker v%s starting\n", Version))
	sb.WriteString(fmt.Sprintf("   Quest ID: %s\n", id))
	sb.WriteString(fmt.Sprintf("   Provider: %s\n", provider))
	sb.WriteString(fmt.Sprintf("   Work Dir: %s\n", workDir))
	sb.WriteString(fmt.Sprintf("   ACP Mode: %v\n", supportsACP))
	sb.WriteString(fmt.Sprintf("   JSON Stream: %v\n", supportsJSON))
	sb.WriteString(fmt.Sprintf("   PID: %d\n", os.Getpid()))
	sb.WriteString(fmt.Sprintf("   Time: %s\n", time.Now().Format(time.RFC3339)))
	return sb.String()
}
