package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"openpal/internal/adapter"
	"openpal/internal/state"
)

func writeMockACP(t *testing.T, dir string) string {
	t.Helper()
	script := `#!/bin/bash
while IFS= read -r line; do
  id=$(echo "$line" | sed -n 's/.*"id":[[:space:]]*\([0-9][0-9]*\).*/\1/p' | head -1)
  if echo "$line" | grep -q '"initialize"'; then
    echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"protocolVersion\":1,\"agentCapabilities\":{}}}"
  elif echo "$line" | grep -q 'session/new'; then
    echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"sessionId\":\"sess-e2e\"}}"
  elif echo "$line" | grep -q 'session/prompt'; then
    text="hello"
    if echo "$line" | grep -q 'second'; then
      text="follow-up"
    fi
    echo "{\"jsonrpc\":\"2.0\",\"method\":\"session/update\",\"params\":{\"sessionId\":\"sess-e2e\",\"sessionUpdate\":\"agent_message_chunk\",\"content\":{\"type\":\"text\",\"text\":\"$text\"}}}"
    echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"stopReason\":\"end_turn\"}}"
  fi
done
`
	path := filepath.Join(dir, "copilot")
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestACPWebSocketEndToEnd(t *testing.T) {
	tmp := t.TempDir()
	mockPath := writeMockACP(t, tmp)

	stateMgr := state.NewManager(tmp)
	if err := stateMgr.CreateTask("acp-e2e", "copilot"); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	cli := adapter.NewAdapter("copilot", tmp)
	if cli.GetMode() != adapter.ModeACP {
		t.Fatalf("expected ACP mode, got %s", cli.GetMode())
	}
	cli.SetCLIPath(mockPath)

	srv := NewWebSocketServer(stateMgr, "acp-e2e", cli, tmp)
	srv.wg.Add(2)
	go srv.broadcastHandler()
	go srv.errorHandler()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", srv.handleWebSocket)
	mux.HandleFunc("/health", srv.handleHealth)
	hs := httptest.NewServer(mux)
	defer hs.Close()
	defer srv.Stop()

	wsURL := "ws" + strings.TrimPrefix(hs.URL, "http") + "/ws?device=e2e"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]interface{}{
		"command": "start_task",
		"data":    map[string]interface{}{"task": "say hello"},
	}); err != nil {
		t.Fatalf("start_task: %v", err)
	}

	sawHello := waitForWSEvent(t, conn, 5*time.Second, func(typ string, data map[string]interface{}) bool {
		return typ == "chunk" && data["content"] == "hello"
	})
	if !sawHello {
		t.Fatal("did not receive ACP chunk 'hello' over WebSocket")
	}

	if err := conn.WriteJSON(map[string]interface{}{
		"command": "send_input",
		"data":    map[string]interface{}{"content": "second turn"},
	}); err != nil {
		t.Fatalf("send_input: %v", err)
	}

	sawFollowUp := waitForWSEvent(t, conn, 5*time.Second, func(typ string, data map[string]interface{}) bool {
		return typ == "chunk" && data["content"] == "follow-up"
	})
	if !sawFollowUp {
		t.Fatal("did not receive second-turn ACP chunk over WebSocket")
	}

	resp, err := http.Get(hs.URL + "/health")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	defer resp.Body.Close()
	var health map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	if health["cli_mode"] != "acp" {
		t.Fatalf("health cli_mode = %v, want acp", health["cli_mode"])
	}
	if health["provider"] != "copilot" {
		t.Fatalf("health provider = %v, want copilot", health["provider"])
	}

	if got := readACPSessionID(tmp, "acp-e2e"); got != "sess-e2e" {
		t.Fatalf("persisted session id = %q", got)
	}
}

func waitForWSEvent(t *testing.T, conn *websocket.Conn, timeout time.Duration, match func(typ string, data map[string]interface{}) bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	conn.SetReadDeadline(deadline)
	for time.Now().Before(deadline) {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return false
		}
		var msg struct {
			Type string                 `json:"type"`
			Data map[string]interface{} `json:"data"`
		}
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}
		if match(msg.Type, msg.Data) {
			return true
		}
	}
	return false
}

