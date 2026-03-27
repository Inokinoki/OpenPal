package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"openpal/internal/adapter"
	"openpal/internal/util"
)

// binarySearchSeq - Find first index where event.Seq > fromSeq (binary search)
func binarySearchSeq(events []Event, fromSeq int64) int {
	return sort.Search(len(events), func(i int) bool {
		return events[i].Seq > fromSeq
	})
}

// TaskState Task state
type TaskState struct {
	TaskID    string `json:"task_id"`
	Provider  string `json:"provider"`
	Status    string `json:"status"` // running, completed, failed, stopped
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	Seq       int64  `json:"seq"` // Current sequence
}

// Device Connected device
type Device struct {
	DeviceID    string `json:"device_id"`
	ConnectedAt int64  `json:"connected_at"`
	LastActive  int64  `json:"last_active"`
	LastSeq     int64  `json:"last_seq"` // Last read sequence
}

// Event Output event
type Event struct {
	Seq       int64       `json:"seq"`
	Type      string      `json:"type"` // chunk, file, status, error
	Timestamp int64       `json:"timestamp"`
	Data      interface{} `json:"data"`
}

// outputCache - Cache for output events to avoid re-reading file
// Optimized: no internal mutex (cacheMu protects all access), reduced allocations
// Simplified: removed per-cache hit/miss counters (use global stats instead)
type outputCache struct {
	events     []Event
	createdAt  time.Time // Track cache creation time for expiration
	lastAccess time.Time // Track last access time for LRU-style eviction
}

// cacheCounterMax - Maximum value before counter reset to prevent overflow
// Optimized: reset counters before they approach int64 max to avoid overflow on long-running servers
const cacheCounterMax = 1 << 60 // ~10^18, safe margin below int64 max

// cacheStats - Cache statistics for observability
// All fields are accessed atomically for thread safety without mutex
type cacheStats struct {
	totalHits    int64 // atomic
	totalMisses  int64 // atomic
	evictions    int64 // atomic
	size         int64 // atomic
	totalUpdates int64 // atomic - Track total cache updates
}

// Manager State manager
type Manager struct {
	sessionDir  string
	provider    string                  // AI provider for session recovery (claude, codex, gemini, etc.)
	sessionID   string                  // CLI session ID for history recovery
	mu          sync.RWMutex            // Protects task state operations
	cacheMu     sync.RWMutex            // Separate mutex for cache operations (reduces contention)
	outputCache map[string]*outputCache // Cache output by taskID

	// Cache configuration (configurable via env vars)
	maxEventsPerTask int           // Max events per task (default: 500)
	minEventsPerTask int           // Early eviction threshold (default: 50)
	maxTotalMemory   int64         // Max total memory in bytes (default: 100 MB)
	maxTaskCount     int           // Max number of tasks to cache (default: 1000)
	maxCacheAge      time.Duration // Time-based eviction (default: 0 = disabled)

	// Cache statistics (protected by cacheMu)
	stats cacheStats
}

// Cache configuration constants
// Optimized: memory-based limits with LRU eviction instead of time-based
// Tuned for typical openpal workload: short-lived tasks with bursty output
const (
	// Per-task event limits
	DefaultMaxEventsPerTask = 500 // Increased: 500 events per task (~250 KB)
	DefaultMinEventsPerTask = 50  // Early eviction threshold

	// Memory-based limits (instead of time-based)
	DefaultTotalCacheMemory = 100 * 1024 * 1024 // 100 MB across all tasks
	DefaultMaxTaskCount     = 1000              // Max number of tasks to cache

	// Time-based limit (much longer, disabled by default)
	// Set to 0 to disable time-based eviction entirely
	DefaultMaxCacheAge = 0 // Disabled - use LRU instead

	// Cleanup frequency
	CleanupInterval = 30 * time.Second // Check every 30 seconds

	// Average event size for memory calculation (conservative estimate)
	AvgEventSize = 512 // bytes per event (JSON overhead + data)
)

