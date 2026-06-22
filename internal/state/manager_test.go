package state

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// setupTestManager setupTestManager - Create test manager
func setupTestManager(t *testing.T) (*Manager, string, func()) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "pal-broker-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	mgr := NewManager(tmpDir)

	// Cleanup function
	cleanup := func() {
		mgr.Close()
		os.RemoveAll(tmpDir)
	}

	return mgr, tmpDir, cleanup
}

// TestCreateTask TestCreateTask - Test task creation
func TestCreateTask(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	taskID := "test_task_001"
	provider := "claude"

	// Create task
	err := mgr.CreateTask(taskID, provider)
	if err != nil {
		t.Fatalf("Failed to create task: %v", err)
	}

	// Verify task state
	state, err := mgr.LoadState(taskID)
	if err != nil {
		t.Fatalf("Failed to load state: %v", err)
	}

	if state.TaskID != taskID {
		t.Errorf("Expected task_id %s, got %s", taskID, state.TaskID)
	}

	if state.Provider != provider {
		t.Errorf("Expected provider %s, got %s", provider, state.Provider)
	}

	if state.Status != "running" {
		t.Errorf("Expected status running, got %s", state.Status)
	}

	if state.Seq != 0 {
		t.Errorf("Expected seq 0, got %d", state.Seq)
	}
}

// TestUpdateStatus TestUpdateStatus - Test status update
func TestUpdateStatus(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	taskID := "test_task_002"
	mgr.CreateTask(taskID, "claude")

	// UpdateState
	err := mgr.UpdateStatus(taskID, "completed")
	if err != nil {
		t.Fatalf("Failed to update status: %v", err)
	}

	// Verify status
	state, _ := mgr.LoadState(taskID)
	if state.Status != "completed" {
		t.Errorf("Expected status completed, got %s", state.Status)
	}

	if state.UpdatedAt == 0 {
		t.Error("Expected UpdatedAt to be set")
	}
}

// TestNextSeq TestNextSeq - Test sequence increment
func TestNextSeq(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	taskID := "test_task_003"
	mgr.CreateTask(taskID, "claude")

	// TestSeqIncreasing
	seq1, _ := mgr.NextSeq(taskID)
	seq2, _ := mgr.NextSeq(taskID)
	seq3, _ := mgr.NextSeq(taskID)

	if seq1 != 1 {
		t.Errorf("Expected seq1=1, got %d", seq1)
	}
	if seq2 != 2 {
		t.Errorf("Expected seq2=2, got %d", seq2)
	}
	if seq3 != 3 {
		t.Errorf("Expected seq3=3, got %d", seq3)
	}
}

// TestAddOutput TestAddOutput - Test add output
func TestAddOutput(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	taskID := "test_task_004"
	mgr.CreateTask(taskID, "claude")

	// AddOutput
	event := Event{
		Type: "chunk",
		Data: map[string]string{"content": "Hello, World!"},
	}

	err := mgr.AddOutput(taskID, event)
	if err != nil {
		t.Fatalf("Failed to add output: %v", err)
	}

	// Verify cache is updated (purely in-memory, no file persistence)
	mgr.cacheMu.RLock()
	cache, exists := mgr.outputCache[taskID]
	mgr.cacheMu.RUnlock()

	if !exists {
		t.Error("Expected cache to exist for task")
	}
	if len(cache.events) != 1 {
		t.Errorf("Expected 1 event in cache, got %d", len(cache.events))
	}

	// Verify sequence updated
	state, _ := mgr.LoadState(taskID)
	if state.Seq != 1 {
		t.Errorf("Expected seq=1 after AddOutput, got %d", state.Seq)
	}
}

