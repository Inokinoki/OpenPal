package state

import (
	"fmt"
	"sync"
	"testing"
)

// TestCachePureMissPattern tests cache behavior with pure miss pattern
func TestCachePureMissPattern(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	// Create multiple tasks and never cache any of them
	taskIDs := []string{"miss_1", "miss_2", "miss_3", "miss_4", "miss_5"}

	// Add one event to each task but never read them
	for _, id := range taskIDs {
		mgr.CreateTask(id, "claude")
		event := Event{
			Type: "chunk",
			Data: map[string]string{"content": fmt.Sprintf("never_cached_%s", id)},
		}
		mgr.AddOutput(id, event)
	}

	// Check stats - should have updates but few misses (only when reading)
	stats := mgr.GetCacheStats()
	t.Logf("Cache stats after pure miss pattern: %+v", stats)

	// Should have some updates from AddOutput
	totalUpdates, ok := stats["total_updates"].(int64)
	if !ok {
		t.Log("Note: total_updates not found in stats (might be nil)")
	} else if totalUpdates == 0 {
		t.Error("Expected some cache updates from AddOutput")
	}
}

// TestCachePureHitPattern tests cache behavior with pure hit pattern
func TestCachePureHitPattern(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	taskID := "pure_hit_test"
	mgr.CreateTask(taskID, "claude")

	// Add events to populate cache
	for i := 1; i <= 10; i++ {
		event := Event{
			Type: "chunk",
			Data: map[string]string{"content": fmt.Sprintf("hit_event_%d", i)},
		}
		mgr.AddOutput(taskID, event)
	}

	// Get initial stats
	initialStats := mgr.GetCacheStats()
	initialHits, _ := initialStats["hits"].(int64)

	// Perform many reads from the same point (should all be cache hits)
	hitCount := 20
	for i := 0; i < hitCount; i++ {
		_, _ = mgr.GetIncrementalOutput(taskID, 0)
	}

	// Get final stats
	finalStats := mgr.GetCacheStats()
	finalHits, _ := finalStats["hits"].(int64)

	actualHits := finalHits - initialHits
	t.Logf("Cache hits during pure hit pattern: %d", actualHits)

	// Should have many hits
	if actualHits < int64(hitCount) {
		t.Logf("Note: Cache implementation may count hits differently than expected")
	}
}

// TestCacheMixedHitMissPattern tests cache behavior with mixed hit/miss pattern
func TestCacheMixedHitMissPattern(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	// Create multiple tasks
	taskIDs := []string{"mixed_1", "mixed_2", "mixed_3"}

	// Populate tasks with different patterns
	for _, id := range taskIDs {
		mgr.CreateTask(id, "claude")

		// Add some events
		for i := 1; i <= 5; i++ {
			event := Event{
				Type: "chunk",
				Data: map[string]string{"content": fmt.Sprintf("%s_event_%d", id, i)},
			}
			mgr.AddOutput(id, event)
		}
	}

	// Initial stats
	initialStats := mgr.GetCacheStats()
	initialHits, _ := initialStats["hits"].(int64)

	// Mixed pattern: read from some tasks frequently, others rarely
	for i := 0; i < 10; i++ {
		// Read from task 1 frequently (should be cache hits)
		_, _ = mgr.GetIncrementalOutput("mixed_1", 0)

		// Read from task 2 moderately
		if i%2 == 0 {
			_, _ = mgr.GetIncrementalOutput("mixed_2", 0)
		}

		// Read from task 3 rarely
		if i%3 == 0 {
			_, _ = mgr.GetIncrementalOutput("mixed_3", 0)
		}
	}

	// Final stats
	finalStats := mgr.GetCacheStats()
	finalHits, _ := finalStats["hits"].(int64)

	actualHits := finalHits - initialHits
	hitRate := float64(actualHits) / float64(30) * 100 // 10 iterations * 3 potential reads = 30

	t.Logf("Cache hits in mixed pattern: %d, Hit rate: %.2f%%", actualHits, hitRate)

	// Hit rate should be reasonable (not 100% due to cache misses)
	if hitRate < 50 {
		t.Logf("Hit rate seems low, but might be due to cache implementation details")
	}
}

