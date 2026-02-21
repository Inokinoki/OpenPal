package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"pal-broker/internal/adapter"
	"pal-broker/internal/state"
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
)

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
	isClosed        bool
	reconnectCtx    context.Context
	reconnectCancel context.CancelFunc
}

// IsClosed CheckClientIfAlreadyClose
func (c *WebSocketClient) IsClosed() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.isClosed
}

// SetClosed MarkClienttoCloseState
func (c *WebSocketClient) SetClosed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.isClosed = true
	if c.reconnectCancel != nil {
		c.reconnectCancel()
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

// WebSocketServer WebSocket server
type WebSocketServer struct {
	stateMgr    *state.Manager
	taskID      string
	cli         *adapter.CLIProcess
	clients     map[string]*WebSocketClient
	mu          sync.RWMutex
	broadcastCh chan state.Event
	errorCh     chan error
	historyFile *os.File // File for saving CLI interaction history
	config      ClientConfig
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
}

// ClientMessage Client message
type ClientMessage struct {
	Command string                 `json:"command"`
	Data    map[string]interface{} `json:"data"`
}

// ServerEvent ServerEvent
type ServerEvent struct {
	state.Event
	DeviceID string `json:"device_id,omitempty"`
}

// NewWebSocketServer Create WebSocket server
func NewWebSocketServer(stateMgr *state.Manager, taskID string, cli *adapter.CLIProcess, saveHistory bool, sessionDir string) *WebSocketServer {
	ctx, cancel := context.WithCancel(context.Background())

	var historyFile *os.File
	if saveHistory {
		// Create history file in session directory
		historyPath := filepath.Join(sessionDir, taskID, "history.log")
		if err := os.MkdirAll(filepath.Dir(historyPath), 0755); err != nil {
			log.Printf("Warning: failed to create history directory: %v", err)
		} else {
			var err error
			historyFile, err = os.OpenFile(historyPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
			if err != nil {
				log.Printf("Warning: failed to create history file: %v", err)
			} else {
				log.Printf("History file created: %s", historyPath)
			}
		}
	}

	return &WebSocketServer{
		stateMgr:    stateMgr,
		taskID:      taskID,
		cli:         cli,
		clients:     make(map[string]*WebSocketClient),
		broadcastCh: make(chan state.Event, 100), // bufferedChannel
		errorCh:     make(chan error, 10),
		historyFile: historyFile,
		config: ClientConfig{
			EnableCompression: true,
			EnableHeartbeat:   true,
			ReconnectEnabled:  true,
		},
		ctx:    ctx,
		cancel: cancel,
	}
}

// Start Start server
func (s *WebSocketServer) Start(addr string) (int, error) {
	http.HandleFunc("/ws", s.handleWebSocket)
	http.HandleFunc("/health", s.handleHealth) // HealthCheckEndpoint

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return 0, fmt.Errorf("failed to listen: %w", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port

	go func() {
		log.Printf("WebSocket server starting on port %d", port)
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

// Stop StopServer
func (s *WebSocketServer) Stop() error {
	log.Println("Stopping WebSocket server...")

	// Cancel context
	s.cancel()

	// CloseAllClientConnected
	s.mu.Lock()
	for deviceID, client := range s.clients {
		client.SetClosed()
		if client.Conn != nil {
			client.Conn.Close()
		}
		log.Printf("Disconnected client: %s", deviceID)
		delete(s.clients, deviceID)
	}
	s.mu.Unlock()

	// Close channels
	close(s.broadcastCh)
	close(s.errorCh)

	// Wait for all goroutines
	s.wg.Wait()

	// Close history file if open
	if s.historyFile != nil {
		s.historyFile.Close()
		log.Printf("History file closed")
	}

	log.Println("WebSocket server stopped")
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
func (s *WebSocketServer) errorHandler() {
	defer s.wg.Done()

	for err := range s.errorCh {
		if err != nil {
			log.Printf("Server error: %v", err)

			// RecordtoState manager
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

// checkHeartbeat CheckClientheartbeat
func (s *WebSocketServer) checkHeartbeat() {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now()
	for deviceID, client := range s.clients {
		// Skip already closed clients
		if client.IsClosed() {
			continue
		}

		client.mu.RLock()
		lastActive := client.LastActive
		client.mu.RUnlock()

		// Disconnect if no activity for 2x heartbeat interval
		if now.Sub(lastActive) > 2*HeartbeatInterval {
			log.Printf("Client %s heartbeat timeout, disconnecting", deviceID)
			// Mark as closed first to stop ping goroutine
			client.SetClosed()
			// Then remove from map
			s.removeClient(deviceID)
		}
	}
}

// ForwardOutput Forward CLI output
func (s *WebSocketServer) ForwardOutput(stdout, stderr io.Reader) {
	s.wg.Add(2)

	// Forward stdout
	go func() {
		defer s.wg.Done()
		s.forwardStream(stdout, "chunk")
	}()

	// Forward stderr
	go func() {
		defer s.wg.Done()
		s.forwardStream(stderr, "error")
	}()
}

func (s *WebSocketServer) forwardStream(reader io.Reader, eventType string) {
	buf := make([]byte, 4096)
	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		n, err := reader.Read(buf)
		if err != nil {
			if err != io.EOF {
				s.errorCh <- fmt.Errorf("read error: %w", err)
			}
			break
		}

		if n == 0 {
			continue
		}

		lines := splitLines(buf[:n])
		for _, line := range lines {
			if len(line) == 0 {
				continue
			}

			// Save to history file if enabled
			if s.historyFile != nil {
				timestamp := time.Now().Format("2006-01-02 15:04:05")
				fmt.Fprintf(s.historyFile, "[%s] [%s] %s\n", timestamp, eventType, string(line))
			}

			// Try to parse JSON
			var data interface{}
			if err := json.Unmarshal(line, &data); err != nil {
				// Non-JSON, treat as text
				data = map[string]interface{}{
					"content": string(line),
				}
			}

			// AddtoState manager
			event := state.Event{
				Type:      eventType,
				Timestamp: time.Now().UnixMilli(),
				Data:      data,
			}

			if err := s.stateMgr.AddOutput(s.taskID, event); err != nil {
				s.errorCh <- fmt.Errorf("failed to add output: %w", err)
			}

			// Sendtobroadcast channel
			select {
			case s.broadcastCh <- event:
			default:
				// Channel full, log error
				s.errorCh <- errors.New("broadcast channel full")
			}
		}
	}
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0

	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}

	if start < len(data) {
		lines = append(lines, data[start:])
	}

	return lines
}

func (s *WebSocketServer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.errorCh <- fmt.Errorf("WebSocket upgrade failed: %w", err)
		return
	}

	// ConfigurationConnected
	conn.SetReadLimit(MaxMessageSize)
	conn.SetWriteDeadline(time.Now().Add(WriteTimeout))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(PongTimeout))
		return nil
	})

	deviceID := r.URL.Query().Get("device")
	if deviceID == "" {
		deviceID = generateDeviceID()
	}

	// CreateClient
	client := &WebSocketClient{
		Conn:        conn,
		DeviceID:    deviceID,
		ConnectedAt: time.Now(),
		LastActive:  time.Now(),
		LastSeq:     0,
	}

	// Register device
	if err := s.stateMgr.AddDevice(s.taskID, deviceID); err != nil {
		s.errorCh <- fmt.Errorf("failed to add device: %w", err)
	}

	// Add client (handles reconnection)
	s.addClient(deviceID, client)

	log.Printf("Device %s connected to task %s", deviceID, s.taskID)

	// Send historical output
	s.sendHistory(conn, deviceID)

	// StartListenAndheartbeat
	s.wg.Add(2)
	go s.listen(client)
	go s.sendPing(client)
}

func (s *WebSocketServer) addClient(deviceID string, client *WebSocketClient) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// IfAlreadyhassame nameClient，firstCloseoldConnected
	if oldClient, exists := s.clients[deviceID]; exists {
		log.Printf("Replacing existing client: %s (reconnect scenario)", deviceID)
		// Mark old client as closed - this will stop its ping goroutine
		oldClient.SetClosed()
		if oldClient.Conn != nil {
			oldClient.Conn.Close()
		}
		// Don't delete from map, just replace - allows seamless reconnection
	}

	s.clients[deviceID] = client
}

func (s *WebSocketServer) removeClient(deviceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if client, exists := s.clients[deviceID]; exists {
		client.SetClosed()
		if client.Conn != nil {
			client.Conn.Close()
		}
		delete(s.clients, deviceID)
	}
}

func (s *WebSocketServer) sendHistory(conn *websocket.Conn, deviceID string) {
	// Get device last read position
	fromSeq := int64(0)

	// Get incremental output
	events, err := s.stateMgr.GetIncrementalOutput(s.taskID, fromSeq)
	if err != nil {
		s.errorCh <- fmt.Errorf("failed to get incremental output: %w", err)
		return
	}

	// Send historical events
	for _, event := range events {
		conn.SetWriteDeadline(time.Now().Add(WriteTimeout))
		if err := conn.WriteJSON(event); err != nil {
			s.errorCh <- fmt.Errorf("failed to send history: %w", err)
			return
		}
	}

	// Send current status
	taskState, err := s.stateMgr.LoadState(s.taskID)
	if err == nil && taskState != nil {
		conn.SetWriteDeadline(time.Now().Add(WriteTimeout))
		conn.WriteJSON(map[string]interface{}{
			"type": "status",
			"data": map[string]string{
				"status": taskState.Status,
			},
		})
	}
}

func (s *WebSocketServer) listen(client *WebSocketClient) {
	defer func() {
		s.wg.Done()
		s.removeClient(client.DeviceID)
		log.Printf("Device %s disconnected", client.DeviceID)
	}()

	lastSeq := int64(0)

	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		client.Conn.SetReadDeadline(time.Now().Add(PongTimeout))
		_, message, err := client.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				s.errorCh <- fmt.Errorf("WebSocket error: %w", err)
			}

			// Mark client as closed immediately to stop ping goroutine
			client.SetClosed()
			log.Printf("[DEBUG] listen: Client %s connection error, marking as closed: %v", client.DeviceID, err)

			// TryReconnect
			if s.config.ReconnectEnabled && client.GetReconnectCount() < MaxReconnectAttempts {
				s.attemptReconnect(client)
			}
			return
		}

		client.UpdateActivity()

		var msg ClientMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			s.errorCh <- fmt.Errorf("failed to parse message: %w", err)
			continue
		}

		// Handle command
		s.handleCommand(msg, client)

		// Update device sequence
		lastSeq++
		if err := s.stateMgr.UpdateDeviceSeq(s.taskID, client.DeviceID, lastSeq); err != nil {
			// Silently ignore sequence update errors
		}
	}
}

