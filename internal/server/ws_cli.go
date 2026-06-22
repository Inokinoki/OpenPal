package server

import (
	"encoding/json"
	"fmt"
	"sync/atomic"

	"openpal/internal/adapter"
	"openpal/internal/util"
)

// startCLI - Start CLI with the given task content (extracted for reusability)
func (s *WebSocketServer) startCLI(taskContent string) error {
	// Set task in adapter
	s.cliAdapter.SetTask(taskContent)

	// Initialize Claude session manager if using Claude provider
	// This must be called before starting CLI to enable session resume
	if s.cliAdapter != nil && s.cliAdapter.GetProvider() == "claude" {
		// Type assertion to access ClaudeAdapter-specific methods
		type sessionInitializer interface {
			SetSessionDir(sessionDir, taskID string)
		}
		if adapter := s.cliAdapter.GetAdapter(); adapter != nil {
			if initializer, ok := adapter.(sessionInitializer); ok {
				initializer.SetSessionDir(s.sessionDir, s.taskID)
				util.DebugLog("[DEBUG] startCLI: initialized Claude session manager for task %s", s.taskID)
			}
		}
	}

	// Start CLI (for ACP mode, this starts the process and initializes)
	cli, err := s.cliAdapter.Start()
	if err != nil {
		return fmt.Errorf("failed to start CLI: %w", err)
	}

	s.cli = cli

	// For ACP mode, create session after starting CLI
	if err := s.cliAdapter.CreateSession(s.sessionDir); err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}

	// Send initial prompt
	if s.cliAdapter.GetMode() == adapter.ModeACP {
		// ACP mode: send prompt using ACP protocol
		if err := s.cliAdapter.SendACPPrompt(taskContent); err != nil {
			return fmt.Errorf("failed to send prompt: %w", err)
		}
	}

	// Send initial prompt and start forwarding output
	if s.cliAdapter != nil && s.cliAdapter.GetMode() == adapter.ModeACP {
		// ACP mode: use the ACP client's shared bufio.Reader to avoid competing
		// readers on the same PTY file descriptor. The ACP client's reader may
		// have buffered data from the handshake that must not be lost.
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.forwardStream(s.cliAdapter.GetACPReader(), "chunk")
		}()

	} else {
		// Text/Stream mode (including Claude stream-json): send prompt via stdin
		type stdinEncoder interface {
			EncodeStdinMessage(text string) ([]byte, error)
		}
		if enc, ok := s.cliAdapter.GetAdapter().(stdinEncoder); ok {
			data, err := enc.EncodeStdinMessage(taskContent)
			if err != nil {
				return fmt.Errorf("failed to encode prompt: %w", err)
			}
			if _, err := cli.Stdin.Write(append(data, '\n')); err != nil {
				return fmt.Errorf("failed to write prompt to stdin: %w", err)
			}
		} else if s.cliAdapter.GetAdapter() != nil {
			// Generic text mode: write raw text
			if _, err := fmt.Fprintf(cli.Stdin, "%s\n", taskContent); err != nil {
				return fmt.Errorf("failed to write prompt to stdin: %w", err)
			}
		}

		// Forward output (ForwardOutput spawns its own goroutines and returns immediately)
		s.ForwardOutput(cli.Stdout, cli.Stderr)
	}

	return nil
}

// processInputQueue - Process input queue and send to CLI
// All providers now use persistent processes:
// - Claude: stream-json stdin/stdout (like ACP)
// - Copilot/OpenCode: ACP protocol
// - Others: text mode via stdin
func (s *WebSocketServer) processInputQueue() {
	for {
		select {
		case <-s.ctx.Done():
			return

		case inputMsg, ok := <-s.inputQueue:
			if !ok {
				return
			}

			if s.cliAdapter.GetMode() == adapter.ModeACP {
				// ACP mode: send via ACP protocol (persistent connection)
				if err := s.cliAdapter.SendACPPrompt(inputMsg.Content); err != nil {
					s.errorCh <- fmt.Errorf("send input: %w", err)
				}
			} else {
				// Stream/Text mode with persistent CLI: send via stdin
				s.mu.RLock()
				cliAlive := s.cli != nil && s.cli.Stdin != nil
				s.mu.RUnlock()

				if !cliAlive {
					// CLI not running, start it with the input as initial prompt
					if err := s.startCLI(inputMsg.Content); err != nil {
						s.errorCh <- err
					}
				} else {
					s.sendToCLI(inputMsg)
				}
			}
		}
	}
}

// sendToCLI - Send message to CLI stdin
// Optimized: direct allocation eliminates pool overhead for infrequent calls (~1-10/sec)
// Enhanced: uses provider-specific encoding (stream-json for Claude, plain JSON for others)
// Performance: ~40-80ns per message (dominated by JSON marshal and I/O)
func (s *WebSocketServer) sendToCLI(inputMsg InputMessage) {
	// Single mutex-protected check for CLI state
	s.mu.RLock()
	cli := s.cli
	cliAlive := s.cliStarted.Load() && cli != nil && cli.Stdin != nil
	s.mu.RUnlock()

	if !cliAlive {
		return
	}

	var data []byte
	var err error

	// Use provider-specific encoding for Claude (stream-json format)
	type stdinEncoder interface {
		EncodeStdinMessage(text string) ([]byte, error)
	}
	if enc, ok := s.cliAdapter.GetAdapter().(stdinEncoder); ok {
		data, err = enc.EncodeStdinMessage(inputMsg.Content)
	} else {
		// Generic JSON format for other providers
		msg := map[string]interface{}{
			"message": map[string]interface{}{
				"role":    "user",
				"content": inputMsg.Content,
			},
		}
		data, err = json.Marshal(msg)
	}

	if err != nil {
		s.errorCh <- fmt.Errorf("marshal message failed: %w", err)
		return
	}

	// Append newline and write (single allocation for write buffer)
	writeBuf := append(data, '\n')
	n, err := cli.Stdin.Write(writeBuf)

	if err != nil {
		util.DebugLog("[DEBUG] sendToCLI: write failed (wrote=%d/%d): %v", n, len(data), err)
		return
	}

	if n != len(data) {
		util.DebugLog("[DEBUG] sendToCLI: partial write (wrote=%d/%d)", n, len(data))
	}
}

// queueInputWithLogging - Helper to queue input (purely in-memory, no file logging)
// Enhanced: non-blocking queue send with overflow protection, queue depth tracking
// Optimized: single atomic operation for CLI start check (avoids double-check pattern)
func (s *WebSocketServer) queueInputWithLogging(entryType, content string) {
	// Non-blocking send to input queue (prevents deadlock if queue is full)
	inputMsg := InputMessage{
		Content: content,
		Type:    entryType,
	}

	select {
	case s.inputQueue <- inputMsg:
		// Successfully queued - start CLI processor if not already started
		// Optimized: single CompareAndSwap handles both check and set atomically
		if s.cliAdapter != nil && s.cliStarted.CompareAndSwap(false, true) {
			go s.processInputQueue()
		}
	case <-s.ctx.Done():
		// Server shutting down, discard message
		util.DebugLog("[DEBUG] queueInputWithLogging: server shutting down, discarding message")
	default:
		// Queue full - track dropped message and log warning
		atomic.AddInt64(&s.stats.inputDropped, 1)
		util.DebugLog("[DEBUG] queueInputWithLogging: input queue full (depth=%d), dropping message", len(s.inputQueue))
	}
}
