package server

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"openpal/internal/adapter"
	"openpal/internal/state"
	"openpal/internal/util"
)

const (
	// Reconnect configuration
	MaxReconnectAttempts = 5
	ReconnectDelayBase   = 1 * time.Second
	ReconnectDelayMax    = 30 * time.Second

	// WebSocket Config
	WriteTimeout      = 10 * time.Second
	PongTimeout       = 60 * time.Second
	PingInterval      = 30 * time.Second
	MaxMessageSize    = 4096
	HeartbeatInterval = 10 * time.Second

	// Performance tuning
	defaultClientCapacity = 16 // Default client map capacity
)

// fastRand - Fast PRNG for non-cryptographic random generation (device IDs, etc.)
// Optimized: uses math/rand with mutex protection for thread safety
// Much faster than crypto/rand for non-security-critical use cases
var fastRand = struct {
	mu sync.Mutex
	r  *rand.Rand
}{
	r: rand.New(rand.NewSource(time.Now().UnixNano())),
}

// fastRandByte - Generate a fast random byte for device ID generation
// Optimized: avoids crypto/rand overhead for non-cryptographic use cases
func fastRandByte() byte {
	fastRand.mu.Lock()
	b := byte(fastRand.r.Intn(256))
	fastRand.mu.Unlock()
	return b
}

// ErrorDefinitions
var (
	ErrClientNotFound     = errors.New("client not found")
	ErrConnectionClosed   = errors.New("connection closed")
	ErrWriteFailed        = errors.New("write failed")
	ErrReconnectFailed    = errors.New("reconnect failed")
	ErrMaxAttemptsReached = errors.New("max reconnect attempts reached")
)

// Device Device information (local copy)
type Device struct {
	DeviceID        string `json:"device_id"`
	ConnectedAt     int64  `json:"connected_at"`
	LastActive      int64  `json:"last_active"`
	LastSeq         int64  `json:"last_seq"`
	ReconnectCount  int    `json:"reconnect_count"`
	LastReconnectAt int64  `json:"last_reconnect_at"`
}

// ClientConfig WebSocket ClientConfig
type ClientConfig struct {
	EnableCompression bool
	EnableHeartbeat   bool
	ReconnectEnabled  bool
}

// WebSocketClient WebSocket Client（withReconnectSupport）
type WebSocketClient struct {
	Conn            *websocket.Conn
	DeviceID        string
	ConnectedAt     time.Time
	LastActive      time.Time
	LastSeq         int64
	ReconnectCount  int
	mu              sync.RWMutex
	writeMu         sync.Mutex // Protects WebSocket write operations
	isClosed        atomic.Bool
	reconnectCtx    context.Context
	reconnectCancel context.CancelFunc
}

// IsClosed CheckClientIfAlreadyClose
func (c *WebSocketClient) IsClosed() bool {
	return c.isClosed.Load()
}

// SetClosed MarkClienttoCloseState
func (c *WebSocketClient) SetClosed() {
	c.isClosed.Store(true)
	c.mu.Lock()
	cancel := c.reconnectCancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// UpdateActivity Update activity time
func (c *WebSocketClient) UpdateActivity() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.LastActive = time.Now()
}

// IncrementReconnect Increment reconnect count
func (c *WebSocketClient) IncrementReconnect() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ReconnectCount++
}

// GetReconnectCount Get reconnect count
func (c *WebSocketClient) GetReconnectCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ReconnectCount
}

var upgrader = websocket.Upgrader{
	CheckOrigin:       func(r *http.Request) bool { return true },
	EnableCompression: true,
	ReadBufferSize:    1024,
	WriteBufferSize:   1024,
}

// InputMessage - Message to send to CLI
type InputMessage struct {
	Content string
	Type    string // "task" or "input"
}

// connectionStats - Statistics for connection tracking
type connectionStats struct {
	totalConnections       int64 // Total connections since start
	totalDisconnections    int64 // Total disconnections since start
	peakConnections        int64 // Peak concurrent connections
	currentConnections     int64 // Current active connections (atomic)
	lastConnectionTime     int64 // Last connection timestamp (nanoseconds, for rate limiting)
	rateLimitedConnections int64 // Count of connections rejected due to rate limiting (atomic)
	broadcastDropped       int64 // Count of broadcast events dropped due to queue overflow (atomic)
	inputDropped           int64 // Count of input messages dropped due to queue overflow (atomic)
}

// connectionRateLimit - Connection rate limiting configuration
type connectionRateLimit struct {
	enabled       bool
	maxPerSecond  int64
	minIntervalNs int64
}