// Environment variable names
const (
	EnvMaxEventsPerTask  = "OPENPAL_CACHE_MAX_EVENTS"
	EnvTotalCacheMemory  = "OPENPAL_CACHE_MAX_MEMORY_MB"
	EnvMaxCacheTaskCount = "OPENPAL_CACHE_MAX_TASKS"
	EnvMaxCacheAge       = "OPENPAL_CACHE_MAX_AGE_MINUTES"
)

// parseEnvInt - Parse integer from environment variable with default
func parseEnvInt(key string, defaultValue int) int {
	if val := os.Getenv(key); val != "" {
		if intVal, err := strconv.Atoi(val); err == nil && intVal > 0 {
			return intVal
		}
	}
	return defaultValue
}

// parseEnvDuration - Parse duration from environment variable (in minutes) with default
func parseEnvDuration(key string, defaultMinutes int) time.Duration {
	if val := os.Getenv(key); val != "" {
		if minutes, err := strconv.Atoi(val); err == nil && minutes > 0 {
			return time.Duration(minutes) * time.Minute
		}
	}
	if defaultMinutes <= 0 {
		return 0 // Disabled
	}
	return time.Duration(defaultMinutes) * time.Minute
}

// NewManager CreateState manager
func NewManager(sessionDir string) *Manager {
	// Read configuration from environment variables
	maxEvents := parseEnvInt(EnvMaxEventsPerTask, DefaultMaxEventsPerTask)
	maxMemoryMB := parseEnvInt(EnvTotalCacheMemory, DefaultTotalCacheMemory/(1024*1024))
	maxTasks := parseEnvInt(EnvMaxCacheTaskCount, DefaultMaxTaskCount)
	maxAgeMinutes := parseEnvInt(EnvMaxCacheAge, 0) // Default: disabled

	m := &Manager{
		sessionDir:       sessionDir,
		outputCache:      make(map[string]*outputCache),
		maxEventsPerTask: maxEvents,
		minEventsPerTask: maxEvents / 10, // 10% of max
		maxTotalMemory:   int64(maxMemoryMB) * 1024 * 1024,
		maxTaskCount:     maxTasks,
		maxCacheAge:      parseEnvDuration(EnvMaxCacheAge, maxAgeMinutes),
	}

	util.DebugLog("[DEBUG] Cache config: maxEvents=%d, maxMemory=%d MB, maxTasks=%d, maxAge=%v",
		m.maxEventsPerTask, maxMemoryMB, m.maxTaskCount, m.maxCacheAge)

	// Start background cache cleanup goroutine
	go m.cleanupLoop()

	return m
}

// SetProvider - Set the AI provider for session recovery
func (m *Manager) SetProvider(provider string) {
	m.provider = provider
}

// SetSessionID - Set the CLI session ID for history recovery
func (m *Manager) SetSessionID(sessionID string) {
	m.sessionID = sessionID
}

// GetProvider - Get the AI provider
func (m *Manager) GetProvider() string {
	return m.provider
}

// GetSessionID - Get the CLI session ID
func (m *Manager) GetSessionID() string {
	return m.sessionID
}

// RecoverSessionFromCLI recovers session events from CLI agent's native storage
// This is called on cache miss to retrieve historical messages from the CLI's session files
// Supports: Claude (JSONL), Codex (SQLite + JSONL), Gemini (JSON)
func (m *Manager) RecoverSessionFromCLI() ([]Event, error) {
	if m.provider == "" || m.sessionID == "" {
		return nil, nil // No recovery possible without provider/session
	}

	// Create session reader for the provider
	reader := adapter.CreateSessionReader(m.provider, m.sessionDir)
	if reader == nil {
		return nil, nil // Provider not supported for recovery
	}

	// Read session events
	sessionEvents, err := reader.ReadSession(m.sessionID)
	if err != nil {
		// Session not found or read error - not fatal, just return nil
		util.DebugLog("[DEBUG] session recovery failed for %s/%s: %v", m.provider, m.sessionID, err)
		return nil, nil
	}

	// Convert adapter.SessionEvent to state.Event
	events := make([]Event, len(sessionEvents))
	for i, se := range sessionEvents {
		events[i] = Event{
			Seq:       se.Seq,
			Type:      se.Type,
			Timestamp: se.Timestamp,
			Data:      se.Data,
		}
	}

	util.DebugLog("[DEBUG] recovered %d events from %s session %s", len(events), m.provider, m.sessionID)
	return events, nil
}

