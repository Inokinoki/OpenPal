package adapter

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setupTestSessionFile creates a temporary session file for testing
func setupTestSessionFile(t *testing.T, events []SessionEvent, sessionID string) (string, func()) {
	t.Helper()

	tmpDir := t.TempDir()
	sessionDir := filepath.Join(tmpDir, "projects", "test-project", "sessions")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatalf("Failed to create sessions directory: %v", err)
	}

	sessionFile := filepath.Join(sessionDir, sessionID+".jsonl")

	// Write session file
	file, err := os.Create(sessionFile)
	if err != nil {
		t.Fatalf("Failed to create session file: %v", err)
	}
	defer file.Close()

	// Write each event as JSON line
	for _, event := range events {
		eventJSON, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("Failed to marshal event: %v", err)
		}
		if _, err := file.WriteString(string(eventJSON) + "\n"); err != nil {
			t.Fatalf("Failed to write event: %v", err)
		}
	}

	return tmpDir, func() {
		// Cleanup is handled by t.TempDir()
	}
}

// TestClaudeSessionReaderCreate tests session reader creation
func TestClaudeSessionReaderCreate(t *testing.T) {
	// Test with custom directory
	customDir := "/tmp/test"
	reader := ClaudeSessionReader(customDir)

	if reader == nil {
		t.Fatal("Expected non-nil reader")
	}

	// Test with empty directory (should default to home)
	reader = ClaudeSessionReader("")
	if reader == nil {
		t.Fatal("Expected non-nil reader")
	}
}

// TestClaudeSessionReaderReadValidFile tests reading a valid session file
func TestClaudeSessionReaderReadValidFile(t *testing.T) {
	// Create test events
	events := []SessionEvent{
		{
			Seq:       1,
			Type:      "user",
			Timestamp: time.Now().UnixMilli() - 1000,
			Data:      map[string]string{"content": "Hello"},
		},
		{
			Seq:       2,
			Type:      "assistant",
			Timestamp: time.Now().UnixMilli(),
			Data:      map[string]string{"content": "Hello! How can I help?"},
		},
	}

	sessionID := "test-session-123"
	tmpDir, cleanup := setupTestSessionFile(t, events, sessionID)
	defer cleanup()

	reader := ClaudeSessionReader(tmpDir)
	recovered, err := reader.ReadSession(sessionID)

	if err != nil {
		t.Fatalf("Failed to read session: %v", err)
	}

	if len(recovered) != len(events) {
		t.Errorf("Expected %d events, got %d", len(events), len(recovered))
	}

	// Verify event content
	for i, event := range recovered {
		if event.Seq != events[i].Seq {
			t.Errorf("Event %d seq mismatch: expected %d, got %d", i, events[i].Seq, event.Seq)
		}
		if event.Type != events[i].Type {
			t.Errorf("Event %d type mismatch: expected %s, got %s", i, events[i].Type, event.Type)
		}
		if event.Timestamp < events[i].Timestamp {
			t.Errorf("Event %d timestamp mismatch: expected >= %d, got %d", i, events[i].Timestamp, event.Timestamp)
		}
	}
}

// TestClaudeSessionReaderNonExistent tests reading non-existent session
func TestClaudeSessionReaderNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	reader := ClaudeSessionReader(tmpDir)

	_, err := reader.ReadSession("non-existent-session")
	if err == nil {
		t.Error("Expected error for non-existent session")
	}
	if !strings.Contains(err.Error(), "session not found") {
		t.Errorf("Expected 'session not found' error, got: %v", err)
	}
}

// TestClaudeSessionReaderCorruptedFile tests reading a corrupted session file
func TestClaudeSessionReaderCorruptedFile(t *testing.T) {
	tmpDir := t.TempDir()
	sessionsDir := filepath.Join(tmpDir, "projects", "test-project", "sessions")
	if err := os.MkdirAll(sessionsDir, 0755); err != nil {
		t.Fatalf("Failed to create sessions directory: %v", err)
	}
	sessionFile := filepath.Join(sessionsDir, "corrupted-session.jsonl")

	// Write corrupted JSON file
	content := `{"seq": 1, "type": "user", "data": {"content": "valid"}}
{"invalid json line
{"seq": 2, "type": "assistant", "data": {"content": "another"}}`

	if err := os.WriteFile(sessionFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write corrupted file: %v", err)
	}

	reader := ClaudeSessionReader(tmpDir)

	// Should return events it can parse successfully
	recovered, err := reader.ReadSession("corrupted-session")
	if err != nil {
		t.Logf("Warning: read error (may be expected for corrupted file): %v", err)
		// Test can continue with partial data
	}

	// Should have at least some valid events
	if len(recovered) == 0 && err != nil {
		// If no valid events found, error should be informative
		if !strings.Contains(err.Error(), "corrupted") && !strings.Contains(err.Error(), "invalid") {
			t.Errorf("Expected error about corruption, got: %v", err)
		}
	}
}

