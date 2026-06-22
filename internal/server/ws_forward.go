package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"openpal/internal/state"
	"openpal/internal/util"
)

// ForwardOutput Forward CLI output
func (s *WebSocketServer) ForwardOutput(stdout, stderr io.Reader) {
	s.wg.Add(2)

	// Forward stdout
	go func() {
		defer s.wg.Done()
		s.forwardStream(stdout, "chunk")
	}()

	// Forward stderr
	go func() {
		defer s.wg.Done()
		s.forwardStream(stderr, "error")
	}()
}

// eventDataPool - Pool for reusing event data maps (reduces GC pressure)
var eventDataPool = sync.Pool{
	New: func() interface{} {
		return make(map[string]interface{}, 4)
	},
}

// forwardStreamBufPool - Pool for scanner buffers (4KB initial, 1MB max)
// Consolidated: replaces lineStringPool and lineBufPool to reduce memory fragmentation
var forwardStreamBufPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 4096)
		return &buf
	},
}

// forwardStream - Forward CLI output to state manager and broadcast channel
// Optimized: reduced allocations, early exits, minimal debug logging
// Enhanced: fast path for empty lines, reduced time.Now() calls, optimized JSON parsing
// Optimization 2026-02-24 03:23: removed verbose start/exit logs (noise reduction)
// Optimization 2026-02-24 03:44: Added line length check before byte access (prevents panic on malformed input)
// Optimization 2026-02-24 05:44: Removed redundant debug logs in hot path (zero overhead)
// Enhancement 2026-02-24: Extract and save Claude session ID from output
// Purely in-memory: no file persistence, all data cached in state manager
// Enhanced 2026-03-19: Set provider and session ID in state manager for recovery
func (s *WebSocketServer) forwardStream(reader io.Reader, eventType string) {
	if reader == nil {
		return
	}
	scanner := bufio.NewScanner(reader)
	const maxCapacity = 1024 * 1024

	bufPtr := forwardStreamBufPool.Get().(*[]byte)
	defer forwardStreamBufPool.Put(bufPtr)
	scanner.Buffer(*bufPtr, maxCapacity)

	// Fast path: cache frequently accessed fields (reduces struct dereferences)
	stateMgr := s.stateMgr
	broadcastCh := s.broadcastCh
	taskID := s.taskID
	cliAdapter := s.cliAdapter // Snapshot to avoid race during shutdown
	provider := ""
	if cliAdapter != nil {
		provider = cliAdapter.GetProvider()
		// Set provider in state manager for session recovery
		stateMgr.SetProvider(provider)
	}

	// Reusable buffers (allocated once, reused per iteration)
	var event state.Event

	for scanner.Scan() {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		line := scanner.Bytes()

		// Fast path: skip empty lines early (no time.Now() call)
		if len(line) == 0 {
			continue
		}

		now := time.Now()

		// Extract Claude session ID from output (if using Claude)
		if provider == "claude" && eventType == "chunk" {
			type sessionUpdater interface {
				UpdateSessionID(line string)
			}
			if adapter, ok := cliAdapter.GetAdapter().(sessionUpdater); ok {
				adapter.UpdateSessionID(string(line))
				// Also sync session ID to state manager for recovery
				type sessionGetter interface {
					GetSessionID() string
				}
				if getter, ok := adapter.(sessionGetter); ok {
					if sid := getter.GetSessionID(); sid != "" {
						stateMgr.SetSessionID(sid)
					}
				}
			}
		}

		// Sync ACP session ID to state manager (for Copilot/OpenCode)
		if provider == "copilot" || provider == "opencode" {
			type acpSessionGetter interface {
				GetSessionID() string
			}
			if acpClient, ok := cliAdapter.GetACPClient(); ok {
				if getter, ok := acpClient.(acpSessionGetter); ok {
					if sid := getter.GetSessionID(); sid != "" {
						stateMgr.SetSessionID(sid)
					}
				}
			}
		}

		// Fast path: simple content-only event (no JSON parsing needed for plain text)
		// Check if line looks like plain text (doesn't start with '{')
		// Optimization: check length first to prevent panic on single-byte lines
		if len(line) == 0 || line[0] != '{' {
			event.Type = eventType
			event.Timestamp = now.UnixMilli()
			// Inline map creation for common case (AddOutput clones data, so fresh map is safe)
			event.Data = map[string]interface{}{"content": string(line)}

			if err := stateMgr.AddOutput(taskID, event); err != nil {
				util.DebugLog("forwardStream: add output error: %v", err)
			}

			// Non-blocking broadcast
			select {
			case broadcastCh <- event:
			default:
				// Channel full, event available via state manager
			}
			continue
		}

		// Parse JSON with pooled map (for JSON-formatted output)
		eventData := eventDataPool.Get().(map[string]interface{})
		if err := json.Unmarshal(line, &eventData); err != nil {
			// Parse failed, treat as plain text
			eventData["content"] = string(line)
		}

		// Create and submit event
		event.Type = eventType
		event.Timestamp = now.UnixMilli()
		event.Data = state.CloneEventDataForForward(eventData)
		eventDataPool.Put(eventData)

		if err := stateMgr.AddOutput(taskID, event); err != nil {
			util.DebugLog("forwardStream: add output error: %v", err)
		}

		// Non-blocking broadcast
		select {
		case broadcastCh <- event:
		default:
			// Channel full, event available via state manager
		}
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		s.errorCh <- fmt.Errorf("scanner error (%s): %w", eventType, err)
	}
}

// Uses state.CloneEventDataForForward from manager.go to eliminate code duplication

// linesSlicePool - Pool for reusable line slices in splitLines
// Optimized: reduces allocations in test scenarios that call splitLines frequently
var linesSlicePool = sync.Pool{
	New: func() interface{} {
		slice := make([][]byte, 0, 8)
		return &slice
	},
}

// splitLines - Split byte slice into lines (used by tests)
// Fixed: correctly handles trailing newline (doesn't create extra empty line)
// Optimized: uses sync.Pool for result slice, avoids copy when caller can consume immediately
// Note: Returns pooled slice - caller must NOT modify or hold reference after use
// For test scenarios where copy is needed, use splitLinesCopy()
func splitLines(data []byte) [][]byte {
	if len(data) == 0 {
		return nil
	}

	// Get slice from pool
	linesPtr := linesSlicePool.Get().(*[][]byte)
	*linesPtr = (*linesPtr)[:0] // Reset length

	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			*linesPtr = append(*linesPtr, data[start:i])
			start = i + 1
		}
	}
	// Add last line only if non-empty (trailing newline doesn't create extra line)
	if start < len(data) {
		*linesPtr = append(*linesPtr, data[start:])
	}

	return *linesPtr
}

// splitLinesCopy - Split byte slice into lines with owned copy (for tests that need persistence)
// Uses splitLines internally, then copies result for caller ownership
func splitLinesCopy(data []byte) [][]byte {
	lines := splitLines(data)
	if lines == nil {
		return nil
	}
	// Copy result for caller ownership
	result := make([][]byte, len(lines))
	for i, line := range lines {
		result[i] = append([]byte(nil), line...)
	}
	// Return pooled slice
	linesSlicePool.Put(&lines)
	return result
}
