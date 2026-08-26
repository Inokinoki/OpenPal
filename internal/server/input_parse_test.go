package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"openpal/internal/adapter"
)

func TestParseClientInputStartTask(t *testing.T) {
	data := map[string]interface{}{
		"task":       "do work",
		"session_id": "sess-1",
		"attachments": []interface{}{
			map[string]interface{}{
				"type":      "image",
				"mime_type": "image/png",
				"data":      "abc",
			},
		},
		"mcp_servers": []interface{}{
			map[string]interface{}{"name": "fs", "command": "npx"},
		},
	}
	msg := parseClientInput("task", data)
	if msg.Content != "do work" || msg.SessionID != "sess-1" {
		t.Fatalf("content/session = %q %q", msg.Content, msg.SessionID)
	}
	if len(msg.Attachments) != 1 || msg.Attachments[0].Type != "image" {
		t.Fatalf("attachments = %+v", msg.Attachments)
	}
	if !msg.HasMCPServers || len(msg.MCPServers) != 1 || msg.MCPServers[0].Name != "fs" {
		t.Fatalf("mcp = %+v has=%v", msg.MCPServers, msg.HasMCPServers)
	}
}

func TestParseClientInputOmitsMCP(t *testing.T) {
	msg := parseClientInput("input", map[string]interface{}{"content": "hi"})
	if msg.HasMCPServers {
		t.Fatal("mcp should be unset when omitted")
	}
	if msg.Content != "hi" {
		t.Fatalf("content=%q", msg.Content)
	}
}

func TestParseClientInputEmptyMCPArray(t *testing.T) {
	msg := parseClientInput("task", map[string]interface{}{
		"task":        "x",
		"mcp_servers": []interface{}{},
	})
	if !msg.HasMCPServers {
		t.Fatal("empty mcp_servers should still set HasMCPServers")
	}
	if len(msg.MCPServers) != 0 {
		t.Fatalf("len=%d", len(msg.MCPServers))
	}
}

func TestACPSessionIDPersist(t *testing.T) {
	dir := t.TempDir()
	if got := readACPSessionID(dir, "task-1"); got != "" {
		t.Fatalf("empty read got %q", got)
	}
	if err := writeACPSessionID(dir, "task-1", "sess-abc"); err != nil {
		t.Fatal(err)
	}
	if got := readACPSessionID(dir, "task-1"); got != "sess-abc" {
		t.Fatalf("got %q", got)
	}
	path := filepath.Join(dir, "task-1", "acp_session_id")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "sess-abc\n" {
		t.Fatalf("file=%q", data)
	}
}

func TestParseAttachmentsRoundTripJSON(t *testing.T) {
	raw := json.RawMessage(`[{"type":"file","path":"/tmp/a.go"}]`)
	atts, err := adapter.ParseAttachments(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 1 || atts[0].Path != "/tmp/a.go" {
		t.Fatalf("%+v", atts)
	}
}