// WebSocketServer WebSocket server
type WebSocketServer struct {
	stateMgr           *state.Manager
	taskID             string
	cli                *adapter.CLIProcess
	cliAdapter         *adapter.Manager // CLI adapter for starting CLI on demand
	clients            map[string]*WebSocketClient
	mu                 sync.RWMutex
	broadcastCh        chan state.Event
	errorCh            chan error
	config             ClientConfig
	ctx                context.Context
	cancel             context.CancelFunc
	wg                 sync.WaitGroup
	cliStarted         atomic.Bool         // Track if CLI has been started (atomic for thread safety)
	inputQueue         chan InputMessage   // Queue for input messages
	sessionDir         string              // Directory for session files
	startedAt          time.Time           // Server start time for uptime calculation
	stats              connectionStats     // Connection statistics
	broadcastRateLimit int64               // Max broadcasts per second (0 = disabled)
	lastBroadcast      int64               // Last broadcast timestamp (atomic, Unix nanoseconds)
	connRateLimit      connectionRateLimit // Connection rate limiting
	maxClients         int64               // Maximum concurrent clients (0 = unlimited)
	stopOnce           sync.Once           // Guard against double-close of channels
}

// ClientMessage Client message
type ClientMessage struct {
	Command string                 `json:"command"`
	Data    map[string]interface{} `json:"data"`
}

// NewWebSocketServer Create WebSocket server (CLI not started yet)
func NewWebSocketServer(stateMgr *state.Manager, taskID string, cliAdapter *adapter.Manager, sessionDir string) *WebSocketServer {
	ctx, cancel := context.WithCancel(context.Background())

	s := &WebSocketServer{
		stateMgr:    stateMgr,
		taskID:      taskID,
		cliAdapter:  cliAdapter,
		clients:     make(map[string]*WebSocketClient, defaultClientCapacity),
		broadcastCh: make(chan state.Event, 100),
		errorCh:     make(chan error, 10),
		inputQueue:  make(chan InputMessage, 100), // Buffered queue
		sessionDir:  sessionDir,
		startedAt:   time.Now(),
		config: ClientConfig{
			EnableCompression: true,
			EnableHeartbeat:   true,
			ReconnectEnabled:  true,
		},
		ctx:                ctx,
		cancel:             cancel,
		broadcastRateLimit: 0, // Disabled by default
	}
	// cliStarted defaults to false (atomic.Bool zero value)
	return s
}

// SetBroadcastRateLimit - Set broadcast rate limit (events per second, 0 = disabled)
func (s *WebSocketServer) SetBroadcastRateLimit(limit int64) {
	s.broadcastRateLimit = limit
}

// SetConnectionRateLimit - Set connection rate limit (connections per second, 0 = disabled)
// Protects against connection flood attacks
func (s *WebSocketServer) SetConnectionRateLimit(limit int64) {
	if limit <= 0 {
		s.connRateLimit.enabled = false
		return
	}
	s.connRateLimit.enabled = true
	s.connRateLimit.maxPerSecond = limit
	s.connRateLimit.minIntervalNs = int64(time.Second) / limit
}

// SetMaxClients - Set maximum concurrent clients (0 = unlimited)
// Protects against memory exhaustion from too many connections
func (s *WebSocketServer) SetMaxClients(limit int64) {
	s.maxClients = limit
}

// checkMaxClients - Check if new connection is allowed under max clients limit
// Returns true if allowed, false if limit exceeded
func (s *WebSocketServer) checkMaxClients() bool {
	if s.maxClients <= 0 {
		return true // No limit
	}
	current := atomic.LoadInt64(&s.stats.currentConnections)
	return current < s.maxClients
}

// checkConnectionRateLimit - Check if new connection is allowed under rate limit
// Returns true if allowed, false if rate limited
// Enhanced: tracks rate-limited connections for observability, also checks max clients limit
func (s *WebSocketServer) checkConnectionRateLimit() bool {
	// Check max clients first (faster check, no atomic operation needed if limit disabled)
	if !s.checkMaxClients() {
		return false
	}

	if !s.connRateLimit.enabled {
		return true // Rate limiting disabled
	}

	now := time.Now().UnixNano()
	lastConn := atomic.LoadInt64(&s.stats.lastConnectionTime)

	// Fast path: check if within rate limit window
	if now-lastConn < s.connRateLimit.minIntervalNs {
		// Track rate-limited connection for observability
		atomic.AddInt64(&s.stats.rateLimitedConnections, 1)
		return false // Rate limited
	}

	// Try to claim this time slot with CAS
	if atomic.CompareAndSwapInt64(&s.stats.lastConnectionTime, lastConn, now) {
		return true
	}

	// CAS failed = rate limited
	atomic.AddInt64(&s.stats.rateLimitedConnections, 1)
	return false
}

