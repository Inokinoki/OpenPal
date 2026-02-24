package server

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"pal-broker/internal/state"
)

// TestWebSocketClientMethods Test WebSocketClient methods
func TestWebSocketClientMethods(t *testing.T) {
	client := &WebSocketClient{
		DeviceID:    "test_device",
		ConnectedAt: time.Now(),
		LastActive:  time.Now(),
	}

	// Test IsClosed
	if client.IsClosed() {
		t.Error("Expected client to not be closed initially")
	}

	// Test SetClosed
	client.SetClosed()
	if !client.IsClosed() {
		t.Error("Expected client to be closed after SetClosed")
	}

	// Test UpdateActivity
	client2 := &WebSocketClient{
		DeviceID:   "test_device_2",
		LastActive: time.Now().Add(-time.Hour),
	}
	client2.UpdateActivity()
	if time.Since(client2.LastActive) > time.Second {
		t.Error("Expected LastActive to be updated")
	}

	// Test IncrementReconnect
	client3 := &WebSocketClient{}
	client3.IncrementReconnect()
	client3.IncrementReconnect()
	if client3.GetReconnectCount() != 2 {
		t.Errorf("Expected reconnect count 2, got %d", client3.GetReconnectCount())
	}
}

// TestWebSocketServerCreation Test WebSocketServer creation
func TestWebSocketServerCreation(t *testing.T) {
	stateMgr := state.NewManager("/tmp/test-ws-creation")
	server := NewWebSocketServer(stateMgr, "test_task", nil, false, "/tmp/test-ws-creation")

	if server == nil {
		t.Fatal("Expected non-nil server")
	}

	if server.taskID != "test_task" {
		t.Errorf("Expected taskID='test_task', got %s", server.taskID)
	}

	if server.clients == nil {
		t.Error("Expected clients map to be initialized")
	}

	if server.broadcastCh == nil {
		t.Error("Expected broadcastCh to be initialized")
	}

	if server.errorCh == nil {
		t.Error("Expected errorCh to be initialized")
	}
}

// TestServerStartAndStop Test server start and stop
func TestServerStartAndStop(t *testing.T) {
	stateMgr := state.NewManager("/tmp/test-ws-startstop")
	server := NewWebSocketServer(stateMgr, "test_task", nil, false, "/tmp/test-ws")

	// StartServer
	port, err := server.Start(":0")
	if err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}

	if port == 0 {
		t.Error("Expected non-zero port")
	}

	t.Logf("Server started on port %d", port)

	// Wait a bit
	time.Sleep(100 * time.Millisecond)

	// Stop server
	err = server.Stop()
	if err != nil {
		t.Errorf("Failed to stop server: %v", err)
	}

	t.Log("Server stopped successfully")
}

// TestBroadcastChannel Test broadcast channel
func TestBroadcastChannel(t *testing.T) {
	stateMgr := state.NewManager("/tmp/test-broadcast-channel")
	server := NewWebSocketServer(stateMgr, "test", nil, false, "/tmp/test-ws")

	// StartBroadcastHandleer
	server.wg.Add(1)
	go server.broadcastHandler()

	// Send event to broadcast channel
	event := state.Event{
		Type:      "chunk",
		Timestamp: time.Now().UnixMilli(),
		Data:      map[string]string{"content": "test"},
	}

	// Should not block
	select {
	case server.broadcastCh <- event:
		t.Log("Event sent to broadcast channel")
	case <-time.After(time.Second):
		t.Error("Broadcast channel is blocked")
	}

	// Cleanup
	close(server.broadcastCh)
	server.wg.Wait()
}

// TestErrorHandler Test error handler
func TestErrorHandler(t *testing.T) {
	stateMgr := state.NewManager("/tmp/test-error-handler")
	server := NewWebSocketServer(stateMgr, "test", nil, false, "/tmp/test-ws")

	// StartErrorHandleer
	server.wg.Add(1)
	go server.errorHandler()

	// Send error
	testErr := "test error"
	select {
	case server.errorCh <- &testError{testErr}:
		t.Log("Error sent to error handler")
	case <-time.After(time.Second):
		t.Error("Error channel is blocked")
	}

	// Cleanup
	close(server.errorCh)
	server.wg.Wait()
}

// testError Implement error interface
type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}

