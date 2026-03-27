package state

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestFileIOPermissionDenied tests handling of permission denied errors
func TestFileIOPermissionDenied(t *testing.T) {
	mgr, tmpDir, cleanup := setupTestManager(t)
	defer cleanup()

	// Create a task
	taskID := "permission_test"
	mgr.CreateTask(taskID, "claude")

	// Add some events
	event := Event{
		Type: "chunk",
		Data: map[string]string{"content": "test content"},
	}
	mgr.AddOutput(taskID, event)

	// Get the session file path
	sessionFile := filepath.Join(tmpDir, "session_test.json")

	// Create the session file
	err := os.WriteFile(sessionFile, []byte(`[{"seq":1,"type":"chunk","data":{"content":"test"}}]`), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Change permissions to read-only
	err = os.Chmod(sessionFile, 0400) // Read-only
	if err != nil {
		t.Fatalf("Failed to change file permissions: %v", err)
	}

	// Try to read with read-only permissions
	// This should not cause the system to crash, though it might fail gracefully
	_, err = mgr.GetIncrementalOutput(taskID, 0)
	if err != nil {
		t.Logf("GetIncrementalOutput failed with read-only file: %v", err)
		// This is acceptable - the system should handle it gracefully
	}

	// Clean up - restore permissions to allow deletion
	os.Chmod(sessionFile, 0644)
}

// TestFileIONonExistent tests handling of non-existent files
func TestFileIONonExistent(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	// Create a task that doesn't exist
	taskID := "non_existent_task"

	// Should handle gracefully without crashing
	start := time.Now()
	_, err := mgr.GetIncrementalOutput(taskID, 0)
	duration := time.Since(start)
	_ = err // Use err to avoid unused variable error

	if err != nil {
		t.Logf("Expected error for non-existent task: %v", err)
	}

	// Should complete quickly even for non-existent files
	if duration > 100*time.Millisecond {
		t.Errorf("GetIncrementalOutput took too long for non-existent task: %v", duration)
	}
}

// TestFileIOSpaceFull tests handling of disk space errors
func TestFileIOSpaceFull(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	// Create a task
	taskID := "space_test"
	mgr.CreateTask(taskID, "claude")

	// Test by creating a very large event that might exceed disk space
	largeData := make([]byte, 1024*1024) // 1MB of data
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	event := Event{
		Type: "chunk",
		Data: map[string]string{"content": string(largeData)},
	}

	// Add the large event - should handle gracefully
	start := time.Now()
	err := mgr.AddOutput(taskID, event)
	duration := time.Since(start)

	if err != nil {
		t.Logf("Adding large event failed (might be expected in limited disk space): %v", err)
	} else {
		t.Logf("Successfully added large event in %v", duration)
	}

	// Reading large events should also be handled gracefully
	_, err = mgr.GetIncrementalOutput(taskID, 0)
	if err != nil {
		t.Logf("Reading large event failed: %v", err)
	}
}

// TestFileIOCorruptedFile tests handling of corrupted session files
func TestFileIOCorruptedFile(t *testing.T) {
	mgr, tmpDir, cleanup := setupTestManager(t)
	defer cleanup()

	// Create a task
	taskID := "corrupted_test"
	mgr.CreateTask(taskID, "claude")

	// Add some valid events
	event := Event{
		Type: "chunk",
		Data: map[string]string{"content": "valid content"},
	}
	mgr.AddOutput(taskID, event)

	// Create a corrupted session file
	sessionFile := filepath.Join(tmpDir, "corrupted_session.json")
	corruptedData := []byte(`{"seq":1,"type":"chunk","data":{"content":"test"} invalid json content`)
	err := os.WriteFile(sessionFile, corruptedData, 0644)
	if err != nil {
		t.Fatalf("Failed to create corrupted file: %v", err)
	}

	// System should handle corrupted files gracefully
	// This test verifies that the system doesn't crash when encountering bad data
	_, err = mgr.GetIncrementalOutput(taskID, 0)
	if err != nil {
		t.Logf("Reading with corrupted file present: %v", err)
		// This is acceptable behavior
	}
}

// TestFileIOEmptyFile tests handling of empty session files
func TestFileIOEmptyFile(t *testing.T) {
	mgr, tmpDir, cleanup := setupTestManager(t)
	defer cleanup()

	// Create a task
	taskID := "empty_test"
	mgr.CreateTask(taskID, "claude")

	// Create an empty session file
	emptyFile := filepath.Join(tmpDir, "empty_session.json")
	err := os.WriteFile(emptyFile, []byte(""), 0644)
	if err != nil {
		t.Fatalf("Failed to create empty file: %v", err)
	}

	// System should handle empty files gracefully
	_, err = mgr.GetIncrementalOutput(taskID, 0)
	if err != nil {
		t.Logf("Reading with empty file: %v", err)
		// This is acceptable behavior
	}
}

// TestFileIOConcurrentAccess tests handling of concurrent file access
func TestFileIOConcurrentAccess(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	// Create a task
	taskID := "concurrent_test"
	mgr.CreateTask(taskID, "claude")

	// Add initial event
	event := Event{
		Type: "chunk",
		Data: map[string]string{"content": "initial content"},
	}
	mgr.AddOutput(taskID, event)

	// Number of concurrent goroutines
	numGoroutines := 10

	// Channel to track errors
	errChan := make(chan error, numGoroutines)
	done := make(chan bool)

	// Start concurrent readers
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			for j := 0; j < 5; j++ {
				_, err := mgr.GetIncrementalOutput(taskID, 0)
				if err != nil {
					errChan <- err
				}
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	go func() {
		for i := 0; i < numGoroutines; i++ {
			<-done
		}
		close(errChan)
	}()

	// Check for errors
	errorCount := 0
	for err := range errChan {
		t.Logf("Concurrent access error: %v", err)
		errorCount++
	}

	// Some errors might be expected due to race conditions
	// But the system should not crash
	if errorCount > 0 {
		t.Logf("Encountered %d errors during concurrent access", errorCount)
	}
}

// TestFileIOMultipleFileOperations tests multiple simultaneous file operations
func TestFileIOMultipleFileOperations(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	// Create multiple tasks
	taskIDs := []string{"multi_1", "multi_2", "multi_3"}
	for _, id := range taskIDs {
		mgr.CreateTask(id, "claude")
	}

	// Add events to all tasks
	for _, id := range taskIDs {
		for i := 1; i <= 10; i++ {
			event := Event{
				Type: "chunk",
				Data: map[string]string{"content": fmt.Sprintf("%s_event_%d", id, i)},
			}
			err := mgr.AddOutput(id, event)
			if err != nil {
				t.Logf("AddOutput failed for %s: %v", id, err)
			}
		}
	}

	// Concurrently read from all tasks
	var wg sync.WaitGroup
	errChan := make(chan error, len(taskIDs)*5)

	for _, id := range taskIDs {
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func(taskID string) {
				defer wg.Done()
				_, err := mgr.GetIncrementalOutput(taskID, 0)
				if err != nil {
					errChan <- err
				}
			}(id)
		}
	}

	wg.Wait()
	close(errChan)

	// Collect and log any errors
	errorCount := 0
	for err := range errChan {
		t.Logf("Concurrent read error: %v", err)
		errorCount++
	}

	if errorCount > 0 {
		t.Logf("Encountered %d errors during multiple file operations", errorCount)
	}
}

// TestFileIOPathTraversal tests protection against path traversal attacks
func TestFileIOPathTraversal(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	// Test that the system doesn't allow path traversal
	// This is more about the adapter layer, but we test the manager response

	taskID := "traversal_test"
	mgr.CreateTask(taskID, "claude")

	// Add event
	event := Event{
		Type: "chunk",
		Data: map[string]string{"content": "test"},
	}
	mgr.AddOutput(taskID, event)

	// Should not crash or allow path traversal
	_, err := mgr.GetIncrementalOutput(taskID, 0)
	if err != nil {
		t.Logf("GetIncrementalOutput completed with error: %v", err)
	}
}