// Broadcast rate limit logic is inlined in broadcast() for zero function call overhead

// Start Start server
func (s *WebSocketServer) Start(addr string) (int, error) {
	http.HandleFunc("/ws", s.handleWebSocket)
	http.HandleFunc("/health", s.handleHealth)   // HealthCheckEndpoint
	http.HandleFunc("/metrics", s.handleMetrics) // Prometheus-style metrics

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return 0, fmt.Errorf("failed to listen: %w", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port

	go func() {
		util.DebugLog("[DEBUG] WebSocket server starting on port %d", port)
		if err := http.Serve(listener, nil); err != nil && err != http.ErrServerClosed {
			s.errorCh <- fmt.Errorf("WebSocket server error: %w", err)
		}
	}()

	// StartBroadcastHandleer
	s.wg.Add(1)
	go s.broadcastHandler()

	// StartErrorHandleer
	s.wg.Add(1)
	go s.errorHandler()

	// StartHeartbeat checker
	if s.config.EnableHeartbeat {
		s.wg.Add(1)
		go s.heartbeatChecker()
	}

	return port, nil
}

// Stop StopServer (graceful shutdown with proper cleanup order)
// Optimized: parallel client cleanup, simplified channel handling, better timeout management
func (s *WebSocketServer) Stop() error {
	util.DebugLog("[DEBUG] Stopping WebSocket server...")

	// 1. Cancel context to signal all goroutines
	s.cancel()

	// 2. Close input queue
	close(s.inputQueue)

	// 3. Close all client connections in parallel
	s.mu.Lock()
	clientsToClose := make([]*WebSocketClient, 0, len(s.clients))
	for deviceID, client := range s.clients {
		clientsToClose = append(clientsToClose, client)
		util.DebugLog("[DEBUG] Disconnecting client: %s", deviceID)
	}
	s.clients = make(map[string]*WebSocketClient)
	s.mu.Unlock()

	// Parallel close (I/O bound)
	var wg sync.WaitGroup
	for _, client := range clientsToClose {
		wg.Add(1)
		go func(c *WebSocketClient) {
			defer wg.Done()
			c.SetClosed()
			if c.Conn != nil {
				c.Conn.Close()
			}
		}(client)
	}
	wg.Wait()

	// 4. Close channels (protected against double-close)
	s.stopOnce.Do(func() {
		select {
		case <-s.broadcastCh:
		default:
			close(s.broadcastCh)
		}
		close(s.errorCh)
	})

	// 5. Stop CLI with timeout
	if s.cli != nil {
		util.DebugLog("[DEBUG] Stopping CLI (PID: %d)...", s.cli.Pid)
		done := make(chan error, 1)
		go func() { done <- s.cli.Stop() }()

		select {
		case err := <-done:
			if err != nil {
				util.DebugLog("[DEBUG] Warning: CLI stop error: %v", err)
			}
		case <-time.After(5 * time.Second):
			util.DebugLog("[DEBUG] Warning: CLI stop timeout")
		}
		s.cli = nil
	}

	// 6. Wait for goroutines with timeout
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()

	select {
	case <-done:
		util.DebugLog("[DEBUG] All goroutines stopped")
	case <-time.After(5 * time.Second):
		util.DebugLog("[DEBUG] Warning: goroutine wait timeout")
	}

	util.DebugLog("[DEBUG] WebSocket server stopped")
	return nil
}

// broadcastHandler Handle broadcast
func (s *WebSocketServer) broadcastHandler() {
	defer s.wg.Done()

	for event := range s.broadcastCh {
		s.broadcastToClients(event)
	}
}

// errorHandler Handle error
// Optimized: direct allocation for error events (infrequent, pool overhead not justified)
// Errors are rare compared to normal events; direct alloc is simpler and equally efficient
func (s *WebSocketServer) errorHandler() {
	defer s.wg.Done()

	for err := range s.errorCh {
		if err != nil {
			util.DebugLog("[DEBUG] Server error: %v", err)

			// Direct allocation (errors are infrequent, pool not needed)
			s.stateMgr.AddOutput(s.taskID, state.Event{
				Type:      "error",
				Timestamp: time.Now().UnixMilli(),
				Data: map[string]interface{}{
					"message": err.Error(),
				},
			})
		}
	}
}

// heartbeatChecker Heartbeat checker
func (s *WebSocketServer) heartbeatChecker() {
	defer s.wg.Done()

	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.checkHeartbeat()
		}
	}
}