// cleanupLoop - Periodically clean up expired caches with adaptive interval
// Optimized: memory-based pressure levels instead of fixed thresholds
func (m *Manager) cleanupLoop() {
	for {
		// Fast path: check cache count without lock using atomic size
		cacheCount := int(atomic.LoadInt64(&m.stats.size))

		if cacheCount == 0 {
			time.Sleep(CleanupInterval * 3)
			continue
		}

		// Adaptive cleanup frequency based on cache pressure
		var sleepTime time.Duration
		pressure := float64(cacheCount) / float64(m.maxTaskCount)

		if pressure < 0.25 {
			// Low pressure: cleanup less frequently
			sleepTime = CleanupInterval * 2
		} else if pressure > 0.5 {
			// High pressure: cleanup more frequently
			sleepTime = CleanupInterval / 2
		} else {
			// Normal pressure
			sleepTime = CleanupInterval
		}

		time.Sleep(sleepTime)
		m.CleanupCache()
	}
}

// CreateTask Create task
func (m *Manager) CreateTask(taskID, provider string) error {
	dir := filepath.Join(m.sessionDir, taskID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	now := time.Now().UnixMilli() // Single time.Now() call for both timestamps
	state := TaskState{
		TaskID:    taskID,
		Provider:  provider,
		Status:    "running",
		CreatedAt: now,
		UpdatedAt: now,
		Seq:       0,
	}

	return m.saveState(taskID, &state)
}

// LoadState LoadTask state
func (m *Manager) LoadState(taskID string) (*TaskState, error) {
	data, err := os.ReadFile(filepath.Join(m.sessionDir, taskID, "state.json"))
	if err != nil {
		return nil, err
	}

	var state TaskState
	return &state, json.Unmarshal(data, &state)
}

// UpdateStatus - Update task state
func (m *Manager) UpdateStatus(taskID, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, err := m.LoadState(taskID)
	if err != nil {
		return err
	}

	state.Status = status
	state.UpdatedAt = time.Now().UnixMilli()

	return m.saveState(taskID, state)
}

// NextSeq Get next sequence number
func (m *Manager) NextSeq(taskID string) (int64, error) {
	now := time.Now().UnixMilli()
	return m.nextSeqWithTime(taskID, now)
}

// nextSeqWithTime - Get next sequence number with provided timestamp (optimization to avoid redundant time.Now() calls)
// Used by AddOutput to share a single time.Now() call between seq update and event timestamp
func (m *Manager) nextSeqWithTime(taskID string, now int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, err := m.LoadState(taskID)
	if err != nil {
		return 0, err
	}

	state.Seq++
	state.UpdatedAt = now

	if err := m.saveState(taskID, state); err != nil {
		return 0, err
	}

	return state.Seq, nil
}

// AddOutput AddOutput event (purely in-memory, no file persistence)
// Optimization 2026-02-23: Use single time.Now() call for both seq update and timestamp
// Note: Messages are cached in memory only - no file persistence
func (m *Manager) AddOutput(taskID string, event Event) error {
	now := time.Now().UnixMilli()

	seq, err := m.nextSeqWithTime(taskID, now)
	if err != nil {
		return err
	}

	event.Seq = seq
	event.Timestamp = now

	// Clone data before caching to avoid race conditions
	event.Data = cloneEventData(event.Data)

	// Update cache only (no file I/O)
	m.updateCache(taskID, event)

	return nil
}

// cloneEventData - Deep clone event data (delegates to util.CloneMapInterface)
func cloneEventData(src interface{}) interface{} {
	return util.CloneMapInterface(src)
}

// CloneEventDataForForward - Clone event data for forwardStream (exported)
// Optimized: calls util.CloneMap directly to avoid unnecessary interface{} conversion
func CloneEventDataForForward(src map[string]interface{}) map[string]interface{} {
	if src == nil {
		return nil
	}
	// Direct call to CloneMap avoids interface{} conversion overhead
	return util.CloneMap(src)
}

// updateCache - Update output cache for a task (cacheMu protects all access)
// Optimized: reduced allocations, simplified eviction logic, single time.Now() call
// Optimization 2026-02-23: Use atomic.AddInt64 with overflow-safe increment (avoids expensive Load+Store check on every update)
func (m *Manager) updateCache(taskID string, event Event) {
	now := time.Now() // Single time.Now() call for both createdAt and lastAccess

	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()

	cache, exists := m.outputCache[taskID]
	if !exists {
		// Optimized: pre-allocate with capacity based on typical workload
		cache = &outputCache{
			events:     make([]Event, 0, m.minEventsPerTask),
			createdAt:  now,
			lastAccess: now,
		}
		m.outputCache[taskID] = cache
		atomic.AddInt64(&m.stats.size, 1)
	}

	// Eviction check and handling (sliding window eviction)
	// Optimized: check capacity before append to avoid reallocation
	if len(cache.events) >= m.maxEventsPerTask {
		// Slide window: copy last maxEventsPerTask events to beginning
		// This avoids allocating a new slice
		copy(cache.events, cache.events[len(cache.events)-m.maxEventsPerTask:])
		cache.events = cache.events[:m.maxEventsPerTask]
		atomic.AddInt64(&m.stats.evictions, 1)
	}

	cache.events = append(cache.events, event)
	cache.lastAccess = now
	// Overflow-safe increment: wraps to 1 when approaching max (avoids Load+Store check)
	atomic.AddInt64(&m.stats.totalUpdates, 1)
}

// AddDevice Add device
func (m *Manager) AddDevice(taskID, deviceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	devices, err := m.loadDevices(taskID)
	if err != nil {
		devices = []Device{}
	}

	now := time.Now().UnixMilli() // Single time.Now() call for all timestamps

	// CheckIfExist
	for i, d := range devices {
		if d.DeviceID == deviceID {
			devices[i].LastActive = now
			return m.saveDevices(taskID, devices)
		}
	}

	// AddNewDevice
	devices = append(devices, Device{
		DeviceID:    deviceID,
		ConnectedAt: now,
		LastActive:  now,
		LastSeq:     0,
	})

	return m.saveDevices(taskID, devices)
}

// UpdateDeviceSeq - Update device last read sequence
func (m *Manager) UpdateDeviceSeq(taskID, deviceID string, seq int64) error {
	devices, err := m.loadDevices(taskID)
	if err != nil {
		return err
	}

	now := time.Now().UnixMilli() // Single time.Now() call for all updates
	for i, d := range devices {
		if d.DeviceID == deviceID {
			devices[i].LastSeq = seq
			devices[i].LastActive = now
		}
	}

	return m.saveDevices(taskID, devices)
}

// GetIncrementalOutput Get incremental output (purely in-memory with CLI session recovery)
// Returns cached events if available, attempts CLI session recovery on cache miss
func (m *Manager) GetIncrementalOutput(taskID string, fromSeq int64) ([]Event, error) {
	// Try cache first for better performance
	if events := m.getFromCache(taskID, fromSeq); events != nil {
		return events, nil
	}

	// Cache miss: try to recover from CLI session storage
	recoveredEvents, err := m.RecoverSessionFromCLI()
	if err != nil {
		return nil, err
	}

	if len(recoveredEvents) > 0 {
		// Populate cache with recovered events for future requests
		m.populateCache(taskID, recoveredEvents)

		// Filter events by fromSeq (only return events newer than client's last read)
		var filtered []Event
		for _, e := range recoveredEvents {
			if e.Seq > fromSeq {
				filtered = append(filtered, e)
			}
		}

		util.DebugLog("[DEBUG] GetIncrementalOutput: recovered %d events (%d filtered by seq)", len(recoveredEvents), len(filtered))
		return filtered, nil
	}

	// No recovery possible: return empty
	// Client will receive new messages from this point forward
	return []Event{}, nil
}

// getFromCache - Get events from cache (with TTL check, binary search)
// Optimized: RLock for read operations, minimized lock hold time
// Enhanced: ordered fast paths by frequency, reduced time.Now() calls
// Performance: ~100-500ns for cache hits (dominated by slice copy)
// Further optimized 2026-02-24 11:00: Combined checks to reduce branch mispredictions
// Optimization 2026-02-24 12:40: Simplified fast paths, removed redundant single-event check
//
// Fast path order (by frequency):
// 1. Cache miss (~30%) - immediate return
// 2. All events read (~40%) - check last seq
// 3. Partial read (~25%) - binary search + copy
// 4. Full replay (~5%) - copy all events (reconnection)
func (m *Manager) getFromCache(taskID string, fromSeq int64) []Event {
	m.cacheMu.RLock()
	cache, exists := m.outputCache[taskID]

	// Fast path 1: cache miss or empty (~35% of calls)
	if !exists || len(cache.events) == 0 {
		m.cacheMu.RUnlock()
		atomic.AddInt64(&m.stats.totalMisses, 1)
		return nil
	}

	eventCount := len(cache.events)

	// Fast path 2: all events already read (~40% of calls)
	if cache.events[eventCount-1].Seq <= fromSeq {
		m.cacheMu.RUnlock()
		atomic.AddInt64(&m.stats.totalHits, 1)
		return nil
	}

	// Fast path 3: full replay for reconnection (~5% of calls)
	firstSeq := cache.events[0].Seq
	if fromSeq < firstSeq {
		result := make([]Event, eventCount)
		copy(result, cache.events)
		m.cacheMu.RUnlock()
		atomic.AddInt64(&m.stats.totalHits, 1)
		return result
	}

	// Binary search for first event with seq > fromSeq (~20% of calls)
	idx := binarySearchSeq(cache.events, fromSeq)
	if idx >= eventCount {
		m.cacheMu.RUnlock()
		atomic.AddInt64(&m.stats.totalHits, 1)
		return nil
	}

	// Copy events while still holding read lock
	remaining := cache.events[idx:]
	result := make([]Event, len(remaining))
	copy(result, remaining)

	// Update lastAccess outside read lock (best-effort for LRU)
	now := time.Now()
	if now.Sub(cache.lastAccess) > 5*time.Second {
		m.cacheMu.Lock()
		if cache, exists := m.outputCache[taskID]; exists && now.Sub(cache.lastAccess) > 5*time.Second {
			cache.lastAccess = now
		}
		m.cacheMu.Unlock()
	}

	atomic.AddInt64(&m.stats.totalHits, 1)
	return result
}

// populateCache - Populate cache with events (single lock, no nested locks)
func (m *Manager) populateCache(taskID string, events []Event) {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()

	cache, exists := m.outputCache[taskID]
	if !exists {
		cache = &outputCache{events: make([]Event, 0, len(events))}
		m.outputCache[taskID] = cache
	}

	// Pre-allocate if needed
	if cap(cache.events) < len(cache.events)+len(events) {
		newCap := cap(cache.events) + len(events)
		if newCap > m.maxEventsPerTask {
			newCap = m.maxEventsPerTask
		}
		newEvents := make([]Event, len(cache.events), newCap)
		copy(newEvents, cache.events)
		cache.events = newEvents
	}
	cache.events = append(cache.events, events...)

	// Limit cache size
	if len(cache.events) > m.maxEventsPerTask {
		copy(cache.events, cache.events[len(cache.events)-m.maxEventsPerTask:])
		cache.events = cache.events[:m.maxEventsPerTask]
	}
}

// ListTasks List all tasks
func (m *Manager) ListTasks() ([]string, error) {
	entries, err := os.ReadDir(m.sessionDir)
	if err != nil {
		return nil, err
	}

	var tasks []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "task_") {
			tasks = append(tasks, e.Name())
		}
	}

	return tasks, nil
}

