package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// AgentStatus - Current status of the AI agent
type AgentStatus struct {
	State         string   `json:"state"`        // running, completed, failed, stopped
	Provider      string   `json:"provider"`     // claude, codex, copilot
	QuestID       string   `json:"quest_id"`     // Quest identifier
	PID           int      `json:"pid"`          // pal-broker PID
	CLIPID        int      `json:"cli_pid"`      // AI CLI PID
	StartTime     int64    `json:"start_time"`   // Unix timestamp (ms)
	UpdateTime    int64    `json:"update_time"`  // Last update time (ms)
	Seq           int64    `json:"seq"`          // Current sequence number
	Capabilities  []string `json:"capabilities"` // Agent capabilities
	WorkDir       string   `json:"work_dir"`     // Working directory
	WebSocketPort int      `json:"ws_port"`      // WebSocket port
}

// TaskProgress - Current task progress
type TaskProgress struct {
	QuestID        string   `json:"quest_id"`
	State          string   `json:"state"`          // pending, running, completed, failed
	Progress       int      `json:"progress"`       // 0-100
	CurrentAction  string   `json:"current_action"` // Current action description
	FilesModified  []string `json:"files_modified"` // List of modified files
	LastOutput     string   `json:"last_output"`    // Last output line
	LastOutputTime int64    `json:"last_output_time"`
	StartTime      int64    `json:"start_time"`
	UpdateTime     int64    `json:"update_time"`
}

// StatusManager - Manages status files
type StatusManager struct {
	sessionDir    string
	mu            sync.RWMutex
	status        *AgentStatus
	progress      *TaskProgress
	filesModified map[string]struct{} // Map for O(1) duplicate check

	// Write buffering for high-frequency updates
	pendingWrites  int32 // atomic flag for pending writes
	writeTimer     *time.Timer
	writeMu        sync.Mutex
	writeScheduled bool
}

// writeDelay - Delay for batching writes (100ms)
const writeDelay = 100 * time.Millisecond

// NewStatusManager - Create new status manager
func NewStatusManager(sessionDir string) *StatusManager {
	return &StatusManager{
		sessionDir:    sessionDir,
		status:        &AgentStatus{},
		progress:      &TaskProgress{},
		filesModified: make(map[string]struct{}),
		writeTimer:    time.NewTimer(writeDelay),
	}
}

// Initialize - Initialize status files
func (m *StatusManager) Initialize(questID, provider string, pid int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	dir := filepath.Join(m.sessionDir, questID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	now := time.Now().UnixMilli() // Single time.Now() call for all timestamps

	m.status = &AgentStatus{
		State:      "initializing",
		Provider:   provider,
		QuestID:    questID,
		PID:        pid,
		StartTime:  now,
		UpdateTime: now,
	}

	m.progress = &TaskProgress{
		QuestID:    questID,
		State:      "pending",
		Progress:   0,
		StartTime:  now,
		UpdateTime: now,
	}

	return m.saveStatus()
}

// UpdateAgentStatus - Update agent status
func (m *StatusManager) UpdateAgentStatus(state string, cliPID int, wsPort int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.status.State = state
	m.status.CLIPID = cliPID
	m.status.WebSocketPort = wsPort
	m.status.UpdateTime = time.Now().UnixMilli()

	m.saveStatus()
}

// UpdateProgress - Update task progress (uses delayed write for batching)
func (m *StatusManager) UpdateProgress(progress int, action string) {
	m.mu.Lock()
	m.progress.Progress = progress
	m.progress.CurrentAction = action
	m.progress.UpdateTime = time.Now().UnixMilli()
	m.mu.Unlock()

	// Schedule delayed write to batch multiple updates
	m.scheduleWrite()
}

// AddFileModified - Record a modified file (uses delayed write for batching)
func (m *StatusManager) AddFileModified(filePath string) {
	m.mu.Lock()

	// O(1) duplicate check using map
	if _, exists := m.filesModified[filePath]; exists {
		m.mu.Unlock()
		return
	}

	// Add to both map and slice (slice for JSON serialization, map for lookup)
	m.filesModified[filePath] = struct{}{}
	m.progress.FilesModified = append(m.progress.FilesModified, filePath)
	m.progress.UpdateTime = time.Now().UnixMilli()
	m.mu.Unlock()

	// Schedule delayed write to batch multiple updates
	m.scheduleWrite()
}

// UpdateLastOutput - Update last output (uses delayed write for batching)
func (m *StatusManager) UpdateLastOutput(output string) {
	m.mu.Lock()
	m.progress.LastOutput = output
	m.progress.LastOutputTime = time.Now().UnixMilli()
	m.progress.UpdateTime = time.Now().UnixMilli()
	m.mu.Unlock()

	// Schedule delayed write to batch multiple updates
	m.scheduleWrite()
}

// SetCompleted - Mark task as completed
func (m *StatusManager) SetCompleted() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UnixMilli() // Single time.Now() call for both updates
	m.status.State = "completed"
	m.progress.State = "completed"
	m.progress.Progress = 100
	m.status.UpdateTime = now
	m.progress.UpdateTime = now

	m.saveStatus()
	m.saveProgress()
}