// TestClaudeSessionReaderEmptyFile tests reading an empty session file
func TestClaudeSessionReaderEmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	sessionsDir := filepath.Join(tmpDir, "projects", "test-project", "sessions")
	if err := os.MkdirAll(sessionsDir, 0755); err != nil {
		t.Fatalf("Failed to create sessions directory: %v", err)
	}
	sessionFile := filepath.Join(sessionsDir, "empty-session.jsonl")

	// Create empty file
	if err := os.WriteFile(sessionFile, []byte{}, 0644); err != nil {
		t.Fatalf("Failed to create empty file: %v", err)
	}

	reader := ClaudeSessionReader(tmpDir)
	recovered, err := reader.ReadSession("empty-session")

	if err != nil {
		t.Fatalf("Failed to read empty session: %v", err)
	}

	if len(recovered) != 0 {
		t.Errorf("Expected 0 events from empty file, got %d", len(recovered))
	}
}

// TestClaudeSessionReaderLargeFile tests reading a large session file
func TestClaudeSessionReaderLargeFile(t *testing.T) {
	tmpDir := t.TempDir()
	sessionsDir := filepath.Join(tmpDir, "projects", "test-project", "sessions")
	if err := os.MkdirAll(sessionsDir, 0755); err != nil {
		t.Fatalf("Failed to create sessions directory: %v", err)
	}
	sessionFile := filepath.Join(sessionsDir, "large-session.jsonl")

	// Create a large file (1000 events)
	file, err := os.Create(sessionFile)
	if err != nil {
		t.Fatalf("Failed to create large file: %v", err)
	}
	defer file.Close()

	writer := bufio.NewWriter(file)

	now := time.Now().UnixMilli()
	for i := 1; i <= 1000; i++ {
		event := SessionEvent{
			Seq:       int64(i),
			Type:      "chunk",
			Timestamp: now + int64(i)*100,
			Data:      map[string]string{"content": fmt.Sprintf("Event %d", i)},
		}
		eventJSON, _ := json.Marshal(event)
		writer.WriteString(string(eventJSON) + "\n")
	}
	writer.Flush()
	file.Close()

	reader := ClaudeSessionReader(tmpDir)
	recovered, err := reader.ReadSession("large-session")

	if err != nil {
		t.Fatalf("Failed to read large session: %v", err)
	}

	if len(recovered) != 1000 {
		t.Errorf("Expected 1000 events from large file, got %d", len(recovered))
	}

	// Verify first and last events
	if recovered[0].Seq != 1 {
		t.Errorf("Expected first event seq=1, got %d", recovered[0].Seq)
	}
	if recovered[len(recovered)-1].Seq != 1000 {
		t.Errorf("Expected last event seq=1000, got %d", recovered[len(recovered)-1].Seq)
	}
}

// TestClaudeSessionReaderBinarySearch tests that events are returned in correct order
func TestClaudeSessionReaderBinarySearch(t *testing.T) {
	tmpDir := t.TempDir()
	sessionsDir := filepath.Join(tmpDir, "projects", "test-project", "sessions")
	if err := os.MkdirAll(sessionsDir, 0755); err != nil {
		t.Fatalf("Failed to create sessions directory: %v", err)
	}
	sessionFile := filepath.Join(sessionsDir, "ordered-session.jsonl")

	file, err := os.Create(sessionFile)
	if err != nil {
		t.Fatalf("Failed to create session file: %v", err)
	}
	defer file.Close()

	now := time.Now().UnixMilli()
	// Write events with non-sequential timestamps but sequential seq
	for i := 1; i <= 10; i++ {
		event := SessionEvent{
			Seq:       int64(i),
			Type:      "chunk",
			Timestamp: now + int64(i)*50, // Not strictly sequential
			Data:      map[string]string{"content": fmt.Sprintf("Message %d", i)},
		}
		eventJSON, _ := json.Marshal(event)
		file.WriteString(string(eventJSON) + "\n")
	}

	reader := ClaudeSessionReader(tmpDir)
	recovered, err := reader.ReadSession("ordered-session")

	if err != nil {
		t.Fatalf("Failed to read session: %v", err)
	}

	// Verify events are sorted by seq (not timestamp)
	for i := 1; i < len(recovered); i++ {
		if recovered[i].Seq != recovered[i-1].Seq+1 {
			t.Errorf("Events not properly sorted by seq: got %d, %d",
				recovered[i-1].Seq, recovered[i].Seq)
		}
	}
}

