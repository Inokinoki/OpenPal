package adapter

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"openpal/internal/util"
)

// SessionReader - Interface for reading session data from CLI agent storage
// Implementations read native session formats from different AI CLI providers
type SessionReader interface {
	// ReadSession reads all events from a session by ID
	// Returns events in chronological order (by sequence/timestamp)
	ReadSession(sessionID string) ([]SessionEvent, error)

	// ListSessions lists available session IDs for this provider
	// Optional: can return all sessions if no filtering is needed
	ListSessions() ([]string, error)

	// GetSessionMetadata returns metadata about a session (optional, can return nil)
	GetSessionMetadata(sessionID string) (*SessionMetadata, error)
}

// SessionEvent - A single event from a CLI agent session
// Compatible with state.Event for easy integration
type SessionEvent struct {
	Seq       int64       `json:"seq"`       // Sequence number (auto-assigned during recovery)
	Type      string      `json:"type"`      // Event type: chunk, assistant, user, system, etc.
	Timestamp int64       `json:"timestamp"` // Unix milliseconds
	Data      interface{} `json:"data"`      // Event payload
	Raw       string      `json:"-"`         // Original raw line (for debugging)
}

// SessionMetadata - Metadata about a session
type SessionMetadata struct {
	SessionID    string    `json:"session_id"`
	Provider     string    `json:"provider"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	MessageCount int       `json:"message_count"`
	Model        string    `json:"model,omitempty"`
	ProjectPath  string    `json:"project_path,omitempty"`
}

// claudeSessionReader - Reads Claude Code session files
// Session format: ~/.claude/projects/<project_hash>/sessions/<session_id>.jsonl
type claudeSessionReader struct {
	claudeDir string // Base Claude directory (default: ~/.claude)
}

// ClaudeSessionReader - Create a new Claude session reader
func ClaudeSessionReader(claudeDir string) SessionReader {
	if claudeDir == "" {
		// Default to home directory
		home, err := os.UserHomeDir()
		if err != nil {
			claudeDir = "/tmp" // Fallback
		} else {
			claudeDir = filepath.Join(home, ".claude")
		}
	}
	return &claudeSessionReader{claudeDir: claudeDir}
}

// claudeSessionEntry - Structure for sessions-index.json entries
type claudeSessionEntry struct {
	SessionID string `json:"sessionId"`
	FullPath  string `json:"fullPath"`
}

// claudeSessionsIndex - Structure for sessions-index.json
type claudeSessionsIndex struct {
	Entries []claudeSessionEntry `json:"entries"`
}

// ReadSession reads a Claude session by ID
// Searches in multiple locations:
// 1. sessions-index.json (Claude's session index)
// 2. Direct file lookup by session ID
// 3. Full scan of all JSONL files (fallback)
func (r *claudeSessionReader) ReadSession(sessionID string) ([]SessionEvent, error) {
	projectsRoot := filepath.Join(r.claudeDir, "projects")

	// Try to find session file
	sessionFile, err := r.findSessionFile(projectsRoot, sessionID)
	if err != nil {
		return nil, fmt.Errorf("find session: %w", err)
	}
	if sessionFile == "" {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}

	return r.parseSessionFile(sessionFile, sessionID)
}

// findSessionFile locates a session file by ID using multiple strategies
func (r *claudeSessionReader) findSessionFile(projectsRoot, sessionID string) (string, error) {
	// Strategy 1: Check sessions-index.json
	if file := r.findFromIndex(projectsRoot, sessionID); file != "" {
		return file, nil
	}

	// Strategy 2: Direct filename match (<session_id>.jsonl)
	if file := r.findByFilename(projectsRoot, sessionID); file != "" {
		return file, nil
	}

	// Strategy 3: Scan first 30 lines of JSONL files for sessionId header
	if file := r.findByHeaderScan(projectsRoot, sessionID); file != "" {
		return file, nil
	}

	return "", nil
}

// findFromIndex searches using Claude's sessions-index.json
func (r *claudeSessionReader) findFromIndex(projectsRoot, sessionID string) string {
	if _, err := os.Stat(projectsRoot); os.IsNotExist(err) {
		return ""
	}

	var matches []string

	// Walk through all projects looking for sessions-index.json
	filepath.Walk(projectsRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if info.Name() != "sessions-index.json" {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		var index claudeSessionsIndex
		if err := json.Unmarshal(data, &index); err != nil {
			return nil
		}

		for _, entry := range index.Entries {
			if entry.SessionID == sessionID && entry.FullPath != "" {
				if _, err := os.Stat(entry.FullPath); err == nil {
					matches = append(matches, entry.FullPath)
				}
			}
		}
		return nil
	})

	if len(matches) == 0 {
		return ""
	}

	// Return most recently modified file (handles duplicates)
	return selectLatestFile(matches)
}

// findByFilename looks for files named <session_id>.jsonl
func (r *claudeSessionReader) findByFilename(projectsRoot, sessionID string) string {
	if _, err := os.Stat(projectsRoot); os.IsNotExist(err) {
		return ""
	}

	var matches []string
	needle := sessionID + ".jsonl"

	filepath.Walk(projectsRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if info.Name() == needle {
			matches = append(matches, path)
		}
		return nil
	})

	if len(matches) == 0 {
		return ""
	}

	return selectLatestFile(matches)
}

// findByHeaderScan scans JSONL files looking for sessionId in the first lines
func (r *claudeSessionReader) findByHeaderScan(projectsRoot, sessionID string) string {
	if _, err := os.Stat(projectsRoot); os.IsNotExist(err) {
		return ""
	}

	var matches []string

	filepath.Walk(projectsRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".jsonl") {
			return nil
		}

		if r.fileContainsSessionID(path, sessionID) {
			matches = append(matches, path)
		}
		return nil
	})

	if len(matches) == 0 {
		return ""
	}

	return selectLatestFile(matches)
}

// fileContainsSessionID checks if a JSONL file contains a session ID in its header
func (r *claudeSessionReader) fileContainsSessionID(path, sessionID string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	// Check first 30 lines (header typically contains sessionId)
	for i := 0; i < 30 && scanner.Scan(); i++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var data map[string]interface{}
		if err := json.Unmarshal([]byte(line), &data); err != nil {
			continue
		}

		if sid, ok := data["sessionId"].(string); ok && sid == sessionID {
			return true
		}
	}

	return false
}

// parseSessionFile reads and parses a Claude session JSONL file
func (r *claudeSessionReader) parseSessionFile(path, sessionID string) ([]SessionEvent, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open session file: %w", err)
	}
	defer file.Close()

	var events []SessionEvent
	scanner := bufio.NewScanner(file)
	seq := int64(0)

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		seq++
		event := r.parseLine(line, seq)
		if event != nil {
			event.Raw = line
			events = append(events, *event)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read session file: %w", err)
	}

	return events, nil
}

// parseLine parses a single JSONL line into a SessionEvent
// Claude session format varies by event type:
// - System events: {"type": "system", ...}
// - Assistant messages: {"type": "assistant", "message": {...}}
// - User messages: {"type": "user", "message": {...}}
// - Tool use: {"type": "tool_use", ...}
// - Chunks: {"type": "chunk", "content": "..."}
func (r *claudeSessionReader) parseLine(line string, seq int64) *SessionEvent {
	data := make(map[string]interface{})
	if err := json.Unmarshal([]byte(line), &data); err != nil {
		return nil // Skip unparseable lines
	}

	eventType, _ := data["type"].(string)
	if eventType == "" {
		// Try to detect type from content
		if _, ok := data["message"]; ok {
			eventType = "assistant"
		} else {
			eventType = "chunk" // Default fallback
		}
	}

	// Extract timestamp (Claude uses various timestamp formats)
	timestamp := extractTimestamp(data)

	return &SessionEvent{
		Seq:       seq,
		Type:      eventType,
		Timestamp: timestamp,
		Data:      data,
	}
}

// extractTimestamp extracts Unix milliseconds from Claude event data
func extractTimestamp(data map[string]interface{}) int64 {
	// Try common timestamp fields
	for _, field := range []string{"timestamp", "created_at", "time"} {
		switch v := data[field].(type) {
		case float64:
			// If it looks like seconds, convert to ms
			if v < 1e12 {
				return int64(v * 1000)
			}
			return int64(v)
		case int64:
			if v < 1e12 {
				return v * 1000
			}
			return v
		case string:
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				return t.UnixMilli()
			}
		}
	}

	// Default to current time if no timestamp found
	return time.Now().UnixMilli()
}

// ListSessions lists all available Claude sessions
func (r *claudeSessionReader) ListSessions() ([]string, error) {
	projectsRoot := filepath.Join(r.claudeDir, "projects")
	if _, err := os.Stat(projectsRoot); os.IsNotExist(err) {
		return []string{}, nil
	}

	sessionSet := make(map[string]bool)

	// Collect from sessions-index.json files
	filepath.Walk(projectsRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if info.Name() != "sessions-index.json" {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		var index claudeSessionsIndex
		if err := json.Unmarshal(data, &index); err != nil {
			return nil
		}

		for _, entry := range index.Entries {
			sessionSet[entry.SessionID] = true
		}
		return nil
	})

	// Also collect from filename pattern
	filepath.Walk(projectsRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(info.Name(), ".jsonl") {
			sessionID := strings.TrimSuffix(info.Name(), ".jsonl")
			sessionSet[sessionID] = true
		}
		return nil
	})

	// Convert set to sorted slice
	sessions := make([]string, 0, len(sessionSet))
	for sid := range sessionSet {
		sessions = append(sessions, sid)
	}
	sort.Strings(sessions)

	return sessions, nil
}

// GetSessionMetadata returns metadata about a Claude session
func (r *claudeSessionReader) GetSessionMetadata(sessionID string) (*SessionMetadata, error) {
	events, err := r.ReadSession(sessionID)
	if err != nil {
		return nil, err
	}

	if len(events) == 0 {
		return nil, fmt.Errorf("empty session")
	}

	// Find first and last timestamps
	var createdAt, updatedAt int64
	var model string

	for _, event := range events {
		if createdAt == 0 || event.Timestamp < createdAt {
			createdAt = event.Timestamp
		}
		if event.Timestamp > updatedAt {
			updatedAt = event.Timestamp
		}

		// Extract model from event data if available
		if data, ok := event.Data.(map[string]interface{}); ok {
			if m, ok := data["model"].(string); ok && model == "" {
				model = m
			}
		}
	}

	return &SessionMetadata{
		SessionID:    sessionID,
		Provider:     "claude",
		CreatedAt:    time.Unix(createdAt/1000, (createdAt%1000)*1000000),
		UpdatedAt:    time.Unix(updatedAt/1000, (updatedAt%1000)*1000000),
		MessageCount: len(events),
		Model:        model,
	}, nil
}

// selectLatestFile returns the most recently modified file from a list
func selectLatestFile(files []string) string {
	if len(files) == 0 {
		return ""
	}
	if len(files) == 1 {
		return files[0]
	}

	type fileScore struct {
		path     string
		modified time.Time
	}

	scoredFiles := make([]fileScore, 0, len(files))
	for _, f := range files {
		if info, err := os.Stat(f); err == nil {
			scoredFiles = append(scoredFiles, fileScore{f, info.ModTime()})
		}
	}

	if len(scoredFiles) == 0 {
		return files[0]
	}

	sort.Slice(scoredFiles, func(i, j int) bool {
		return scoredFiles[i].modified.After(scoredFiles[j].modified)
	})

	return scoredFiles[0].path
}

// codexSessionReader - Reads Codex session files
// Stub implementation - to be fleshed out based on Codex storage format
type codexSessionReader struct {
	codexDir string
}

// CodexSessionReader creates a new Codex session reader
func CodexSessionReader(codexDir string) SessionReader {
	if codexDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			codexDir = "/tmp"
		} else {
			codexDir = filepath.Join(home, ".codex")
		}
	}
	return &codexSessionReader{codexDir: codexDir}
}

func (r *codexSessionReader) ReadSession(sessionID string) ([]SessionEvent, error) {
	// Codex stores sessions in SQLite + JSONL rollout files
	// Try to find the session directory
	sessionPath := filepath.Join(r.codexDir, "sessions", sessionID)
	if _, err := os.Stat(sessionPath); os.IsNotExist(err) {
		// Try alternative path: ~/.codex/sessions/<sessionID>
		sessionPath = filepath.Join(r.codexDir, sessionID)
		if _, err := os.Stat(sessionPath); os.IsNotExist(err) {
			return nil, fmt.Errorf("Codex session not found: %s", sessionID)
		}
	}

	// Read rollout JSONL file (similar format to Claude)
	rolloutFile := filepath.Join(sessionPath, "rollout.jsonl")
	if _, err := os.Stat(rolloutFile); os.IsNotExist(err) {
		// Try alternative: session_<id>.jsonl
		rolloutFile = filepath.Join(sessionPath, fmt.Sprintf("session_%s.jsonl", sessionID))
	}

	if _, err := os.Stat(rolloutFile); err != nil {
		return nil, fmt.Errorf("Codex rollout file not found: %s", rolloutFile)
	}

	// Parse JSONL file
	file, err := os.Open(rolloutFile)
	if err != nil {
		return nil, fmt.Errorf("open rollout file: %w", err)
	}
	defer file.Close()

	var events []SessionEvent
	scanner := bufio.NewScanner(file)
	seq := int64(0)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var rawData map[string]interface{}
		if err := json.Unmarshal(line, &rawData); err != nil {
			util.DebugLog("[DEBUG] Failed to parse Codex JSONL line: %v", err)
			continue
		}

		seq++

		// Determine event type from data
		eventType := "unknown"
		if msgType, ok := rawData["type"].(string); ok {
			eventType = msgType
		} else if role, ok := rawData["role"].(string); ok {
			eventType = role
		}

		// Extract timestamp
		var timestamp int64
		if ts, ok := rawData["timestamp"].(float64); ok {
			timestamp = int64(ts)
		} else if ts, ok := rawData["created_at"].(float64); ok {
			timestamp = int64(ts)
		} else {
			timestamp = time.Now().UnixMilli()
		}

		events = append(events, SessionEvent{
			Seq:       seq,
			Type:      eventType,
			Timestamp: timestamp,
			Data:      rawData,
			Raw:       string(line),
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan rollout file: %w", err)
	}

	util.DebugLog("[DEBUG] Read %d events from Codex session %s", len(events), sessionID)
	return events, nil
}

func (r *codexSessionReader) ListSessions() ([]string, error) {
	sessionsDir := filepath.Join(r.codexDir, "sessions")
	if _, err := os.Stat(sessionsDir); os.IsNotExist(err) {
		return []string{}, nil
	}

	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return nil, fmt.Errorf("read sessions directory: %w", err)
	}

	var sessions []string
	for _, entry := range entries {
		if entry.IsDir() {
			sessions = append(sessions, entry.Name())
		}
	}

	return sessions, nil
}

func (r *codexSessionReader) GetSessionMetadata(sessionID string) (*SessionMetadata, error) {
	sessionPath := filepath.Join(r.codexDir, "sessions", sessionID)
	if info, err := os.Stat(sessionPath); err == nil {
		return &SessionMetadata{
			SessionID:    sessionID,
			Provider:     "codex",
			CreatedAt:    info.ModTime(),
			UpdatedAt:    info.ModTime(),
			MessageCount: 0, // Would need to parse rollout file to get accurate count
		}, nil
	}
	return nil, fmt.Errorf("Codex session not found: %s", sessionID)
}

// geminiSessionReader - Reads Gemini session files
// Stub implementation - to be fleshed out based on Gemini storage format
type geminiSessionReader struct {
	geminiDir string
}

// GeminiSessionReader creates a new Gemini session reader
func GeminiSessionReader(geminiDir string) SessionReader {
	if geminiDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			geminiDir = "/tmp"
		} else {
			geminiDir = filepath.Join(home, ".gemini")
		}
	}
	return &geminiSessionReader{geminiDir: geminiDir}
}

func (r *geminiSessionReader) ReadSession(sessionID string) ([]SessionEvent, error) {
	// Gemini stores sessions in JSON format
	// Common locations: ~/.gemini/sessions/<sessionID>.json or ~/.gemini/sessions/<sessionID>/session.json

	// Try multiple file paths
	var sessionFile string
	paths := []string{
		filepath.Join(r.geminiDir, "sessions", sessionID+".json"),
		filepath.Join(r.geminiDir, "sessions", sessionID, "session.json"),
		filepath.Join(r.geminiDir, "sessions", sessionID, "history.json"),
		filepath.Join(r.geminiDir, sessionID+".json"),
	}

	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			sessionFile = path
			break
		}
	}

	if sessionFile == "" {
		return nil, fmt.Errorf("Gemini session not found: %s", sessionID)
	}

	// Read JSON file
	data, err := os.ReadFile(sessionFile)
	if err != nil {
		return nil, fmt.Errorf("read session file: %w", err)
	}

	// Parse JSON
	var sessionData map[string]interface{}
	if err := json.Unmarshal(data, &sessionData); err != nil {
		return nil, fmt.Errorf("parse session JSON: %w", err)
	}

	// Extract conversation history
	var events []SessionEvent
	seq := int64(0)

	// Gemini format varies - try multiple structures
	if conversations, ok := sessionData["conversations"].([]interface{}); ok {
		// Format: { "conversations": [ { "role": "user", "parts": [...] }, ... ] }
		for _, conv := range conversations {
			if convMap, ok := conv.(map[string]interface{}); ok {
				seq++
				eventType := "unknown"
				if role, ok := convMap["role"].(string); ok {
					eventType = role
				}

				var timestamp int64
				if ts, ok := convMap["timestamp"].(float64); ok {
					timestamp = int64(ts)
				} else {
					timestamp = time.Now().UnixMilli()
				}

				events = append(events, SessionEvent{
					Seq:       seq,
					Type:      eventType,
					Timestamp: timestamp,
					Data:      convMap,
					Raw:       fmt.Sprintf("%v", convMap),
				})
			}
		}
	} else if messages, ok := sessionData["messages"].([]interface{}); ok {
		// Format: { "messages": [ { "role": "user", "content": "..." }, ... ] }
		for _, msg := range messages {
			if msgMap, ok := msg.(map[string]interface{}); ok {
				seq++
				eventType := "unknown"
				if role, ok := msgMap["role"].(string); ok {
					eventType = role
				}

				var timestamp int64
				if ts, ok := msgMap["timestamp"].(float64); ok {
					timestamp = int64(ts)
				} else {
					timestamp = time.Now().UnixMilli()
				}

				events = append(events, SessionEvent{
					Seq:       seq,
					Type:      eventType,
					Timestamp: timestamp,
					Data:      msgMap,
					Raw:       fmt.Sprintf("%v", msgMap),
				})
			}
		}
	} else if history, ok := sessionData["history"].([]interface{}); ok {
		// Format: { "history": [ ... ] }
		for _, item := range history {
			if itemMap, ok := item.(map[string]interface{}); ok {
				seq++
				eventType := "unknown"
				if role, ok := itemMap["role"].(string); ok {
					eventType = role
				} else if msgType, ok := itemMap["type"].(string); ok {
					eventType = msgType
				}

				var timestamp int64
				if ts, ok := itemMap["timestamp"].(float64); ok {
					timestamp = int64(ts)
				} else {
					timestamp = time.Now().UnixMilli()
				}

				events = append(events, SessionEvent{
					Seq:       seq,
					Type:      eventType,
					Timestamp: timestamp,
					Data:      itemMap,
					Raw:       fmt.Sprintf("%v", itemMap),
				})
			}
		}
	} else {
		// Fallback: treat entire session as one event
		seq++
		events = append(events, SessionEvent{
			Seq:       seq,
			Type:      "session",
			Timestamp: time.Now().UnixMilli(),
			Data:      sessionData,
			Raw:       string(data),
		})
	}

	util.DebugLog("[DEBUG] Read %d events from Gemini session %s", len(events), sessionID)
	return events, nil
}

func (r *geminiSessionReader) ListSessions() ([]string, error) {
	sessionsDir := filepath.Join(r.geminiDir, "sessions")
	if _, err := os.Stat(sessionsDir); os.IsNotExist(err) {
		return []string{}, nil
	}

	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return nil, fmt.Errorf("read sessions directory: %w", err)
	}

	var sessions []string
	for _, entry := range entries {
		// Include both .json files and directories
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			// Remove .json suffix
			sessionID := strings.TrimSuffix(entry.Name(), ".json")
			sessions = append(sessions, sessionID)
		} else if entry.IsDir() {
			sessions = append(sessions, entry.Name())
		}
	}

	return sessions, nil
}

func (r *geminiSessionReader) GetSessionMetadata(sessionID string) (*SessionMetadata, error) {
	// Try to find session file
	paths := []string{
		filepath.Join(r.geminiDir, "sessions", sessionID+".json"),
		filepath.Join(r.geminiDir, "sessions", sessionID, "session.json"),
		filepath.Join(r.geminiDir, "sessions", sessionID, "history.json"),
		filepath.Join(r.geminiDir, sessionID+".json"),
	}

	for _, path := range paths {
		if info, err := os.Stat(path); err == nil {
			// Read file to get message count
			data, _ := os.ReadFile(path)
			var sessionData map[string]interface{}
			messageCount := 0
			if json.Unmarshal(data, &sessionData) == nil {
				if conversations, ok := sessionData["conversations"].([]interface{}); ok {
					messageCount = len(conversations)
				} else if messages, ok := sessionData["messages"].([]interface{}); ok {
					messageCount = len(messages)
				} else if history, ok := sessionData["history"].([]interface{}); ok {
					messageCount = len(history)
				}
			}

			return &SessionMetadata{
				SessionID:    sessionID,
				Provider:     "gemini",
				CreatedAt:    info.ModTime(),
				UpdatedAt:    info.ModTime(),
				MessageCount: messageCount,
			}, nil
		}
	}

	return nil, fmt.Errorf("Gemini session not found: %s", sessionID)
}

// CreateSessionReader - Factory function to create a reader for a specific provider
func CreateSessionReader(provider, sessionDir string) SessionReader {
	switch strings.ToLower(provider) {
	case "claude":
		return ClaudeSessionReader(sessionDir)
	case "codex":
		return CodexSessionReader(sessionDir)
	case "gemini":
		return GeminiSessionReader(sessionDir)
	case "copilot", "copilot-acp", "opencode":
		// ACP providers use SQLite for session persistence
		return ACPSessionReader(sessionDir, provider)
	default:
		return nil
	}
}

// acpSessionReader - Reads ACP session data from SQLite
// Supports Copilot and OpenCode ACP providers
type acpSessionReader struct {
	store    *ACPSessionStore
	provider string
}

// ACPSessionReader creates a new ACP session reader
func ACPSessionReader(sessionDir, provider string) SessionReader {
	store, err := NewACPSessionStore(sessionDir, provider)
	if err != nil {
		util.WarnLog("[WARN] ACPSessionReader: failed to create store: %v", err)
		return nil
	}
	return &acpSessionReader{
		store:    store,
		provider: provider,
	}
}

func (r *acpSessionReader) ReadSession(sessionID string) ([]SessionEvent, error) {
	if r.store == nil {
		return nil, fmt.Errorf("session store not initialized")
	}
	return r.store.ReadSession(sessionID)
}

func (r *acpSessionReader) ListSessions() ([]string, error) {
	if r.store == nil {
		return []string{}, nil
	}
	return r.store.ListSessions()
}

func (r *acpSessionReader) GetSessionMetadata(sessionID string) (*SessionMetadata, error) {
	if r.store == nil {
		return nil, fmt.Errorf("session store not initialized")
	}
	return r.store.GetSessionMetadata(sessionID)
}
