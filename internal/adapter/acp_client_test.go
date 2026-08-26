package adapter

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestACPClientHandshakeAndPrompt(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	mock := `#!/bin/bash
while IFS= read -r line; do
  id=$(echo "$line" | sed -n 's/.*"id":[[:space:]]*\([0-9][0-9]*\).*/\1/p' | head -1)
  if echo "$line" | grep -q '"initialize"'; then
    echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"protocolVersion\":1,\"agentCapabilities\":{}}}"
  elif echo "$line" | grep -q 'session/new'; then
    echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"sessionId\":\"sess-1\"}}"
  elif echo "$line" | grep -q 'session/prompt'; then
    echo '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess-1","sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hello"}}}'
    echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"stopReason\":\"end_turn\"}}"
  fi
done
`
	path := mockCLI(t, "mock-acp-handshake", mock)
	client, err := NewACPClient("copilot", path)
	if err != nil {
		t.Fatalf("NewACPClient: %v", err)
	}
	defer client.Stop()

	var events []map[string]interface{}
	var mu sync.Mutex
	client.SetEventHandler(func(ev map[string]interface{}) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	})

	if err := client.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if client.Pid() == 0 {
		t.Fatal("expected non-zero pid")
	}

	sid, err := client.NewSession(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if sid != "sess-1" {
		t.Fatalf("session id = %q", sid)
	}

	if err := client.Prompt("hi"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := append([]map[string]interface{}(nil), events...)
		mu.Unlock()
		var sawChunk, sawResult bool
		for _, ev := range got {
			if ev["type"] == "chunk" && ev["content"] == "hello" {
				sawChunk = true
			}
			if ev["type"] == "result" {
				sawResult = true
			}
		}
		if sawChunk && sawResult {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	t.Fatalf("timed out waiting for chunk+result, events=%v", events)
}

func TestACPClientPermissionFlow(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	mock := `#!/bin/bash
PROMPT_ID=""
while IFS= read -r line; do
  id=$(echo "$line" | sed -n 's/.*"id":[[:space:]]*\([0-9][0-9]*\).*/\1/p' | head -1)
  if echo "$line" | grep -q '"initialize"'; then
    echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"protocolVersion\":1}}"
  elif echo "$line" | grep -q 'session/new'; then
    echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"sessionId\":\"sess-perm\"}}"
  elif echo "$line" | grep -q 'session/prompt'; then
    PROMPT_ID=$id
    echo '{"jsonrpc":"2.0","id":99,"method":"session/request_permission","params":{"sessionId":"sess-perm","toolCall":{"title":"Run ls"},"options":[{"optionId":"allow-once","name":"Allow","kind":"allow_once"},{"optionId":"reject-once","name":"Reject","kind":"reject_once"}]}}'
  elif echo "$line" | grep -q '"outcome"'; then
    echo "{\"jsonrpc\":\"2.0\",\"id\":$PROMPT_ID,\"result\":{\"stopReason\":\"end_turn\"}}"
  fi
done
`
	path := mockCLI(t, "mock-acp-perm", mock)
	client, err := NewACPClient("copilot", path)
	if err != nil {
		t.Fatalf("NewACPClient: %v", err)
	}
	defer client.Stop()

	sawPerm := make(chan map[string]interface{}, 1)
	client.SetEventHandler(func(ev map[string]interface{}) {
		if ev["type"] == "permission_request" {
			select {
			case sawPerm <- ev:
			default:
			}
		}
	})

	if err := client.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := client.NewSession(t.TempDir(), nil); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := client.Prompt("need tools"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	select {
	case ev := <-sawPerm:
		if ev["session_id"] != "sess-perm" {
			t.Fatalf("unexpected permission event: %v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for permission_request")
	}

	if err := client.RespondPermission(true); err != nil {
		t.Fatalf("RespondPermission: %v", err)
	}
}

func TestACPClientCancelWithoutSession(t *testing.T) {
	client := &ACPClient{provider: "copilot", pending: map[string]*acpPendingCall{}}
	if err := client.Cancel(); err != nil {
		t.Fatalf("Cancel with no session: %v", err)
	}
}

func TestPermissionOutcomeSelection(t *testing.T) {
	opts := []ACPPermissionOption{
		{OptionID: "allow-once", Name: "Allow", Kind: "allow_once"},
		{OptionID: "reject-once", Name: "Reject", Kind: "reject_once"},
	}
	allow := permissionOutcome(opts, true)
	if allow["outcome"] != "selected" || allow["optionId"] != "allow-once" {
		t.Fatalf("approve outcome = %v", allow)
	}
	reject := permissionOutcome(opts, false)
	if reject["outcome"] != "selected" || reject["optionId"] != "reject-once" {
		t.Fatalf("reject outcome = %v", reject)
	}
	cancelled := permissionOutcome(nil, false)
	if cancelled["outcome"] != "cancelled" {
		t.Fatalf("empty options should cancel, got %v", cancelled)
	}
}

func TestParseSessionUpdateNested(t *testing.T) {
	client := &ACPClient{provider: "copilot"}
	raw := `{
		"jsonrpc":"2.0",
		"method":"session/update",
		"params":{
			"sessionId":"s1",
			"update":{
				"sessionUpdate":"agent_message_chunk",
				"content":{"type":"text","text":"nested"}
			}
		}
	}`
	var msg ACPMessage
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	got := client.ParseMessage(&msg)
	if got["type"] != "chunk" || got["content"] != "nested" {
		t.Fatalf("nested update parse = %v", got)
	}
}

func TestJSONIDKey(t *testing.T) {
	if jsonIDKey(nil) != "" {
		t.Fatal("nil id should be empty")
	}
	if jsonIDKey([]byte("1")) != "1" {
		t.Fatalf("numeric id: %q", jsonIDKey([]byte("1")))
	}
	if jsonIDKey([]byte(`"abc"`)) != "abc" {
		t.Fatalf("string id: %q", jsonIDKey([]byte(`"abc"`)))
	}
}

func TestNewAdapterUsesACPForCopilotAndOpenCode(t *testing.T) {
	copilot := NewAdapter("copilot", "/tmp")
	if copilot.GetMode() != ModeACP {
		t.Fatalf("copilot mode = %s, want acp", copilot.GetMode())
	}
	opencode := NewAdapter("opencode", "/tmp")
	if opencode.GetMode() != ModeACP {
		t.Fatalf("opencode mode = %s, want acp", opencode.GetMode())
	}
	claude := NewAdapter("claude", "/tmp")
	if claude.GetMode() != ModeText {
		t.Fatalf("claude mode = %s, want text", claude.GetMode())
	}
	codex := NewAdapter("codex", "/tmp")
	if codex.GetMode() != ModeText {
		t.Fatalf("codex mode = %s, want text", codex.GetMode())
	}
	gemini := NewAdapter("gemini", "/tmp")
	if gemini.GetMode() != ModeText {
		t.Fatalf("gemini mode = %s, want text", gemini.GetMode())
	}
}

func TestRespondPermissionWithoutPending(t *testing.T) {
	client, err := NewACPClient("copilot", "")
	if err != nil {
		t.Fatal(err)
	}
	err = client.RespondPermission(true)
	if err == nil || !strings.Contains(err.Error(), "no pending") {
		t.Fatalf("expected no pending error, got %v", err)
	}
}

func recordingACPMock(t *testing.T, initCaps string) (binPath, logPath string) {
	t.Helper()
	dir := t.TempDir()
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
    text="ok"
    if echo "$line" | grep -q '"type":"image"'; then
      text="got-image"
    fi
    echo "{\"jsonrpc\":\"2.0\",\"method\":\"session/update\",\"params\":{\"sessionId\":\"sess-new\",\"sessionUpdate\":\"agent_message_chunk\",\"content\":{\"type\":\"text\",\"text\":\"$text\"}}}"
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

func readMockLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestACPClientLoadSessionAndMCP(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	bin, logPath := recordingACPMock(t, `{"loadSession":true,"mcpCapabilities":{"http":true}}`)
	client, err := NewACPClient("copilot", bin)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Stop()
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	if !client.SupportsLoadSession() {
		t.Fatal("expected loadSession capability")
	}
	if !client.SupportsMCPHTTP() {
		t.Fatal("expected mcp http capability")
	}

	mcp := MCPServersToACP([]MCPServer{
		{Name: "fs", Command: "npx", Args: []string{"mcp-fs"}},
		{Type: "http", Name: "remote", URL: "https://example.test/mcp"},
	})
	if err := client.LoadSession("sess-old", t.TempDir(), mcp); err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if client.GetSessionID() != "sess-old" {
		t.Fatalf("session id = %q", client.GetSessionID())
	}
	log := readMockLog(t, logPath)
	if !strings.Contains(log, `"session/load"`) {
		t.Fatalf("expected session/load in log: %s", log)
	}
	if strings.Contains(log, `"session/new"`) {
		t.Fatalf("did not expect session/new: %s", log)
	}
	if !strings.Contains(log, "sess-old") || !strings.Contains(log, "mcp-fs") || !strings.Contains(log, "https://example.test/mcp") {
		t.Fatalf("load params missing: %s", log)
	}
}

func TestACPClientPromptWithImage(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	bin, logPath := recordingACPMock(t, `{"promptCapabilities":{"image":true}}`)
	client, err := NewACPClient("copilot", bin)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Stop()
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.NewSession(t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	data := "aGVsbG8="
	if err := client.PromptWith("see this", []PromptAttachment{
		{Type: "image", MIMEType: "image/png", Data: data},
	}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var log string
	for time.Now().Before(deadline) {
		log = readMockLog(t, logPath)
		if strings.Contains(log, `"session/prompt"`) && strings.Contains(log, `"image/png"`) && strings.Contains(log, data) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("prompt did not include image block: %s", log)
}

func TestManagerCreateSessionLoadThenNew(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	bin, logPath := recordingACPMock(t, `{"loadSession":true}`)
	mgr := NewAdapter("copilot", t.TempDir())
	mgr.SetCLIPath(bin)
	mgr.SetResumeSessionID("sess-saved")
	mgr.SetMCPServers([]MCPServer{{Name: "fs", Command: "npx", Args: []string{"-y", "mcp-fs"}}})
	if _, err := mgr.Start(); err != nil {
		t.Fatal(err)
	}
	defer mgr.acpClient.Stop()
	if err := mgr.CreateSession(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if mgr.ACPSessionID() != "sess-saved" {
		t.Fatalf("session id = %q want sess-saved", mgr.ACPSessionID())
	}
	log := readMockLog(t, logPath)
	if !strings.Contains(log, `"session/load"`) {
		t.Fatalf("expected session/load: %s", log)
	}
	if !strings.Contains(log, "mcp-fs") {
		t.Fatalf("expected mcp server on load: %s", log)
	}
	if strings.Contains(log, `"session/new"`) {
		t.Fatalf("did not expect session/new: %s", log)
	}
}

func TestManagerCreateSessionFallsBackWhenLoadUnsupported(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	bin, logPath := recordingACPMock(t, `{}`)
	mgr := NewAdapter("copilot", t.TempDir())
	mgr.SetCLIPath(bin)
	mgr.SetResumeSessionID("sess-saved")
	if _, err := mgr.Start(); err != nil {
		t.Fatal(err)
	}
	defer mgr.acpClient.Stop()
	if err := mgr.CreateSession(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if mgr.ACPSessionID() != "sess-new" {
		t.Fatalf("session id = %q want sess-new", mgr.ACPSessionID())
	}
	log := readMockLog(t, logPath)
	if strings.Contains(log, `"session/load"`) {
		t.Fatalf("did not expect session/load: %s", log)
	}
	if !strings.Contains(log, `"session/new"`) {
		t.Fatalf("expected session/new: %s", log)
	}
}