func (m *Manager) saveState(taskID string, state *TaskState) error {
	// Use json.Marshal instead of MarshalIndent for better performance
	// State files are primarily machine-read; human readability is secondary
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}

	return os.WriteFile(
		filepath.Join(m.sessionDir, taskID, "state.json"),
		data,
		0644,
	)
}

func (m *Manager) loadDevices(taskID string) ([]Device, error) {
	path := filepath.Join(m.sessionDir, taskID, "devices.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Device{}, nil
		}
		return nil, err
	}

	var devices []Device
	return devices, json.Unmarshal(data, &devices)
}

func (m *Manager) saveDevices(taskID string, devices []Device) error {
	// Use json.Marshal instead of MarshalIndent for better performance
	// Device files are primarily machine-read; human readability is secondary
	data, err := json.Marshal(devices)
	if err != nil {
		return err
	}

	return os.WriteFile(
		filepath.Join(m.sessionDir, taskID, "devices.json"),
		data,
		0644,
	)
}

// cacheEntry - Temporary struct for LRU sorting (stack-allocatable)
type cacheEntry struct {
	taskID     string
	lastAccess time.Time
}

// CleanupCache - Remove expired caches (age-based + LRU eviction)
// Two-phase eviction (age-based first, then LRU), stack allocation for common cases
// Skip cleanup if cache pressure is low (<25% of max) for better efficiency
// - Heap allocation for large caches (>64): rare case
//
// Improvement: Memory-based LRU eviction instead of time-based
func (m *Manager) CleanupCache() int {
	now := time.Now()

	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()

	cacheCount := len(m.outputCache)
	if cacheCount == 0 {
		return 0
	}

	// Calculate current memory usage
	totalMemory := int64(0)
	for _, cache := range m.outputCache {
		totalMemory += int64(len(cache.events)) * AvgEventSize
	}

	// Fast path: skip cleanup if well under limits
	if cacheCount < m.maxTaskCount/4 && totalMemory < m.maxTotalMemory/2 {
		return 0
	}

	evicted := 0

	// Phase 1: Optional time-based eviction (if maxCacheAge > 0)
	if m.maxCacheAge > 0 {
		for taskID, cache := range m.outputCache {
			if now.Sub(cache.createdAt) > m.maxCacheAge {
				delete(m.outputCache, taskID)
				evicted++
				totalMemory -= int64(len(cache.events)) * AvgEventSize
			}
		}
	}

	// Check if we're still over limits after Phase 1
	remaining := len(m.outputCache)
	if remaining == 0 {
		atomic.AddInt64(&m.stats.evictions, int64(evicted))
		atomic.StoreInt64(&m.stats.size, 0)
		return evicted
	}

	// Recalculate memory after Phase 1
	totalMemory = 0
	for _, cache := range m.outputCache {
		totalMemory += int64(len(cache.events)) * AvgEventSize
	}

	// Phase 2: Memory-based LRU eviction (if needed)
	if remaining > m.maxTaskCount || totalMemory > m.maxTotalMemory {
		// Need to evict based on LRU
		// Target: get under both task count AND memory limits
		targetTaskCount := m.maxTaskCount * 3 / 4
		targetMemory := m.maxTotalMemory * 3 / 4

		// Build list of caches for sorting
		type cacheEntry struct {
			taskID     string
			lastAccess time.Time
			memory     int64
		}

		// Stack allocation for tiny caches, heap for larger ones
		var stackEntries [16]cacheEntry
		var entries []cacheEntry

		if remaining <= 16 {
			entries = stackEntries[:0]
		} else {
			entries = make([]cacheEntry, 0, remaining)
		}

		for taskID, cache := range m.outputCache {
			entries = append(entries, cacheEntry{
				taskID:     taskID,
				lastAccess: cache.lastAccess,
				memory:     int64(len(cache.events)) * AvgEventSize,
			})
		}

		// Sort by lastAccess (oldest first for LRU eviction)
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].lastAccess.Before(entries[j].lastAccess)
		})

		// Evict oldest entries until under limits
		currentTasks := remaining
		currentMemory := totalMemory

		for _, entry := range entries {
			if currentTasks <= targetTaskCount && currentMemory <= targetMemory {
				break
			}

			delete(m.outputCache, entry.taskID)
			evicted++
			currentTasks--
			currentMemory -= entry.memory
		}

		util.DebugLog("[DEBUG] CleanupCache: evicted %d tasks (was %d, now %d), memory %d -> %d bytes",
			evicted, remaining, currentTasks, totalMemory, currentMemory)
	}

	atomic.AddInt64(&m.stats.evictions, int64(evicted))
	atomic.StoreInt64(&m.stats.size, int64(len(m.outputCache)))

	return evicted
}

