package server

import (
	"encoding/json"
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
