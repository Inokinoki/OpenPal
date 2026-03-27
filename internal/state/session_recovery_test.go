package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"openpal/internal/adapter"
)

// setupMockSessionFiles creates mock session files for testing session recovery
func setupMockSessionFiles(t *testing.T, sessionDir string, sessionID string, events []adapter.SessionEvent) func() {
	t.Helper()

	// Create session directory under projects structure expected by ClaudeSessionReader
	projectsDir := filepath.Join(filepath.Dir(sessionDir), "projects", "test-project", "sessions")
	if err := os.MkdirAll(projectsDir, 0755); err != nil {
		t.Fatalf("Failed to create session directory: %v", err)
	}

	// Convert adapter events to state events for file storage
	stateEvents := make([]Event, len(events))
	for i, event := range events {
		stateEvents[i] = Event{
			Seq:       event.Seq,
			Type:      event.Type,
			Timestamp: event.Timestamp,
			Data:      event.Data,
		}
	}

	// Write session events to JSONL file
	sessionFile := filepath.Join(projectsDir, sessionID+".jsonl")
	file, err := os.Create(sessionFile)
	if err != nil {
		t.Fatalf("Failed to create session file: %v", err)
	}
	defer file.Close()

	for _, event := range stateEvents {
		eventJSON, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("Failed to marshal event: %v", err)
		}
		if _, err := file.WriteString(string(eventJSON) + "\n"); err != nil {
			t.Fatalf("Failed to write event: %v", err)
		}
	}

	// Write provider metadata
	metadataFile := filepath.Join(projectsDir, ".claude-session.json")
	metadata := map[string]interface{}{
		"session_id": sessionID,
		"provider":   "claude",
		"task_id":    "recovery_test",
		"created_at": time.Now().UnixMilli(),
		"updated_at": time.Now().UnixMilli(),
	}
	metadataJSON, _ := json.MarshalIndent(metadata, "", "  ")
	os.WriteFile(metadataFile, metadataJSON, 0644)

	return func() {
		os.RemoveAll(projectsDir)
	}
}

// TestRecoverSessionFromCLISuccess tests successful session recovery
func TestRecoverSessionFromCLISuccess(t *testing.T) {
	mgr, tmpDir, cleanup := setupTestManager(t)
	defer cleanup()

	// Set provider and session ID
	mgr.SetProvider("claude")
	mgr.SetSessionID("test-session-123")

	// Create mock session events
	sessionEvents := []adapter.SessionEvent{
		{
			Seq:       1,
			Type:      "user",
			Timestamp: time.Now().UnixMilli() - 2000,
			Data:      map[string]string{"content": "Hello, I need help with my code"},
		},
		{
			Seq:       2,
			Type:      "assistant",
			Timestamp: time.Now().UnixMilli() - 1000,
			Data: map[string]interface{}{
				"content":  "I'd be happy to help with your code!",
				"thinking": "The user wants assistance with programming",
			},
		},
		{
			Seq:       3,
			Type:      "assistant",
			Timestamp: time.Now().UnixMilli(),
			Data: map[string]interface{}{
				"content": "Could you please share what specific issue you're facing?",
			},
		},
	}

	sessionDir := filepath.Join(tmpDir, "sessions")
	setupCleanup := setupMockSessionFiles(t, sessionDir, "test-session-123", sessionEvents)
	defer setupCleanup()

	// Test recovery
	recovered, err := mgr.RecoverSessionFromCLI()

	if err != nil {
		t.Fatalf("Failed to recover session: %v", err)
	}

	if len(recovered) != len(sessionEvents) {
		t.Errorf("Expected %d events, got %d", len(sessionEvents), len(recovered))
	}

	// Verify recovered events match original
	for i, event := range recovered {
		if event.Seq != sessionEvents[i].Seq {
			t.Errorf("Event %d seq mismatch: expected %d, got %d", i, sessionEvents[i].Seq, event.Seq)
		}
		if event.Type != sessionEvents[i].Type {
			t.Errorf("Event %d type mismatch: expected %s, got %s", i, sessionEvents[i].Type, event.Type)
		}
		if event.Timestamp < sessionEvents[i].Timestamp-100 { // Allow small time difference
			t.Errorf("Event %d timestamp too old: expected >= %d, got %d",
				i, sessionEvents[i].Timestamp-100, event.Timestamp)
		}
	}
}