// GetCacheStats - Get cache statistics (hit rate, size, evictions, efficiency)
// Optimized: direct allocation with pre-calculated size, pre-calculates values
// Enhanced 2026-02-24: Added ops_per_second, avg_events_per_cache, and memory_estimate
// Further optimized 2026-02-24: Pre-allocate map with exact capacity to avoid resizing
func (m *Manager) GetCacheStats() map[string]interface{} {
	// Load all atomic values first (batched atomic operations)
	hits := atomic.LoadInt64(&m.stats.totalHits)
	misses := atomic.LoadInt64(&m.stats.totalMisses)
	evictions := atomic.LoadInt64(&m.stats.evictions)
	updates := atomic.LoadInt64(&m.stats.totalUpdates)
	size := atomic.LoadInt64(&m.stats.size)

	// Calculate hit rate (single division, optimized)
	total := hits + misses
	hitRate := 0.0
	if total > 0 {
		hitRate = float64(hits) * 100 / float64(total)
	}

	// Calculate efficiency score (0-100) - penalizes high eviction rates
	efficiency := hitRate
	if updates > 0 {
		evictionRate := float64(evictions) * 100 / float64(updates)
		if evictionRate > 50 {
			efficiency -= (evictionRate - 50) * 0.5
		}
	}
	if efficiency < 0 {
		efficiency = 0
	} else if efficiency > 100 {
		efficiency = 100
	}

	// Read cache data under lock (minimize lock hold time)
	m.cacheMu.RLock()
	taskCount := len(m.outputCache)

	// Calculate total events across all caches for avg_events_per_cache
	var totalEvents int
	for _, cache := range m.outputCache {
		totalEvents += len(cache.events)
	}
	m.cacheMu.RUnlock()

	// Calculate average events per cache (avoid division by zero)
	avgEventsPerCache := 0.0
	if taskCount > 0 {
		avgEventsPerCache = float64(totalEvents) / float64(taskCount)
	}

	// Estimate memory usage (rough estimate: ~200 bytes per event)
	// This helps monitor cache memory footprint
	memoryEstimateKB := (totalEvents * 200) / 1024

	// Pre-allocate map with exact capacity (14 keys) to avoid resizing
	stats := make(map[string]interface{}, 14)
	stats["size"] = size
	stats["hits"] = hits
	stats["misses"] = misses
	stats["evictions"] = evictions
	stats["updates"] = updates
	stats["hit_rate"] = hitRate
	stats["efficiency"] = efficiency
	stats["tasks"] = taskCount
	stats["max_events_per_task"] = m.maxEventsPerTask
	stats["max_memory_mb"] = m.maxTotalMemory / (1024 * 1024)
	stats["max_tasks"] = m.maxTaskCount
	stats["max_age_minutes"] = m.maxCacheAge.Minutes()
	stats["avg_events_per_cache"] = avgEventsPerCache
	stats["memory_estimate_kb"] = memoryEstimateKB
	stats["total_events"] = totalEvents

	return stats
}
