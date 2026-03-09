package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SessionData - Session data structure
type SessionData struct {
	SessionID string `json:"session_id"`
	Provider  string `json:"provider"`
	TaskID    string `json:"task_id"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// Manager - Session manager for a specific task
type Manager struct {
	sessionDir  string // /tmp/pal-broker/{taskID}/
	provider    string // claude, copilot, codex, etc.
	taskID      string
	sessionFile string // Full path to session file
	mu          sync.RWMutex
	cachedID    string // Cached session ID for fast access
}

// NewManager - Create a new session manager for a task
func NewManager(sessionDir, taskID, provider string) *Manager {
	// Ensure session directory exists
	_ = os.MkdirAll(filepath.Join(sessionDir, taskID), 0755)

	sessionFile := filepath.Join(sessionDir, taskID, fmt.Sprintf(".%s-session.json", provider))

	return &Manager{
		sessionDir:  sessionDir,
		taskID:      taskID,
		provider:    provider,
		sessionFile: sessionFile,
	}
}

// Save - Save session ID to file
func (m *Manager) Save(sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("session ID cannot be empty")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Update cache
	m.cachedID = sessionID

	// Ensure session directory exists
	if err := os.MkdirAll(filepath.Join(m.sessionDir, m.taskID), 0755); err != nil {
		return fmt.Errorf("failed to create session directory: %w", err)
	}

	data := &SessionData{
		SessionID: sessionID,
		Provider:  m.provider,
		TaskID:    m.taskID,
		CreatedAt: time.Now().UnixMilli(),
		UpdatedAt: time.Now().UnixMilli(),
	}

	// If file exists, preserve created_at
	if existing, err := m.loadWithoutLock(); err == nil && existing != nil {
		data.CreatedAt = existing.CreatedAt
	}

	fileData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session data: %w", err)
	}

	if err := os.WriteFile(m.sessionFile, fileData, 0644); err != nil {
		return fmt.Errorf("failed to write session file: %w", err)
	}

	return nil
}

// Load - Load session ID from file
func (m *Manager) Load() (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Fast path: return cached ID
	if m.cachedID != "" {
		return m.cachedID, nil
	}

	data, err := m.loadWithoutLock()
	if err != nil {
		return "", err
	}

	// No session file yet
	if data == nil {
		return "", nil
	}

	// Cache for next time
	m.cachedID = data.SessionID
	return data.SessionID, nil
}

// loadWithoutLock - Internal load without lock (caller must hold lock)
func (m *Manager) loadWithoutLock() (*SessionData, error) {
	fileData, err := os.ReadFile(m.sessionFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No session file yet, that's OK
		}
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}

	var data SessionData
	if err := json.Unmarshal(fileData, &data); err != nil {
		return nil, fmt.Errorf("failed to unmarshal session data: %w", err)
	}

	// Validate provider matches
	if data.Provider != m.provider {
		return nil, fmt.Errorf("provider mismatch: expected %s, got %s", m.provider, data.Provider)
	}

	return &data, nil
}

// Clear - Remove session file (for cleanup or reset)
func (m *Manager) Clear() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.cachedID = ""

	if err := os.Remove(m.sessionFile); err != nil {
		if os.IsNotExist(err) {
			return nil // Already cleared
		}
		return fmt.Errorf("failed to remove session file: %w", err)
	}

	return nil
}

// Exists - Check if session file exists
func (m *Manager) Exists() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	_, err := os.Stat(m.sessionFile)
	return err == nil
}

// GetSessionData - Get full session data (for debugging/inspection)
func (m *Manager) GetSessionData() (*SessionData, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.loadWithoutLock()
}