// TestRecoverSessionFromCLIEmpty tests recovery from empty session
func TestRecoverSessionFromCLIEmpty(t *testing.T) {
	mgr, tmpDir, cleanup := setupTestManager(t)
	defer cleanup()

	mgr.SetProvider("claude")
	mgr.SetSessionID("empty-session")

	sessionDir := filepath.Join(tmpDir, "sessions")
	setupCleanup := setupMockSessionFiles(t, sessionDir, "empty-session", []adapter.SessionEvent{})
	defer setupCleanup()

	recovered, err := mgr.RecoverSessionFromCLI()

	if err != nil {
		t.Fatalf("Failed to recover empty session: %v", err)
	}

	if len(recovered) != 0 {
		t.Errorf("Expected 0 events from empty session, got %d", len(recovered))
	}
}

// TestRecoverSessionFromCLINoProvider tests recovery when no provider is set
func TestRecoverSessionFromCLINoProvider(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	// Don't set provider or session ID
	recovered, err := mgr.RecoverSessionFromCLI()

	if err != nil {
		t.Fatalf("Expected no error when no provider/session, got: %v", err)
	}

	if recovered != nil {
		t.Error("Expected nil result when no provider/session")
	}
}

// TestRecoverSessionFromCLINonExistent tests recovery when session doesn't exist
func TestRecoverSessionFromCLINonExistent(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	mgr.SetProvider("claude")
	mgr.SetSessionID("non-existent-session")

	recovered, err := mgr.RecoverSessionFromCLI()

	// Should not error, just return nil
	if err != nil {
		t.Fatalf("Expected no error for non-existent session, got: %v", err)
	}

	if recovered != nil {
		t.Error("Expected nil result for non-existent session")
	}
}

// TestGetIncrementalOutputWithRecovery tests incremental output with session recovery
func TestGetIncrementalOutputWithRecovery(t *testing.T) {
	mgr, tmpDir, cleanup := setupTestManager(t)
	defer cleanup()

	taskID := "recovery_task"
	mgr.CreateTask(taskID, "claude")

	// Set up session recovery
	mgr.SetProvider("claude")
	mgr.SetSessionID("recovery-test-session")

	// Create mock session with existing events
	existingEvents := []adapter.SessionEvent{
		{
			Seq:       1,
			Type:      "user",
			Timestamp: time.Now().UnixMilli() - 3000,
			Data:      map[string]string{"content": "First message"},
		},
		{
			Seq:       2,
			Type:      "assistant",
			Timestamp: time.Now().UnixMilli() - 2000,
			Data:      map[string]string{"content": "Response to first message"},
		},
		{
			Seq:       3,
			Type:      "assistant",
			Timestamp: time.Now().UnixMilli() - 1000,
			Data:      map[string]string{"content": "Continuation of response"},
		},
	}

	sessionDir := filepath.Join(tmpDir, "sessions")
	setupCleanup := setupMockSessionFiles(t, sessionDir, "recovery-test-session", existingEvents)
	defer setupCleanup()

	// Test 1: Get all events from beginning (recovery + cache population)
	events1, err := mgr.GetIncrementalOutput(taskID, 0)
	if err != nil {
		t.Fatalf("Failed to get incremental output: %v", err)
	}

	if len(events1) != 3 {
		t.Errorf("Expected 3 events from recovery, got %d", len(events1))
	}

	// Add new events to cache
	newEvent1 := Event{
		Type: "chunk",
		Data: map[string]string{"content": "New message 1"},
	}
	mgr.AddOutput(taskID, newEvent1)

	newEvent2 := Event{
		Type: "chunk",
		Data: map[string]string{"content": "New message 2"},
	}
	mgr.AddOutput(taskID, newEvent2)

	// Test 2: Get all events from beginning (recovered + newly added)
	// Note: AddOutput seq starts from 1 because recovery doesn't update state.json seq,
	// so new events may have overlapping seq values with recovered events.
	events2, err := mgr.GetIncrementalOutput(taskID, 0)
	if err != nil {
		t.Fatalf("Failed to get incremental output: %v", err)
	}

	// Should have 5 events total: 3 recovered + 2 new
	if len(events2) != 5 {
		t.Errorf("Expected 5 events (3 recovered + 2 new), got %d", len(events2))
	}

	// Test 3: Get events after all recovered events (seq 3)
	// Since new events start from seq 1 (not seq 4), requesting after seq 3
	// won't find new events in cache - this demonstrates the seq overlap behavior.
	events3, err := mgr.GetIncrementalOutput(taskID, 3)
	if err != nil {
		t.Fatalf("Failed to get incremental output: %v", err)
	}

	// New events have seq 1 and 2, both <= 3, so nothing is returned
	if len(events3) != 0 {
		t.Errorf("Expected 0 events (new events have seq <= 3), got %d", len(events3))
	}
}