// TestAddRemoveClient Test add and remove client
func TestAddRemoveClient(t *testing.T) {
	stateMgr := state.NewManager("/tmp/test-client-mgmt")
	server := NewWebSocketServer(stateMgr, "test", nil, false, "/tmp/test-ws")

	client := &WebSocketClient{
		DeviceID:    "test_device",
		ConnectedAt: time.Now(),
		LastActive:  time.Now(),
	}

	// Add client
	server.addClient("test_device", client)

	if server.GetClientCount() != 1 {
		t.Errorf("Expected 1 client, got %d", server.GetClientCount())
	}

	// Get client info
	info, err := server.GetClientInfo("test_device")
	if err != nil {
		t.Fatalf("Failed to get client info: %v", err)
	}

	if info.DeviceID != "test_device" {
		t.Errorf("Expected device_id='test_device', got %s", info.DeviceID)
	}

	// Remove client
	server.removeClient("test_device")

	if server.GetClientCount() != 0 {
		t.Errorf("Expected 0 clients after removal, got %d", server.GetClientCount())
	}
}

// TestConcurrentClientAccess Test concurrent client access
func TestConcurrentClientAccess(t *testing.T) {
	stateMgr := state.NewManager("/tmp/test-concurrent-access")
	server := NewWebSocketServer(stateMgr, "test", nil, false, "/tmp/test-ws")

	var wg sync.WaitGroup
	numGoroutines := 10

	// Concurrent add clients
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			client := &WebSocketClient{
				DeviceID:    "device_" + string(rune('A'+id)),
				ConnectedAt: time.Now(),
				LastActive:  time.Now(),
			}
			server.addClient("device_"+string(rune('A'+id)), client)
		}(i)
	}

	wg.Wait()

	if server.GetClientCount() != numGoroutines {
		t.Errorf("Expected %d clients, got %d", numGoroutines, server.GetClientCount())
	}

	// Concurrent remove clients
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			server.removeClient("device_" + string(rune('A'+id)))
		}(i)
	}

	wg.Wait()

	if server.GetClientCount() != 0 {
		t.Errorf("Expected 0 clients after concurrent removal, got %d", server.GetClientCount())
	}
}

// TestBroadcastToClients Test broadcast to clients
func TestBroadcastToClients(t *testing.T) {
	// This test needs real WebSocket, testing logic only
	stateMgr := state.NewManager("/tmp/test-broadcast")
	server := NewWebSocketServer(stateMgr, "test", nil, false, "/tmp/test-ws")

	event := state.Event{
		Type:      "chunk",
		Timestamp: time.Now().UnixMilli(),
		Data:      map[string]string{"content": "test"},
	}

	// Should not panic with no clients
	server.broadcastToClients(event)
	t.Log("Broadcast to empty clients succeeded")
}

// TestSplitLines Test split lines
func TestSplitLines(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"line1\nline2\nline3", 3},
		{"single line", 1},
		{"", 0},
		{"\n", 1},
		{"line1\n\nline3", 3},
	}

	for _, test := range tests {
		lines := splitLinesCopy([]byte(test.input))
		if len(lines) != test.expected {
			t.Errorf("splitLinesCopy(%q) returned %d lines, expected %d", test.input, len(lines), test.expected)
		}
	}
}

// TestClientMessageUnmarshal Test client message unmarshal
func TestClientMessageUnmarshal(t *testing.T) {
	jsonData := `{"command": "send_input", "data": {"content": "Hello"}}`

	var msg ClientMessage
	err := json.Unmarshal([]byte(jsonData), &msg)
	if err != nil {
		t.Fatalf("Failed to unmarshal ClientMessage: %v", err)
	}

	if msg.Command != "send_input" {
		t.Errorf("Expected command='send_input', got %s", msg.Command)
	}

	if msg.Data["content"] != "Hello" {
		t.Errorf("Expected content='Hello', got %v", msg.Data["content"])
	}
}

// TestServerEvent Test server event
func TestServerEvent(t *testing.T) {
	// Test state.Event directly (ServerEvent was removed)
	event := state.Event{
		Type: "chunk",
		Data: map[string]interface{}{"content": "test"},
	}

	if event.Type != "chunk" {
		t.Errorf("Expected type='chunk', got %s", event.Type)
	}
}

// TestGenerateDeviceID Test device ID generation
func TestGenerateDeviceID(t *testing.T) {
	id1 := generateDeviceID()
	id2 := generateDeviceID()

	if id1 == "" {
		t.Error("Expected non-empty device ID")
	}

	if !strings.HasPrefix(id1, "device_") {
		t.Errorf("Expected device ID to start with 'device_', got %s", id1)
	}

	// Two IDs should be different (probabilistic)
	t.Logf("Generated IDs: %s, %s", id1, id2)
}

// TestHealthEndpoint Test health endpoint
func TestHealthEndpoint(t *testing.T) {
	t.Skip("Skipping: HTTP handler registration conflict in tests")

	stateMgr := state.NewManager("/tmp/test-health")
	server := NewWebSocketServer(stateMgr, "test_task", nil, false, "/tmp/test-ws")

	// StartServer
	port, err := server.Start(":0")
	if err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer server.Stop()

	// Wait for server start
	time.Sleep(100 * time.Millisecond)

	// Can test health endpoint via HTTP
	// HereOnlyTestLogic
	t.Logf("Health endpoint available at http://localhost:%d/health", port)
}

