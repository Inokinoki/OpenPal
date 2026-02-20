package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AgentStatus - Current status of the AI agent
type AgentStatus struct {
	State       string            `json:"state"`        // running, completed, failed, stopped
	Provider    string            `json:"provider"`     // claude, codex, copilot
	QuestID     string            `json:"quest_id"`     // Quest identifier
	PID         int               `json:"pid"`          // pal-broker PID
	CLIPID      int               `json:"cli_pid"`      // AI CLI PID
	StartTime   int64             `json:"start_time"`   // Unix timestamp (ms)
	UpdateTime  int64             `json:"update_time"`  // Last update time (ms)
	Seq         int64             `json:"seq"`          // Current sequence number
	Capabilities []string         `json:"capabilities"` // Agent capabilities
	WorkDir     string            `json:"work_dir"`     // Working directory
	WebSocketPort int             `json:"ws_port"`      // WebSocket port
}

// TaskProgress - Current task progress
type TaskProgress struct {
	QuestID       string    `json:"quest_id"`
	State         string    `json:"state"`         // pending, running, completed, failed
	Progress      int       `json:"progress"`      // 0-100
	CurrentAction string    `json:"current_action"` // Current action description
	FilesModified []string  `json:"files_modified"` // List of modified files
	LastOutput    string    `json:"last_output"`    // Last output line
	LastOutputTime int64    `json:"last_output_time"`
	StartTime     int64     `json:"start_time"`
	UpdateTime    int64     `json:"update_time"`
}

// StatusManager - Manages status files
type StatusManager struct {
	sessionDir string
	mu         sync.RWMutex
	status     *AgentStatus
	progress   *TaskProgress
}

// NewStatusManager - Create new status manager
func NewStatusManager(sessionDir string) *StatusManager {
	return &StatusManager{
		sessionDir: sessionDir,
		status:     &AgentStatus{},
		progress:   &TaskProgress{},
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

	m.status = &AgentStatus{
		State:      "initializing",
		Provider:   provider,
		QuestID:    questID,
		PID:        pid,
		StartTime:  time.Now().UnixMilli(),
		UpdateTime: time.Now().UnixMilli(),
	}

	m.progress = &TaskProgress{
		QuestID:    questID,
		State:      "pending",
		Progress:   0,
		StartTime:  time.Now().UnixMilli(),
		UpdateTime: time.Now().UnixMilli(),
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

// UpdateProgress - Update task progress
func (m *StatusManager) UpdateProgress(progress int, action string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.progress.Progress = progress
	m.progress.CurrentAction = action
	m.progress.UpdateTime = time.Now().UnixMilli()

	m.saveProgress()
}

// AddFileModified - Record a modified file
func (m *StatusManager) AddFileModified(filePath string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if file already in list
	for _, f := range m.progress.FilesModified {
		if f == filePath {
			return
		}
	}

	m.progress.FilesModified = append(m.progress.FilesModified, filePath)
	m.progress.UpdateTime = time.Now().UnixMilli()

	m.saveProgress()
}

// UpdateLastOutput - Update last output
func (m *StatusManager) UpdateLastOutput(output string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.progress.LastOutput = output
	m.progress.LastOutputTime = time.Now().UnixMilli()
	m.progress.UpdateTime = time.Now().UnixMilli()

	m.saveProgress()
}

// SetCompleted - Mark task as completed
func (m *StatusManager) SetCompleted() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.status.State = "completed"
	m.progress.State = "completed"
	m.progress.Progress = 100
	m.status.UpdateTime = time.Now().UnixMilli()
	m.progress.UpdateTime = time.Now().UnixMilli()

	m.saveStatus()
	m.saveProgress()
}

// SetFailed - Mark task as failed
func (m *StatusManager) SetFailed(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.status.State = "failed"
	m.progress.State = "failed"
	m.progress.LastOutput = reason
	m.status.UpdateTime = time.Now().UnixMilli()
	m.progress.UpdateTime = time.Now().UnixMilli()

	m.saveStatus()
	m.saveProgress()
}

// SetStopped - Mark task as stopped
func (m *StatusManager) SetStopped() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.status.State = "stopped"
	m.progress.State = "stopped"
	m.status.UpdateTime = time.Now().UnixMilli()
	m.progress.UpdateTime = time.Now().UnixMilli()

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

// saveStatus - Save status to file
func (m *StatusManager) saveStatus() error {
	if m.status.QuestID == "" {
		return nil
	}

	dir := filepath.Join(m.sessionDir, m.status.QuestID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	statusFile := filepath.Join(dir, "status.json")
	data, err := json.MarshalIndent(m.status, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(statusFile, data, 0644)
}

// saveProgress - Save progress to file
func (m *StatusManager) saveProgress() error {
	if m.progress.QuestID == "" {
		return nil
	}

	dir := filepath.Join(m.sessionDir, m.progress.QuestID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	progressFile := filepath.Join(dir, "progress.json")
	data, err := json.MarshalIndent(m.progress, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(progressFile, data, 0644)
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