// TestCachePopulationFromSession tests populating cache with recovered events
func TestCachePopulationFromSession(t *testing.T) {
	mgr, tmpDir, cleanup := setupTestManager(t)
	defer cleanup()

	taskID := "cache_pop_test"
	mgr.CreateTask(taskID, "claude")

	// Set up session recovery
	mgr.SetProvider("claude")
	mgr.SetSessionID("cache-pop-session")

	// Create session with many events
	sessionEvents := make([]adapter.SessionEvent, 100)
	for i := 1; i <= 100; i++ {
		sessionEvents[i-1] = adapter.SessionEvent{
			Seq:       int64(i),
			Type:      "chunk",
			Timestamp: time.Now().UnixMilli() + int64(i)*100,
			Data:      map[string]string{"content": fmt.Sprintf("Event %d", i)},
		}
	}

	sessionDir := filepath.Join(tmpDir, "sessions")
	setupCleanup := setupMockSessionFiles(t, sessionDir, "cache-pop-session", sessionEvents)
	defer setupCleanup()

	// Trigger cache population by calling GetIncrementalOutput
	_, _ = mgr.GetIncrementalOutput(taskID, 0)

	// Verify cache is populated
	mgr.cacheMu.RLock()
	cache := mgr.outputCache[taskID]
	mgr.cacheMu.RUnlock()

	if len(cache.events) != 100 {
		t.Errorf("Expected 100 events in cache, got %d", len(cache.events))
	}

	// Verify cache efficiency
	stats := mgr.GetCacheStats()
	if stats["total_events"].(int) != 100 {
		t.Errorf("Expected 100 total events in stats, got %d", stats["total_events"])
	}
}