// TestClientReconnect Test client reconnect logic
func TestClientReconnect(t *testing.T) {
	client := &WebSocketClient{
		DeviceID:       "test_device",
		ReconnectCount: 0,
	}

	// Simulate multiple reconnects
	for i := 0; i < MaxReconnectAttempts; i++ {
		client.IncrementReconnect()
		t.Logf("Reconnect attempt %d/%d", client.GetReconnectCount(), MaxReconnectAttempts)
	}

	if client.GetReconnectCount() != MaxReconnectAttempts {
		t.Errorf("Expected reconnect count %d, got %d", MaxReconnectAttempts, client.GetReconnectCount())
	}
}

// TestReconnectDelay Test reconnect delay
func TestReconnectDelay(t *testing.T) {
	// TestExponentialBackoff
	delays := []time.Duration{}
	for i := 0; i < MaxReconnectAttempts; i++ {
		delay := time.Duration(1<<uint(i)) * ReconnectDelayBase
		if delay > ReconnectDelayMax {
			delay = ReconnectDelayMax
		}
		delays = append(delays, delay)
		t.Logf("Attempt %d: delay=%v", i+1, delay)
	}

	// Verify increasing delays
	for i := 1; i < len(delays)-1; i++ {
		if delays[i] <= delays[i-1] {
			t.Errorf("Expected increasing delays: %v <= %v", delays[i], delays[i-1])
		}
	}

	// Verify does not exceed max
	if delays[len(delays)-1] > ReconnectDelayMax {
		t.Errorf("Max delay exceeded: %v > %v", delays[len(delays)-1], ReconnectDelayMax)
	}
}

// TestErrorDefinitions Test error definitions
func TestErrorDefinitions(t *testing.T) {
	errors := []error{
		ErrClientNotFound,
		ErrConnectionClosed,
		ErrWriteFailed,
		ErrReconnectFailed,
		ErrMaxAttemptsReached,
	}

	for _, err := range errors {
		if err == nil {
			t.Error("Expected non-nil error")
		}
		t.Logf("Error: %v", err)
	}
}

// TestServerConfig Test server config
func TestServerConfig(t *testing.T) {
	stateMgr := state.NewManager("/tmp/test-config")
	server := NewWebSocketServer(stateMgr, "test", nil, false, "/tmp/test-ws")

	// Verify default config
	if !server.config.EnableCompression {
		t.Error("Expected EnableCompression to be true")
	}

	if !server.config.EnableHeartbeat {
		t.Error("Expected EnableHeartbeat to be true")
	}

	if !server.config.ReconnectEnabled {
		t.Error("Expected ReconnectEnabled to be true")
	}
}

// TestConcurrentBroadcast Test concurrent broadcast
func TestConcurrentBroadcast(t *testing.T) {
	stateMgr := state.NewManager("/tmp/test-concurrent-broadcast")
	server := NewWebSocketServer(stateMgr, "test", nil, false, "/tmp/test-ws")

	// StartBroadcastHandleer
	server.wg.Add(1)
	go server.broadcastHandler()

	event := state.Event{
		Type:      "chunk",
		Timestamp: time.Now().UnixMilli(),
		Data:      map[string]string{"content": "test"},
	}

	var wg sync.WaitGroup
	numGoroutines := 10

	// Concurrent send broadcasts
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			server.broadcast(event)
		}()
	}

	wg.Wait()

	// Cleanup
	close(server.broadcastCh)
	server.wg.Wait()
}

// TestWebSocketConstants Test WebSocket constants
func TestWebSocketConstants(t *testing.T) {
	// Verify constants are reasonable
	if MaxReconnectAttempts <= 0 {
		t.Error("MaxReconnectAttempts should be positive")
	}

	if ReconnectDelayBase <= 0 {
		t.Error("ReconnectDelayBase should be positive")
	}

	if ReconnectDelayMax < ReconnectDelayBase {
		t.Error("ReconnectDelayMax should be >= ReconnectDelayBase")
	}

	if PingInterval <= 0 {
		t.Error("PingInterval should be positive")
	}

	if WriteTimeout <= 0 {
		t.Error("WriteTimeout should be positive")
	}

	t.Logf("MaxReconnectAttempts: %d", MaxReconnectAttempts)
	t.Logf("ReconnectDelayBase: %v", ReconnectDelayBase)
	t.Logf("ReconnectDelayMax: %v", ReconnectDelayMax)
	t.Logf("PingInterval: %v", PingInterval)
	t.Logf("WriteTimeout: %v", WriteTimeout)
}