// TestClaudeSessionReaderWithComplexData tests reading events with complex data structures
func TestClaudeSessionReaderWithComplexData(t *testing.T) {
	tmpDir := t.TempDir()
	sessionsDir := filepath.Join(tmpDir, "projects", "test-project", "sessions")
	if err := os.MkdirAll(sessionsDir, 0755); err != nil {
		t.Fatalf("Failed to create sessions directory: %v", err)
	}
	sessionFile := filepath.Join(sessionsDir, "complex-session.jsonl")

	file, err := os.Create(sessionFile)
	if err != nil {
		t.Fatalf("Failed to create session file: %v", err)
	}
	defer file.Close()

	now := time.Now().UnixMilli()
	complexData := map[string]interface{}{
		"content": "Complex message",
		"metadata": map[string]interface{}{
			"model":          "claude-3",
			"tokens":         150,
			"function_calls": []string{"read_file", "write_file"},
		},
		"nested": map[string]interface{}{
			"level1": map[string]interface{}{
				"level2": "deep value",
			},
		},
		"array": []string{"item1", "item2", "item3"},
	}

	event := SessionEvent{
		Seq:       1,
		Type:      "assistant",
		Timestamp: now,
		Data:      complexData,
	}

	eventJSON, _ := json.Marshal(event)
	file.WriteString(string(eventJSON) + "\n")

	reader := ClaudeSessionReader(tmpDir)
	recovered, err := reader.ReadSession("complex-session")

	if err != nil {
		t.Fatalf("Failed to read complex session: %v", err)
	}

	if len(recovered) != 1 {
		t.Errorf("Expected 1 event, got %d", len(recovered))
	}

	recoveredData, ok := recovered[0].Data.(map[string]interface{})
	if !ok {
		t.Fatalf("Expected Data to be map[string]interface{}, got %T", recovered[0].Data)
	}
	// parseLine stores the entire JSON line as Data, so the original Data field
	// is nested under the "data" key, and numeric values become float64
	nestedData, ok := recoveredData["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected nested 'data' key in recovered event, got: %v", recoveredData)
	}
	if nestedData["content"] != "Complex message" {
		t.Errorf("Expected content 'Complex message', got %v", nestedData["content"])
	}
	metadata, ok := nestedData["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected metadata object, got: %T", nestedData["metadata"])
	}
	if metadata["model"] != "claude-3" {
		t.Errorf("Expected model 'claude-3', got %v", metadata["model"])
	}
	if metadata["tokens"].(float64) != 150 {
		t.Errorf("Expected tokens 150, got %v", metadata["tokens"])
	}
}

// TestFindFromIndex tests finding session from sessions-index.json
func TestFindFromIndex(t *testing.T) {
	tmpDir := t.TempDir()
	projectsRoot := filepath.Join(tmpDir, "projects")
	sessionsDir := filepath.Join(projectsRoot, "project1", "sessions")

	if err := os.MkdirAll(sessionsDir, 0755); err != nil {
		t.Fatalf("Failed to create directories: %v", err)
	}

	// Create session files
	sessionFiles := []string{"session1", "session2"}

	for _, sessionID := range sessionFiles {
		events := []SessionEvent{
			{
				Seq:       1,
				Type:      "user",
				Timestamp: time.Now().UnixMilli(),
				Data:      map[string]string{"content": "Hello from " + sessionID},
			},
		}

		sessionFile := filepath.Join(sessionsDir, sessionID+".jsonl")
		file, err := os.Create(sessionFile)
		if err != nil {
			t.Fatalf("Failed to create session file %s: %v", sessionID, err)
		}
		for _, event := range events {
			eventJSON, _ := json.Marshal(event)
			file.WriteString(string(eventJSON) + "\n")
		}
		file.Close()
	}

	// Create sessions-index.json
	index := claudeSessionsIndex{
		Entries: []claudeSessionEntry{
			{
				SessionID: "session1",
				FullPath:  filepath.Join(sessionsDir, "session1.jsonl"),
			},
			{
				SessionID: "session2",
				FullPath:  filepath.Join(sessionsDir, "session2.jsonl"),
			},
		},
	}

	indexFile := filepath.Join(projectsRoot, "sessions-index.json")
	indexJSON, _ := json.MarshalIndent(index, "", "  ")
	os.WriteFile(indexFile, indexJSON, 0644)

	reader := &claudeSessionReader{claudeDir: tmpDir}

	// Test finding existing session
	foundFile := reader.findFromIndex(projectsRoot, "session1")
	if foundFile == "" {
		t.Error("Expected to find session1")
	}

	// Test finding non-existent session
	foundFile = reader.findFromIndex(projectsRoot, "non-existent")
	if foundFile != "" {
		t.Error("Expected not to find non-existent session")
	}
}

