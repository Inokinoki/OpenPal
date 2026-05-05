package server

import (
	"encoding/json"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// memStatsCache - Cached memory statistics to reduce ReadMemStats calls
// ReadMemStats is expensive (~1-2ms), so we cache it for health checks
var memStatsCache struct {
	stats     runtime.MemStats
	updatedAt int64 // Unix nanoseconds
	mu        sync.RWMutex
}

// memStatsCacheTTL - Cache TTL for memory stats (1 second)
const memStatsCacheTTL = int64(time.Second)

// getMemStatsCached - Get memory stats with caching
// Optimized: reduces ReadMemStats calls from O(requests) to O(1/sec)
func getMemStatsCached() runtime.MemStats {
	now := time.Now().UnixNano()

	// Fast path: check cache with read lock
	memStatsCache.mu.RLock()
	if now-memStatsCache.updatedAt < memStatsCacheTTL {
		stats := memStatsCache.stats
		memStatsCache.mu.RUnlock()
		return stats
	}
	memStatsCache.mu.RUnlock()

	// Cache miss: acquire write lock and refresh
	memStatsCache.mu.Lock()
	defer memStatsCache.mu.Unlock()

	// Double-check after acquiring write lock (another goroutine may have updated)
	if now-memStatsCache.updatedAt < memStatsCacheTTL {
		return memStatsCache.stats
	}

	runtime.ReadMemStats(&memStatsCache.stats)
	memStatsCache.updatedAt = now
	return memStatsCache.stats
}

// handleHealth HealthCheckEndpoint
// Optimized: includes cache statistics, memory usage, connection stats, and broadcast rate limit for better observability
// Enhanced: added queue depth, CLI mode, and rate limit status
// Optimized: cached memory stats to reduce ReadMemStats overhead
func (s *WebSocketServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	clientCount := len(s.clients)
	cli := s.cli
	s.mu.RUnlock()

	cliPID := 0
	if cli != nil {
		cliPID = cli.Pid
	}

	response := map[string]interface{}{
		"status":       "healthy",
		"task_id":      s.taskID,
		"client_count": clientCount,
		"uptime_ms":    time.Since(s.startedAt).Milliseconds(),
		"cli_pid":      cliPID,
		"cli_mode":     "",
	}

	// Add CLI mode info
	if s.cliAdapter != nil {
		response["cli_mode"] = string(s.cliAdapter.GetMode())
		response["provider"] = s.cliAdapter.GetProvider()
	}

	// Add input queue depth for monitoring
	response["input_queue_depth"] = len(s.inputQueue)
	response["broadcast_queue_depth"] = len(s.broadcastCh)

	// Add broadcast rate limit status
	if s.broadcastRateLimit > 0 {
		response["broadcast_rate_limit"] = s.broadcastRateLimit
		lastBroadcast := atomic.LoadInt64(&s.lastBroadcast)
		if lastBroadcast > 0 {
			response["last_broadcast_ms"] = lastBroadcast / 1000000 // Convert to ms for readability
		}
	}

	// Add connection statistics for observability
	response["connection_stats"] = map[string]interface{}{
		"total_connections":        atomic.LoadInt64(&s.stats.totalConnections),
		"total_disconnections":     atomic.LoadInt64(&s.stats.totalDisconnections),
		"peak_connections":         atomic.LoadInt64(&s.stats.peakConnections),
		"current_connections":      atomic.LoadInt64(&s.stats.currentConnections),
		"rate_limited_connections": atomic.LoadInt64(&s.stats.rateLimitedConnections),
		"broadcast_dropped":        atomic.LoadInt64(&s.stats.broadcastDropped),
		"input_dropped":            atomic.LoadInt64(&s.stats.inputDropped),
		"max_clients":              s.maxClients,
	}

	// Add cache statistics for observability
	if s.stateMgr != nil {
		response["cache_stats"] = s.stateMgr.GetCacheStats()
	}

	// Add memory stats (cached to reduce ReadMemStats overhead)
	memStats := getMemStatsCached()
	response["memory"] = map[string]interface{}{
		"alloc_mb":       memStats.Alloc / 1024 / 1024,
		"sys_mb":         memStats.Sys / 1024 / 1024,
		"num_gc":         memStats.NumGC,
		"pause_total_ms": memStats.PauseTotalNs / 1000000,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// metricsBuilderPool - Pool for reusable strings.Builder in handleMetrics
// Optimized: reduces allocations in metrics endpoint (called frequently by monitoring systems)
var metricsBuilderPool = sync.Pool{
	New: func() interface{} {
		b := new(strings.Builder)
		b.Grow(4096) // Pre-allocate for typical metrics response
		return b
	},
}

// metricsNumBufPool - Pool for reusable number format buffers in handleMetrics
// Optimized: reduces allocations when formatting numbers for Prometheus metrics
var metricsNumBufPool = sync.Pool{
	New: func() interface{} {
		return make([]byte, 0, 32)
	},
}

// metricsHeadersStatic - Pre-built HELP+TYPE headers as single concatenated string
// Optimized 2026-02-24: Eliminates map lookup overhead entirely, single string write
// All metric headers are static - concatenate once at startup, write as single block
const metricsHeadersStatic = `# HELP openpal_uptime_seconds Server uptime in seconds
# TYPE openpal_uptime_seconds gauge
# HELP openpal_info Server information
# TYPE openpal_info gauge
# HELP openpal_connections_current Current number of connected clients
# TYPE openpal_connections_current gauge
# HELP openpal_connections_total Total number of connections since start
# TYPE openpal_connections_total counter
# HELP openpal_disconnections_total Total number of disconnections since start
# TYPE openpal_disconnections_total counter
# HELP openpal_connections_peak Peak number of concurrent connections
# TYPE openpal_connections_peak gauge
# HELP openpal_memory_alloc_bytes Current memory allocation in bytes
# TYPE openpal_memory_alloc_bytes gauge
# HELP openpal_memory_sys_bytes Total memory in bytes
# TYPE openpal_memory_sys_bytes gauge
# HELP openpal_gc_num Total number of GC cycles
# TYPE openpal_gc_num counter
# HELP openpal_gc_pause_total_seconds Total GC pause time in seconds
# TYPE openpal_gc_pause_total_seconds counter
# HELP openpal_cache_hits_total Total cache hits
# TYPE openpal_cache_hits_total counter
# HELP openpal_cache_misses_total Total cache misses
# TYPE openpal_cache_misses_total counter
# HELP openpal_cache_evictions_total Total cache evictions
# TYPE openpal_cache_evictions_total counter
# HELP openpal_cache_hit_rate Cache hit rate (0-100)
# TYPE openpal_cache_hit_rate gauge
# HELP openpal_cache_updates_total Total cache update operations
# TYPE openpal_cache_updates_total counter
# HELP openpal_cache_size Current number of cached tasks
# TYPE openpal_cache_size gauge
# HELP openpal_cache_avg_events_per_cache Average events per cached task
# TYPE openpal_cache_avg_events_per_cache gauge
# HELP openpal_cache_memory_estimate_kb Estimated cache memory usage in KB
# TYPE openpal_cache_memory_estimate_kb gauge
# HELP openpal_cache_total_events Total events across all caches
# TYPE openpal_cache_total_events gauge
# HELP openpal_input_queue_depth Current input queue depth
# TYPE openpal_input_queue_depth gauge
# HELP openpal_broadcast_queue_depth Current broadcast queue depth
# TYPE openpal_broadcast_queue_depth gauge
# HELP openpal_cli_pid CLI process ID (0 if not running)
# TYPE openpal_cli_pid gauge
# HELP openpal_connections_rate_limited_total Total connections rejected due to rate limiting
# TYPE openpal_connections_rate_limited_total counter
# HELP openpal_broadcast_events_dropped_total Total broadcast events dropped due to queue overflow
# TYPE openpal_broadcast_events_dropped_total counter
# HELP openpal_input_messages_dropped_total Total input messages dropped due to queue overflow
# TYPE openpal_input_messages_dropped_total counter
# HELP openpal_max_clients Maximum allowed concurrent clients (0 = unlimited)
# TYPE openpal_max_clients gauge
`

// metricsHeadersDynamic - Pre-built headers for optional/dynamic metrics
// These are written conditionally based on configuration
const (
	metricsHeadersBroadcastRateLimit = `# HELP openpal_broadcast_rate_limit Broadcast rate limit (events per second)
# TYPE openpal_broadcast_rate_limit gauge
# HELP openpal_last_broadcast_timestamp Last broadcast timestamp (Unix nanoseconds)
# TYPE openpal_last_broadcast_timestamp gauge
`
	metricsHeadersConnectionRateLimit = `# HELP openpal_connection_rate_limit Connection rate limit (connections per second)
# TYPE openpal_connection_rate_limit gauge
`
)

// handleMetrics - Prometheus-style metrics endpoint
// Optimized: sync.Pool for Builder/buffers, strconv instead of fmt.Fprintf, pre-built headers
func (s *WebSocketServer) handleMetrics(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	cli := s.cli
	s.mu.RUnlock()

	cliPID := 0
	cliMode := ""
	provider := ""
	if cli != nil {
		cliPID = cli.Pid
	}
	if s.cliAdapter != nil {
		cliMode = string(s.cliAdapter.GetMode())
		provider = s.cliAdapter.GetProvider()
	}

	memStats := getMemStatsCached()

	// Get cache stats (GetCacheStats returns consistent types)
	var cacheHits, cacheMisses, cacheEvictions, cacheUpdates, cacheSize int64
	var cacheHitRate, avgEventsPerCache float64
	var memoryEstimateKB, totalEvents int
	if s.stateMgr != nil {
		stats := s.stateMgr.GetCacheStats()
		cacheHits, _ = stats["hits"].(int64)
		cacheMisses, _ = stats["misses"].(int64)
		cacheEvictions, _ = stats["evictions"].(int64)
		cacheUpdates, _ = stats["updates"].(int64)
		cacheSize, _ = stats["size"].(int64)
		cacheHitRate, _ = stats["hit_rate"].(float64)
		avgEventsPerCache, _ = stats["avg_events_per_cache"].(float64)
		memoryEstimateKB, _ = stats["memory_estimate_kb"].(int)
		totalEvents, _ = stats["total_events"].(int)
	}

	sb := metricsBuilderPool.Get().(*strings.Builder)
	sb.Reset()
	defer metricsBuilderPool.Put(sb)

	numBuf := metricsNumBufPool.Get().([]byte)
	defer metricsNumBufPool.Put(numBuf)

	uptimeSecs := time.Since(s.startedAt).Seconds()
	currentConns := atomic.LoadInt64(&s.stats.currentConnections)
	totalConns := atomic.LoadInt64(&s.stats.totalConnections)
	totalDisconns := atomic.LoadInt64(&s.stats.totalDisconnections)
	peakConns := atomic.LoadInt64(&s.stats.peakConnections)
	gcPauseSecs := float64(memStats.PauseTotalNs) / 1e9
	inputQueueDepth := len(s.inputQueue)

	writeFloat := func(name string, value float64) {
		sb.WriteString(name)
		sb.WriteByte(' ')
		numBuf = numBuf[:0]
		sb.Write(strconv.AppendFloat(numBuf, value, 'f', -1, 64))
		sb.WriteByte('\n')
	}

	writeInt := func(name string, value int64) {
		sb.WriteString(name)
		sb.WriteByte(' ')
		numBuf = numBuf[:0]
		sb.Write(strconv.AppendInt(numBuf, value, 10))
		sb.WriteByte('\n')
	}

	writeInfo := func(name string, value float64) {
		sb.WriteString(name)
		sb.WriteString("{task_id=\"")
		sb.WriteString(s.taskID)
		sb.WriteString("\",provider=\"")
		sb.WriteString(provider)
		sb.WriteString("\",mode=\"")
		sb.WriteString(cliMode)
		sb.WriteString("\"} ")
		numBuf = numBuf[:0]
		sb.Write(strconv.AppendFloat(numBuf, value, 'f', -1, 64))
		sb.WriteByte('\n')
	}

	// Server metrics (static headers - single string write)
	sb.WriteString(metricsHeadersStatic)
	writeFloat("openpal_uptime_seconds", uptimeSecs)
	writeInfo("openpal_info", 1)

	// Connection metrics
	writeInt("openpal_connections_current", currentConns)
	writeInt("openpal_connections_total", totalConns)
	writeInt("openpal_disconnections_total", totalDisconns)
	writeInt("openpal_connections_peak", peakConns)

	// Memory metrics
	writeInt("openpal_memory_alloc_bytes", int64(memStats.Alloc))
	writeInt("openpal_memory_sys_bytes", int64(memStats.Sys))
	writeInt("openpal_gc_num", int64(memStats.NumGC))
	writeFloat("openpal_gc_pause_total_seconds", gcPauseSecs)

	// Cache metrics
	writeInt("openpal_cache_hits_total", cacheHits)
	writeInt("openpal_cache_misses_total", cacheMisses)
	writeInt("openpal_cache_evictions_total", cacheEvictions)
	writeFloat("openpal_cache_hit_rate", cacheHitRate)
	writeInt("openpal_cache_updates_total", cacheUpdates)
	writeInt("openpal_cache_size", cacheSize)
	writeFloat("openpal_cache_avg_events_per_cache", avgEventsPerCache)
	writeInt("openpal_cache_memory_estimate_kb", int64(memoryEstimateKB))
	writeInt("openpal_cache_total_events", int64(totalEvents))

	// Queue metrics
	writeInt("openpal_input_queue_depth", int64(inputQueueDepth))
	writeInt("openpal_broadcast_queue_depth", int64(len(s.broadcastCh)))

	// Queue overflow metrics (dropped events/messages)
	writeInt("openpal_broadcast_events_dropped_total", atomic.LoadInt64(&s.stats.broadcastDropped))
	writeInt("openpal_input_messages_dropped_total", atomic.LoadInt64(&s.stats.inputDropped))

	// Max clients limit
	writeInt("openpal_max_clients", s.maxClients)

	// Broadcast rate limit metrics (conditional)
	if s.broadcastRateLimit > 0 {
		sb.WriteString(metricsHeadersBroadcastRateLimit)
		writeInt("openpal_broadcast_rate_limit", s.broadcastRateLimit)
		lastBroadcast := atomic.LoadInt64(&s.lastBroadcast)
		if lastBroadcast > 0 {
			writeInt("openpal_last_broadcast_timestamp", lastBroadcast)
		}
	}

	// Connection rate limit metrics (always expose rate_limited counter)
	rateLimited := atomic.LoadInt64(&s.stats.rateLimitedConnections)
	writeInt("openpal_connections_rate_limited_total", rateLimited)
	if s.connRateLimit.enabled {
		sb.WriteString(metricsHeadersConnectionRateLimit)
		writeInt("openpal_connection_rate_limit", s.connRateLimit.maxPerSecond)
	}

	// CLI metrics
	writeInt("openpal_cli_pid", int64(cliPID))

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	w.Write([]byte(sb.String()))
}