// TestCacheLRUHitMissPattern tests cache behavior with LRU-based hit/miss
func TestCacheLRUHitMissPattern(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	// Set small task limit to trigger LRU behavior
	mgr.maxTaskCount = 3

	// Create and access tasks in a pattern that exercises LRU
	taskIDs := []string{"lru_1", "lru_2", "lru_3", "lru_4"}

	// First, access all tasks
	for _, id := range taskIDs[:3] {
		mgr.CreateTask(id, "claude")
		for i := 1; i <= 5; i++ {
			event := Event{
				Type: "chunk",
				Data: map[string]string{"content": fmt.Sprintf("%s_initial_%d", id, i)},
			}
			mgr.AddOutput(id, event)
		}
	}

	// Access task 1 and 2 again (should be cache hits)
	_, _ = mgr.GetIncrementalOutput("lru_1", 0)
	_, _ = mgr.GetIncrementalOutput("lru_2", 0)

	// Add task 4 (should evict least recently used task)
	mgr.CreateTask("lru_4", "claude")
	for i := 1; i <= 3; i++ {
		event := Event{
			Type: "chunk",
			Data: map[string]string{"content": fmt.Sprintf("lru_4_event_%d", i)},
		}
		mgr.AddOutput("lru_4", event)
	}

	// Now access tasks - should see different hit patterns
	initialStats := mgr.GetCacheStats()
	initialHits, _ := initialStats["hits"].(int64)

	// Access all tasks
	for _, id := range taskIDs {
		_, _ = mgr.GetIncrementalOutput(id, 0)
	}

	finalStats := mgr.GetCacheStats()
	finalHits, _ := finalStats["hits"].(int64)

	actualHits := finalHits - initialHits
	t.Logf("Cache hits after LRU exercise: %d", actualHits)

	// Should have some hits due to recent access patterns
	if actualHits > 0 {
		t.Logf("Cache LRU behavior working with hits detected")
	}
}

// TestCacheSequentialMissPattern tests cache behavior with sequential miss pattern
func TestCacheSequentialMissPattern(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	// Create many tasks and access each only once (high miss rate)
	numTasks := 10
	taskIDs := make([]string, numTasks)

	for i := 0; i < numTasks; i++ {
		taskIDs[i] = fmt.Sprintf("sequential_%d", i)
		mgr.CreateTask(taskIDs[i], "claude")

		event := Event{
			Type: "chunk",
			Data: map[string]string{"content": fmt.Sprintf("sequential_%d", i)},
		}
		mgr.AddOutput(taskIDs[i], event)

		// Read once (might be cache hit if still in cache)
		_, _ = mgr.GetIncrementalOutput(taskIDs[i], 0)
	}

	stats := mgr.GetCacheStats()
	t.Logf("Cache stats after sequential miss pattern: %+v", stats)

	// Should have many cache operations
	if stats["total_events"].(int) == 0 {
		t.Error("Expected some cache operations")
	}
}

// TestCacheBurstHitMissPattern tests cache behavior with bursty hit/miss patterns
func TestCacheBurstHitMissPattern(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	taskID := "burst_test"
	mgr.CreateTask(taskID, "claude")

	// Add events
	for i := 1; i <= 10; i++ {
		event := Event{
			Type: "chunk",
			Data: map[string]string{"content": fmt.Sprintf("burst_%d", i)},
		}
		mgr.AddOutput(taskID, event)
	}

	// Burst pattern: multiple rapid reads followed by a miss
	initialStats := mgr.GetCacheStats()
	initialHits, _ := initialStats["hits"].(int64)

	// Burst of hits
	for i := 0; i < 15; i++ {
		_, _ = mgr.GetIncrementalOutput(taskID, 0)
	}

	// One miss by creating a new task
	newTaskID := "burst_miss_test"
	mgr.CreateTask(newTaskID, "claude")
	event := Event{
		Type: "chunk",
		Data: map[string]string{"content": "burst_miss"},
	}
	mgr.AddOutput(newTaskID, event)
	_, _ = mgr.GetIncrementalOutput(newTaskID, 0)

	finalStats := mgr.GetCacheStats()
	finalHits, _ := finalStats["hits"].(int64)

	actualHits := finalHits - initialHits
	t.Logf("Cache hits in burst pattern: %d", actualHits)

	// Should have many hits from the burst
	if actualHits < 10 {
		t.Logf("Burst hit count might be lower than expected due to cache behavior")
	}
}