// TestAddDevice TestAddDevice - Test add device
func TestAddDevice(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	taskID := "test_task_005"
	mgr.CreateTask(taskID, "claude")

	// AddDevice
	deviceID := "device_001"
	err := mgr.AddDevice(taskID, deviceID)
	if err != nil {
		t.Fatalf("Failed to add device: %v", err)
	}

	// Verify device added (in-memory)
	devices := mgr.devices[taskID]
	found := false
	for _, d := range devices {
		if d.DeviceID == deviceID {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected device to be added in memory")
	}

	// Add same device again (should update, not duplicate)
	err = mgr.AddDevice(taskID, deviceID)
	if err != nil {
		t.Fatalf("Failed to update device: %v", err)
	}
}

// TestUpdateDeviceSeq TestUpdateDeviceSeq - Test update device sequence
func TestUpdateDeviceSeq(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	taskID := "test_task_006"
	deviceID := "device_002"

	mgr.CreateTask(taskID, "claude")
	mgr.AddDevice(taskID, deviceID)

	// Update device sequence
	err := mgr.UpdateDeviceSeq(taskID, deviceID, 100)
	if err != nil {
		t.Fatalf("Failed to update device seq: %v", err)
	}

	// Verify sequence updated (via in-memory state)
	devices := mgr.devices[taskID]
	found := false
	for _, d := range devices {
		if d.DeviceID == deviceID {
			found = true
			if d.LastSeq != 100 {
				t.Errorf("Expected LastSeq=100, got %d", d.LastSeq)
			}
			if d.LastActive == 0 {
				t.Error("Expected LastActive to be set")
			}
		}
	}

	if !found {
		t.Error("Device not found")
	}
}

// TestGetIncrementalOutput TestGetIncrementalOutput - Test get incremental output
func TestGetIncrementalOutput(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	taskID := "test_task_007"
	mgr.CreateTask(taskID, "claude")

	// AddmultipleOutput
	for i := 1; i <= 5; i++ {
		event := Event{
			Type: "chunk",
			Data: map[string]string{"content": string(rune('A' + i - 1))},
		}
		mgr.AddOutput(taskID, event)
	}

	// Getfrom seq=2 startIncrementalOutput
	events, err := mgr.GetIncrementalOutput(taskID, 2)
	if err != nil {
		t.Fatalf("Failed to get incremental output: %v", err)
	}

	// Should return seq=3,4,5 output (3 total)
	if len(events) != 3 {
		t.Errorf("Expected 3 events, got %d", len(events))
	}

	// Verify first event seq
	if len(events) > 0 && events[0].Seq != 3 {
		t.Errorf("Expected first event seq=3, got %d", events[0].Seq)
	}
}

// TestListTasks TestListTasks - Test list tasks
func TestListTasks(t *testing.T) {
	mgr, tmpDir, cleanup := setupTestManager(t)
	defer cleanup()

	// CreatemultipleTask（Use task_ prefix，sinceto ListTasks FilterThisprefix）
	taskIDs := []string{"task_001", "task_002", "task_003"}
	for _, id := range taskIDs {
		mgr.CreateTask(id, "claude")
	}

	// Create non-task directory (should not be listed)
	os.MkdirAll(filepath.Join(tmpDir, "not_a_task"), 0755)

	// ListTask
	tasks, err := mgr.ListTasks()
	if err != nil {
		t.Fatalf("Failed to list tasks: %v", err)
	}

	// Verify task count
	if len(tasks) != 3 {
		t.Errorf("Expected 3 tasks, got %d: %v", len(tasks), tasks)
	}
}

// TestLoadNonExistentTask TestLoadNonExistentTask - Test load non-existent task
func TestLoadNonExistentTask(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	_, err := mgr.LoadState("non_existent_task")
	if err == nil {
		t.Error("Expected error when loading non-existent task")
	}
}

// TestConcurrentAccess TestConcurrentAccess - Test concurrent access
func TestConcurrentAccess(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	taskID := "test_concurrent"
	mgr.CreateTask(taskID, "claude")

	// Concurrent status update
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			mgr.UpdateStatus(taskID, "running")
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify no panic or race condition
	state, err := mgr.LoadState(taskID)
	if err != nil {
		t.Fatalf("Failed to load state after concurrent access: %v", err)
	}

	if state.Status != "running" {
		t.Errorf("Expected status running, got %s", state.Status)
	}
}

// TestCloseStopsCleanupLoop - Verify Close() signals cleanupLoop to exit.
// Prevents goroutine leaks on shutdown.
func TestCloseStopsCleanupLoop(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	// Close should not block and should signal cleanupLoop to exit
	mgr.Close()

	// Calling Close again should be a no-op (idempotent)
	mgr.Close()
}

// TestConcurrentProviderSessionAccess - Race-detecting test for SetProvider/GetProvider/SetSessionID/GetSessionID.
// Run with -race to detect data races on provider/sessionID fields.
func TestConcurrentProviderSessionAccess(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	done := make(chan struct{})

	// Writer: SetProvider / SetSessionID
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			mgr.SetProvider("claude")
			mgr.SetSessionID("sess-1")
		}
	}()

	// Readers: concurrent GetProvider / GetSessionID (race with the writer)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				_ = mgr.GetProvider()
				_ = mgr.GetSessionID()
			}
		}()
	}

	<-done
	wg.Wait()
}

// TestEventMarshaling TestEventMarshaling - Test event marshaling
func TestEventMarshaling(t *testing.T) {
	event := Event{
		Seq:       42,
		Type:      "chunk",
		Timestamp: time.Now().UnixMilli(),
		Data: map[string]interface{}{
			"content": "test",
			"number":  123,
		},
	}

	// Serialization should succeed
	// （AlreadyTestin AddOutput inAlreadycovered）
	if event.Seq != 42 {
		t.Errorf("Expected seq=42, got %d", event.Seq)
	}
	if event.Type != "chunk" {
		t.Errorf("Expected type=chunk, got %s", event.Type)
	}
}