func (s *WebSocketServer) sendPing(client *WebSocketClient) {
	defer s.wg.Done()

	ticker := time.NewTicker(PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			if client.IsClosed() {
				return
			}

			// Protect write operations with mutex
			client.writeMu.Lock()
			client.Conn.SetWriteDeadline(time.Now().Add(WriteTimeout))
			err := client.Conn.WriteMessage(websocket.PingMessage, nil)
			client.writeMu.Unlock()

			if err != nil {
				// Mark client as closed to stop further attempts
				client.SetClosed()
				return
			}
		}
	}
}

func (s *WebSocketServer) attemptReconnect(client *WebSocketClient) {
	count := client.GetReconnectCount()
	if count >= MaxReconnectAttempts {
		s.errorCh <- fmt.Errorf("%s: %s", client.DeviceID, ErrMaxAttemptsReached.Error())
		log.Printf("[DEBUG] attemptReconnect: Max attempts reached for %s, removing client", client.DeviceID)
		// Client will be removed by listen() goroutine
		return
	}

	// ExponentialBackoff
	delay := time.Duration(1<<uint(count)) * ReconnectDelayBase
	if delay > ReconnectDelayMax {
		delay = ReconnectDelayMax
	}

	log.Printf("Attempting reconnect for %s in %v (attempt %d/%d)",
		client.DeviceID, delay, count+1, MaxReconnectAttempts)

	client.IncrementReconnect()

	// RecordReconnectTime
	if err := s.stateMgr.AddOutput(s.taskID, state.Event{
		Type:      "reconnect",
		Timestamp: time.Now().UnixMilli(),
		Data: map[string]interface{}{
			"device_id": client.DeviceID,
			"attempt":   count + 1,
			"delay_ms":  delay.Milliseconds(),
		},
	}); err != nil {
		s.errorCh <- fmt.Errorf("failed to record reconnect: %w", err)
	}

	// Note: Client remains in s.clients map to allow reconnection
	// New connection with same deviceID will reuse the client object
	// listen() goroutine will exit, but ping goroutine should have already stopped
}