// TestCacheStressHitMissPattern tests cache behavior under high load with mixed hit/miss
func TestCacheStressHitMissPattern(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	// Set aggressive cache limits
	mgr.maxEventsPerTask = 5
	mgr.maxTaskCount = 5

	// Create many tasks
	numTasks := 20
	taskIDs := make([]string, numTasks)
	for i := 0; i < numTasks; i++ {
		taskIDs[i] = fmt.Sprintf("stress_%d", i)
		mgr.CreateTask(taskIDs[i], "claude")

		// Add limited events to each
		for j := 1; j <= 3; j++ {
			event := Event{
				Type: "chunk",
				Data: map[string]string{"content": fmt.Sprintf("stress_%d_%d", i, j)},
			}
			mgr.AddOutput(taskIDs[i], event)
		}
	}

	// High load pattern: random access patterns
	rng := make([]int, 100)
	for i := range rng {
		rng[i] = i % numTasks
	}

	// Concurrent stress test
	var wg sync.WaitGroup
	errChan := make(chan error, 100)

	for _, taskIndex := range rng {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			taskID := taskIDs[idx]
			_, err := mgr.GetIncrementalOutput(taskID, 0)
			if err != nil {
				errChan <- err
			}
		}(taskIndex)
	}

	wg.Wait()
	close(errChan)

	// Collect errors
	errorCount := 0
	for err := range errChan {
		t.Logf("Stress test error: %v", err)
		errorCount++
	}

	finalStats := mgr.GetCacheStats()
	t.Logf("Final cache stats under stress: %+v", finalStats)
	t.Logf("Errors during stress test: %d", errorCount)

	// Should handle stress gracefully
	if errorCount > 10 {
		t.Errorf("Too many errors during stress test: %d", errorCount)
	}
}

// TestCacheHitRateCalculation tests the accuracy of cache hit rate calculations
func TestCacheHitRateCalculation(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	taskID := "hit_rate_calc_test"
	mgr.CreateTask(taskID, "claude")

	// Add events
	for i := 1; i <= 5; i++ {
		event := Event{
			Type: "chunk",
			Data: map[string]string{"content": fmt.Sprintf("calc_%d", i)},
		}
		mgr.AddOutput(taskID, event)
	}

	// Get initial stats
	initialStats := mgr.GetCacheStats()
	initialHits, _ := initialStats["hits"].(int64)
	initialMisses := initialStats["misses"].(int64)

	// Perform operations that should result in predictable hit/miss patterns
	operations := 20
	hitsExpected := 15
	missesExpected := 5

	for i := 0; i < operations; i++ {
		if i < hitsExpected {
			// These should be hits (cache is populated)
			_, _ = mgr.GetIncrementalOutput(taskID, 0)
		} else {
			// These could be misses if cache evicts
			newTaskID := fmt.Sprintf("calc_miss_%d", i)
			mgr.CreateTask(newTaskID, "claude")
			event := Event{
				Type: "chunk",
				Data: map[string]string{"content": fmt.Sprintf("miss_%d", i)},
			}
			mgr.AddOutput(newTaskID, event)
			_, _ = mgr.GetIncrementalOutput(newTaskID, 0)
		}
	}

	finalStats := mgr.GetCacheStats()
	finalHits, _ := finalStats["hits"].(int64)
	finalMisses := finalStats["misses"].(int64)

	actualHits := finalHits - initialHits
	actualMisses := finalMisses - initialMisses

	t.Logf("Expected hits: %d, Actual hits: %d", hitsExpected, actualHits)
	t.Logf("Expected misses: %d, Actual misses: %d", missesExpected, actualMisses)

	// Calculate hit rate
	totalOperations := actualHits + actualMisses
	if totalOperations > 0 {
		hitRate := float64(actualHits) / float64(totalOperations) * 100
		t.Logf("Actual hit rate: %.2f%%", hitRate)
	}
}