// SetFailed - Mark task as failed
func (m *StatusManager) SetFailed(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UnixMilli() // Single time.Now() call for both updates
	m.status.State = "failed"
	m.progress.State = "failed"
	m.progress.LastOutput = reason
	m.status.UpdateTime = now
	m.progress.UpdateTime = now

	m.saveStatus()
	m.saveProgress()
}

// SetStopped - Mark task as stopped
func (m *StatusManager) SetStopped() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UnixMilli() // Single time.Now() call for both updates
	m.status.State = "stopped"
	m.progress.State = "stopped"
	m.status.UpdateTime = now
	m.progress.UpdateTime = now

	m.saveStatus()
	m.saveProgress()
}

// GetStatus - Get current status
func (m *StatusManager) GetStatus() (*AgentStatus, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.status, nil
}

// GetProgress - Get current progress
func (m *StatusManager) GetProgress() (*TaskProgress, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.progress, nil
}

// saveJSON - Helper to save JSON data to file (reduces duplication)
func (m *StatusManager) saveJSON(filename string, data interface{}) error {
	// Extract questID from the data type
	var questID string
	switch v := data.(type) {
	case *AgentStatus:
		questID = v.QuestID
	case *TaskProgress:
		questID = v.QuestID
	default:
		return fmt.Errorf("unsupported data type")
	}

	if questID == "" {
		return nil
	}

	dir := filepath.Join(m.sessionDir, questID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	filePath := filepath.Join(dir, filename)
	// Use json.Marshal instead of MarshalIndent for better performance
	// Status files are primarily machine-read; human readability is secondary
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}

	return os.WriteFile(filePath, jsonData, 0644)
}

// scheduleWrite - Schedule a delayed write to batch multiple updates
// Optimized: reduces I/O by batching writes within writeDelay window
// Uses atomic operations for lock-free scheduling check
func (m *StatusManager) scheduleWrite() {
	// Fast path: check if write already scheduled (atomic, no lock)
	if atomic.LoadInt32(&m.pendingWrites) == 1 {
		return
	}

	// Try to claim the write slot
	if !atomic.CompareAndSwapInt32(&m.pendingWrites, 0, 1) {
		return // Another goroutine claimed it
	}

	m.writeMu.Lock()

	// Safety check: don't schedule if already closed
	if m.writeTimer == nil {
		atomic.StoreInt32(&m.pendingWrites, 0)
		m.writeMu.Unlock()
		return
	}

	// Reset and start timer (inline timer reset to avoid extra goroutine)
	if !m.writeTimer.Stop() {
		select {
		case <-m.writeTimer.C:
		default:
		}
	}
	m.writeTimer.Reset(writeDelay)
	m.writeScheduled = true

	m.writeMu.Unlock()
	// Timer will fire and call flushWrites directly - no extra goroutine needed
}

// flushWrites - Flush pending writes to disk
func (m *StatusManager) flushWrites() {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()

	// Save both status and progress together
	m.saveStatusLocked()
	m.saveProgressLocked()

	atomic.StoreInt32(&m.pendingWrites, 0)
	m.writeScheduled = false
}

// saveStatusLocked - Save status without acquiring main mutex
func (m *StatusManager) saveStatusLocked() error {
	return m.saveJSON("status.json", m.status)
}

// saveProgressLocked - Save progress without acquiring main mutex
func (m *StatusManager) saveProgressLocked() error {
	return m.saveJSON("progress.json", m.progress)
}

// saveStatus - Save status to file (immediate)
func (m *StatusManager) saveStatus() error {
	return m.saveJSON("status.json", m.status)
}

// saveProgress - Save progress to file (immediate)
func (m *StatusManager) saveProgress() error {
	return m.saveJSON("progress.json", m.progress)
}

// Flush - Force flush pending writes immediately
func (m *StatusManager) Flush() {
	if atomic.LoadInt32(&m.pendingWrites) == 1 {
		m.flushWrites()
	}
}

// ReadStatusFromFile - Read status from file (for Tavern)
func ReadStatusFromFile(sessionDir, questID string) (*AgentStatus, error) {
	statusFile := filepath.Join(sessionDir, questID, "status.json")
	data, err := os.ReadFile(statusFile)
	if err != nil {
		return nil, err
	}

	var status AgentStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return nil, err
	}

	return &status, nil
}

// ReadProgressFromFile - Read progress from file (for Tavern)
func ReadProgressFromFile(sessionDir, questID string) (*TaskProgress, error) {
	progressFile := filepath.Join(sessionDir, questID, "progress.json")
	data, err := os.ReadFile(progressFile)
	if err != nil {
		return nil, err
	}

	var progress TaskProgress
	if err := json.Unmarshal(data, &progress); err != nil {
		return nil, err
	}

	return &progress, nil
}

// Close - Clean up resources (stop timer, flush pending writes)
// Call this when the StatusManager is no longer needed to prevent goroutine leaks
func (m *StatusManager) Close() {
	// Flush any pending writes first
	m.Flush()

	// Stop the write timer to prevent goroutine leak
	m.writeMu.Lock()
	if m.writeTimer != nil {
		m.writeTimer.Stop()
		m.writeTimer = nil
	}
	m.writeScheduled = false
	m.writeMu.Unlock()
}