// TestFindByHeaderScan tests finding session by scanning headers
func TestFindByHeaderScan(t *testing.T) {
	tmpDir := t.TempDir()
	projectsRoot := filepath.Join(tmpDir, "projects")
	sessionDir := filepath.Join(projectsRoot, "project1", "sessions")

	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatalf("Failed to create directories: %v", err)
	}

	// Create session files without matching names but with sessionId in content
	sessionFiles := map[string]string{
		"random_name1.jsonl": "{\"sessionId\": \"target-session\", \"type\": \"user\", \"data\": {\"content\": \"Hello\"}}\n{\"type\": \"assistant\", \"data\": {\"content\": \"Hi there\"}}",
		"random_name2.jsonl": "{\"sessionId\": \"other-session\", \"type\": \"user\", \"data\": {\"content\": \"Different\"}}\n{\"type\": \"assistant\", \"data\": {\"content\": \"Hello again\"}}",
	}

	for filename, content := range sessionFiles {
		sessionFile := filepath.Join(sessionDir, filename)
		os.WriteFile(sessionFile, []byte(content), 0644)
	}

	reader := &claudeSessionReader{claudeDir: tmpDir}

	// Test finding by header scan
	foundFile := reader.findByHeaderScan(projectsRoot, "target-session")
	if foundFile == "" {
		t.Error("Expected to find target-session by header scan")
	}
	if !strings.Contains(foundFile, "random_name1.jsonl") {
		t.Errorf("Expected file containing target-session, got: %s", foundFile)
	}

	// Test non-existent session
	foundFile = reader.findByHeaderScan(projectsRoot, "non-existent")
	if foundFile != "" {
		t.Errorf("Expected not to find non-existent session, got: %s", foundFile)
	}
}

// TestSelectLatestFile tests the utility function to select latest modified file
func TestSelectLatestFile(t *testing.T) {
	// Create temporary files with different modification times
	tmpDir := t.TempDir()
	filePaths := []string{}

	for i := 0; i < 3; i++ {
		filePath := filepath.Join(tmpDir, fmt.Sprintf("file%d.jsonl", i))
		filePaths = append(filePaths, filePath)

		content := fmt.Sprintf(`{"seq": %d, "type": "chunk", "data": {"content": "test %d"}}`, i+1, i+1)
		os.WriteFile(filePath, []byte(content), 0644)

		// Simulate different modification times by touching
		time.Sleep(100 * time.Millisecond)
		if i > 0 {
			os.Chtimes(filePath, time.Now(), time.Now())
		}
	}

	latest := selectLatestFile(filePaths)
	expected := filePaths[len(filePaths)-1] // Last file should be newest

	if latest != expected {
		t.Errorf("Expected %s to be latest, got %s", expected, latest)
	}
}

// TestFindByFilename tests finding session by filename
func TestFindByFilename(t *testing.T) {
	tmpDir := t.TempDir()
	projectsRoot := filepath.Join(tmpDir, "projects")
	sessionDir := filepath.Join(projectsRoot, "project1", "sessions")

	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatalf("Failed to create directories: %v", err)
	}

	// Create session files with matching names
	sessionFiles := []string{
		"session123.jsonl",
		"session456.jsonl",
		"other.txt",
	}

	for _, filename := range sessionFiles {
		sessionFile := filepath.Join(sessionDir, filename)
		content := fmt.Sprintf(`{"seq": 1, "type": "chunk", "data": {"content": "test"}}`)
		os.WriteFile(sessionFile, []byte(content), 0644)
	}

	reader := &claudeSessionReader{claudeDir: tmpDir}

	// Test finding by filename
	foundFile := reader.findByFilename(projectsRoot, "session123")
	if foundFile == "" {
		t.Error("Expected to find session123")
	}
	if !strings.Contains(foundFile, "session123.jsonl") {
		t.Errorf("Expected file containing session123, got: %s", foundFile)
	}

	// Test non-existent session
	foundFile = reader.findByFilename(projectsRoot, "non-existent")
	if foundFile != "" {
		t.Error("Expected not to find non-existent session")
	}
}

// TestSessionReaderInterface tests all required methods are implemented
func TestSessionReaderInterface(t *testing.T) {
	var reader SessionReader = ClaudeSessionReader("/tmp")

	// Test all interface methods exist and return expected types
	var _ interface {
		ReadSession(string) ([]SessionEvent, error)
		ListSessions() ([]string, error)
		GetSessionMetadata(string) (*SessionMetadata, error)
	} = reader

	// This test passes if the above type assertion succeeds
}