func (s *WebSocketServer) handleCommand(msg ClientMessage, client *WebSocketClient) {
	switch msg.Command {
	case "heartbeat":
		// Don't log heartbeat to reduce noise
		s.sendToClient(client.DeviceID, map[string]interface{}{
			"type": "heartbeat_ack",
			"data": map[string]interface{}{
				"timestamp": time.Now().UnixMilli(),
			},
		})

	case "start_task":
		// Send task to CLI (if CLI accepts task via stdin)
		if s.cli != nil && s.cli.Stdin != nil {
			if task, ok := msg.Data["task"].(string); ok {
				if _, err := s.cli.Stdin.Write([]byte(task + "\n")); err != nil {
					s.errorCh <- fmt.Errorf("failed to send task: %w", err)
				}
			}
		}

	case "send_input":
		// Send input to CLI
		if s.cli != nil && s.cli.Stdin != nil {
			if content, ok := msg.Data["content"].(string); ok {
				if _, err := s.cli.Stdin.Write([]byte(content + "\n")); err != nil {
					s.errorCh <- fmt.Errorf("failed to send input: %w", err)
				}
			}
		}

	case "cancel":
		// Cancel task
		if s.cli != nil {
			if err := s.cli.Stop(); err != nil {
				s.errorCh <- fmt.Errorf("failed to cancel task: %w", err)
			}
		}

	case "get_status":
		// Get status
		taskState, err := s.stateMgr.LoadState(s.taskID)
		if err == nil && taskState != nil {
			s.sendToClient(client.DeviceID, map[string]interface{}{
				"type": "status",
				"data": map[string]interface{}{
					"status":     taskState.Status,
					"provider":   taskState.Provider,
					"seq":        taskState.Seq,
					"created_at": taskState.CreatedAt,
				},
			})
		}

	case "approve":
		// Approve operation(for AI permission requests)
		if s.cli != nil && s.cli.Stdin != nil {
			if _, err := s.cli.Stdin.Write([]byte("y\n")); err != nil {
				s.errorCh <- fmt.Errorf("failed to send approval: %w", err)
			}
		}

	case "reject":
		// Reject operation
		if s.cli != nil && s.cli.Stdin != nil {
			if _, err := s.cli.Stdin.Write([]byte("n\n")); err != nil {
				s.errorCh <- fmt.Errorf("failed to send rejection: %w", err)
			}
		}

	default:
		// Unknown command, ignore silently
	}
}