func writeRecordingMockACP(t *testing.T, dir, initCaps string) (binPath, logPath string) {
	t.Helper()
	logPath = filepath.Join(dir, "acp.jsonl")
	script := fmt.Sprintf(`#!/bin/bash
LOG=%q
CAPS=%q
while IFS= read -r line; do
  printf '%%s\n' "$line" >> "$LOG"
  id=$(echo "$line" | sed -n 's/.*"id":[[:space:]]*\([0-9][0-9]*\).*/\1/p' | head -1)
  if echo "$line" | grep -q '"initialize"'; then
    echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"protocolVersion\":1,\"agentCapabilities\":$CAPS}}"
  elif echo "$line" | grep -q 'session/load'; then
    echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{}}"
  elif echo "$line" | grep -q 'session/new'; then
    echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"sessionId\":\"sess-new\"}}"
  elif echo "$line" | grep -q 'session/prompt'; then
    text="hello"
    if echo "$line" | grep -q '"type":"image"'; then
      text="got-image"
    fi
    echo "{\"jsonrpc\":\"2.0\",\"method\":\"session/update\",\"params\":{\"sessionId\":\"sess-resume\",\"sessionUpdate\":\"agent_message_chunk\",\"content\":{\"type\":\"text\",\"text\":\"$text\"}}}"
    echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"stopReason\":\"end_turn\"}}"
  fi
done
`, logPath, initCaps)
	binPath = filepath.Join(dir, "copilot")
	if err := os.WriteFile(binPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return binPath, logPath
}

func startACPTestServer(t *testing.T, tmp, mockPath, taskID string) (*httptest.Server, *WebSocketServer) {
	t.Helper()
	stateMgr := state.NewManager(tmp)
	if err := stateMgr.CreateTask(taskID, "copilot"); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	cli := adapter.NewAdapter("copilot", tmp)
	cli.SetCLIPath(mockPath)
	srv := NewWebSocketServer(stateMgr, taskID, cli, tmp)
	srv.wg.Add(2)
	go srv.broadcastHandler()
	go srv.errorHandler()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", srv.handleWebSocket)
	mux.HandleFunc("/health", srv.handleHealth)
	hs := httptest.NewServer(mux)
	return hs, srv
}

func TestACPWebSocketResumeAttachmentsAndMCP(t *testing.T) {
	tmp := t.TempDir()
	mockPath, logPath := writeRecordingMockACP(t, tmp, `{"loadSession":true,"promptCapabilities":{"image":true},"mcpCapabilities":{"http":true}}`)
	hs, srv := startACPTestServer(t, tmp, mockPath, "acp-resume")
	defer hs.Close()
	defer srv.Stop()

	wsURL := "ws" + strings.TrimPrefix(hs.URL, "http") + "/ws?device=e2e"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]interface{}{
		"command": "start_task",
		"data": map[string]interface{}{
			"task":       "look at this",
			"session_id": "sess-resume",
			"attachments": []map[string]interface{}{
				{"type": "image", "mime_type": "image/png", "data": "aGVsbG8="},
			},
			"mcp_servers": []map[string]interface{}{
				{"name": "fs", "command": "npx", "args": []string{"mcp-fs"}},
			},
		},
	}); err != nil {
		t.Fatalf("start_task: %v", err)
	}

	sawImage := waitForWSEvent(t, conn, 5*time.Second, func(typ string, data map[string]interface{}) bool {
		return typ == "chunk" && data["content"] == "got-image"
	})
	if !sawImage {
		t.Fatal("did not receive image-ack ACP chunk over WebSocket")
	}

	deadline := time.Now().Add(2 * time.Second)
	var log string
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(logPath)
		if err == nil {
			log = string(raw)
			if strings.Contains(log, `"session/load"`) && strings.Contains(log, "sess-resume") &&
				strings.Contains(log, "mcp-fs") && strings.Contains(log, `"image/png"`) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(log, `"session/load"`) {
		t.Fatalf("expected session/load: %s", log)
	}
	if strings.Contains(log, `"session/new"`) {
		t.Fatalf("did not expect session/new: %s", log)
	}
	if !strings.Contains(log, "mcp-fs") {
		t.Fatalf("expected mcp server on session/load: %s", log)
	}
	if !strings.Contains(log, `"image/png"`) || !strings.Contains(log, "aGVsbG8=") {
		t.Fatalf("expected image ContentBlock: %s", log)
	}
	if got := readACPSessionID(tmp, "acp-resume"); got != "sess-resume" {
		t.Fatalf("persisted session id = %q", got)
	}
}