// TestMixedSourceRecovery tests that recovery populates cache, then new events are added
func TestMixedSourceRecovery(t *testing.T) {
	mgr, tmpDir, cleanup := setupTestManager(t)
	defer cleanup()

	taskID := "mixed_source_test"
	mgr.CreateTask(taskID, "claude")

	mgr.SetProvider("claude")
	mgr.SetSessionID("mixed-source-session")

	sessionEvents := []adapter.SessionEvent{
		{
			Seq:       1,
			Type:      "user",
			Timestamp: time.Now().UnixMilli() - 4000,
			Data:      map[string]string{"content": "Message 1"},
		},
		{
			Seq:       2,
			Type:      "assistant",
			Timestamp: time.Now().UnixMilli() - 3000,
			Data:      map[string]string{"content": "Response 1"},
		},
		{
			Seq:       3,
			Type:      "assistant",
			Timestamp: time.Now().UnixMilli() - 2000,
			Data:      map[string]string{"content": "Response 2"},
		},
		{
			Seq:       4,
			Type:      "assistant",
			Timestamp: time.Now().UnixMilli() - 1000,
			Data:      map[string]string{"content": "Response 3"},
		},
		{
			Seq:       5,
			Type:      "assistant",
			Timestamp: time.Now().UnixMilli(),
			Data:      map[string]string{"content": "Response 4"},
		},
	}

	sessionDir := filepath.Join(tmpDir, "sessions")
	setupCleanup := setupMockSessionFiles(t, sessionDir, "mixed-source-session", sessionEvents)
	defer setupCleanup()

	// Step 1: Trigger recovery which populates cache with 5 session events
	events1, err := mgr.GetIncrementalOutput(taskID, 0)
	if err != nil {
		t.Fatalf("Failed to get incremental output: %v", err)
	}
	if len(events1) != 5 {
		t.Fatalf("Expected 5 recovered events, got %d", len(events1))
	}

	// Step 2: Add new events via AddOutput (seq starts from 1 due to state.json seq=0)
	for i := 0; i < 3; i++ {
		event := Event{
			Type: "chunk",
			Data: map[string]string{"content": fmt.Sprintf("New message %d", i+1)},
		}
		mgr.AddOutput(taskID, event)
	}

	// Step 3: Get all events from cache (now contains 5 recovered + 3 new = 8 events)
	events2, err := mgr.GetIncrementalOutput(taskID, 0)
	if err != nil {
		t.Fatalf("Failed to get incremental output: %v", err)
	}

	if len(events2) != 8 {
		t.Errorf("Expected 8 events total (5 recovered + 3 new), got %d", len(events2))
	}

	// Step 4: Verify events after seq 3
	// Only recovered events with seq > 3 pass the filter (seq 4, 5)
	// New events have seq 1,2,3 which are all <= 3
	events3, err := mgr.GetIncrementalOutput(taskID, 3)
	if err != nil {
		t.Fatalf("Failed to get incremental output: %v", err)
	}

	if len(events3) != 2 {
		t.Errorf("Expected 2 events with seq > 3 (recovered 4,5), got %d", len(events3))
	}

	// Step 5: Verify events after seq 5 returns only new events with seq > 5
	// New events have seq 1,2,3 — none > 5, so result is empty
	events4, err := mgr.GetIncrementalOutput(taskID, 5)
	if err != nil {
		t.Fatalf("Failed to get incremental output: %v", err)
	}

	if len(events4) != 0 {
		t.Errorf("Expected 0 events with seq > 5, got %d", len(events4))
	}
}

// TestRecoveryWithCacheLimits tests session recovery with cache limits
func TestRecoveryWithCacheLimits(t *testing.T) {
	mgr, tmpDir, cleanup := setupTestManager(t)
	defer cleanup()

	// Set strict cache limits
	mgr.maxEventsPerTask = 10
	mgr.maxTaskCount = 5
	mgr.maxTotalMemory = 1024 * 1024 // 1MB

	taskID := "limited_recovery_test"
	mgr.CreateTask(taskID, "claude")

	// Set up session recovery
	mgr.SetProvider("claude")
	mgr.SetSessionID("limited-session")

	// Create session with many events (more than cache can hold)
	sessionEvents := make([]adapter.SessionEvent, 20)
	for i := 1; i <= 20; i++ {
		sessionEvents[i-1] = adapter.SessionEvent{
			Seq:       int64(i),
			Type:      "chunk",
			Timestamp: time.Now().UnixMilli() + int64(i)*100,
			Data: map[string]string{
				"content": fmt.Sprintf("This is a longer message number %d with more content to consume memory", i),
			},
		}
	}

	sessionDir := filepath.Join(tmpDir, "sessions")
	setupCleanup := setupMockSessionFiles(t, sessionDir, "limited-session", sessionEvents)
	defer setupCleanup()

	// Trigger recovery and cache population
	_, _ = mgr.GetIncrementalOutput(taskID, 0)

	// Should be limited by cache size
	mgr.cacheMu.RLock()
	cache := mgr.outputCache[taskID]
	mgr.cacheMu.RUnlock()

	if len(cache.events) > 10 {
		t.Errorf("Expected cache limited to 10 events, got %d", len(cache.events))
	}

	// Verify only the most recent events are kept
	if len(cache.events) > 0 {
		if cache.events[0].Seq != 11 { // Should have seq 11-20
			t.Errorf("Expected to keep most recent events, first event has seq %d", cache.events[0].Seq)
		}
	}
}