func (s *WebSocketServer) broadcastToClients(event state.Event) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for deviceID, client := range s.clients {
		if client.IsClosed() {
			continue
		}

		// Protect write operations with mutex
		client.writeMu.Lock()
		client.Conn.SetWriteDeadline(time.Now().Add(WriteTimeout))
		err := client.Conn.WriteJSON(event)
		client.writeMu.Unlock()

		if err != nil {
			s.errorCh <- fmt.Errorf("failed to broadcast to %s: %w", deviceID, err)
			// Notimmediatelydisconnect，let listen goroutine Handle
		}
	}
}

func (s *WebSocketServer) broadcast(event state.Event) {
	select {
	case s.broadcastCh <- event:
	default:
		s.errorCh <- errors.New("broadcast channel full")
	}
}

func (s *WebSocketServer) sendToClient(deviceID string, data interface{}) {
	s.mu.RLock()
	client, exists := s.clients[deviceID]
	s.mu.RUnlock()

	if !exists {
		s.errorCh <- fmt.Errorf("%s: %w", deviceID, ErrClientNotFound)
		return
	}

	// Protect write operations with mutex
	client.writeMu.Lock()
	defer client.writeMu.Unlock()

	if client.IsClosed() {
		return
	}

	client.Conn.SetWriteDeadline(time.Now().Add(WriteTimeout))
	if err := client.Conn.WriteJSON(data); err != nil {
		s.errorCh <- fmt.Errorf("failed to send to %s: %w", deviceID, err)
	}
}

func (s *WebSocketServer) getClient(deviceID string) *WebSocketClient {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.clients[deviceID]
}

// GetClientCount GetClientCount
func (s *WebSocketServer) GetClientCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.clients)
}

// GetClientInfo GetClientInfo
func (s *WebSocketServer) GetClientInfo(deviceID string) (*Device, error) {
	s.mu.RLock()
	client, exists := s.clients[deviceID]
	s.mu.RUnlock()

	if !exists {
		return nil, ErrClientNotFound
	}

	client.mu.RLock()
	defer client.mu.RUnlock()

	return &Device{
		DeviceID:        client.DeviceID,
		ConnectedAt:     client.ConnectedAt.UnixMilli(),
		LastActive:      client.LastActive.UnixMilli(),
		LastSeq:         client.LastSeq,
		ReconnectCount:  client.ReconnectCount,
		LastReconnectAt: client.LastActive.UnixMilli(),
	}, nil
}

// handleHealth HealthCheckEndpoint
func (s *WebSocketServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	clientCount := len(s.clients)
	s.mu.RUnlock()

	response := map[string]interface{}{
		"status":       "healthy",
		"task_id":      s.taskID,
		"client_count": clientCount,
		"uptime_ms":    time.Since(time.Now()).Milliseconds(), // NeedRecordStartTime
		"cli_pid":      0,
	}

	if s.cli != nil {
		response["cli_pid"] = s.cli.Pid
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func generateDeviceID() string {
	return "device_" + randomString(8)
}

func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[int(randomByte())%len(letters)]
	}
	return string(b)
}

func randomByte() byte {
	// UseSimpleRandomByteGenerate
	// Production environmentShouldUse crypto/rand
	return byte(time.Now().UnixNano() % 256)
}
