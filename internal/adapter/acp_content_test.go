package adapter

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildPromptBlocksTextAndImage(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("png-bytes"))
	blocks, err := BuildPromptBlocks("hello", []PromptAttachment{
		{Type: "image", MIMEType: "image/png", Data: data},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 {
		t.Fatalf("blocks=%d want 2", len(blocks))
	}
	if blocks[0]["type"] != "text" || blocks[0]["text"] != "hello" {
		t.Fatalf("text block = %v", blocks[0])
	}
	if blocks[1]["type"] != "image" || blocks[1]["mimeType"] != "image/png" || blocks[1]["data"] != data {
		t.Fatalf("image block = %v", blocks[1])
	}
}

func TestBuildPromptBlocksFileResourceLink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	blocks, err := BuildPromptBlocks("", []PromptAttachment{
		{Type: "file", Path: path},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks=%d", len(blocks))
	}
	if blocks[0]["type"] != "resource_link" {
		t.Fatalf("type=%v", blocks[0]["type"])
	}
	uri, _ := blocks[0]["uri"].(string)
	if !strings.HasPrefix(uri, "file://") || !strings.Contains(uri, "notes.txt") {
		t.Fatalf("uri=%q", uri)
	}
	if blocks[0]["name"] != "notes.txt" {
		t.Fatalf("name=%v", blocks[0]["name"])
	}
}

func TestBuildPromptBlocksImageFromPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pic.png")
	raw := []byte{0x89, 0x50, 0x4e, 0x47}
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatal(err)
	}
	blocks, err := BuildPromptBlocks("look", []PromptAttachment{
		{Type: "file", Path: "pic.png"},
	}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 {
		t.Fatalf("blocks=%d", len(blocks))
	}
	if blocks[1]["type"] != "image" {
		t.Fatalf("expected image, got %v", blocks[1])
	}
	if blocks[1]["mimeType"] != "image/png" {
		t.Fatalf("mime=%v", blocks[1]["mimeType"])
	}
	decoded, err := base64.StdEncoding.DecodeString(blocks[1]["data"].(string))
	if err != nil || string(decoded) != string(raw) {
		t.Fatalf("decoded=%q err=%v", decoded, err)
	}
}

func TestParseAttachmentsCamelCase(t *testing.T) {
	atts, err := ParseAttachments([]interface{}{
		map[string]interface{}{
			"type":     "image",
			"mimeType": "image/jpeg",
			"data":     "abc",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 1 || atts[0].MIMEType != "image/jpeg" || atts[0].Data != "abc" {
		t.Fatalf("got %+v", atts)
	}
}

func TestMCPServersToACPStdioAndHTTP(t *testing.T) {
	out := MCPServersToACP([]MCPServer{
		{Name: "fs", Command: "npx", Args: []string{"-y", "mcp-fs"}, Env: MCPEnvList{{Name: "FOO", Value: "bar"}}},
		{Type: "http", Name: "remote", URL: "https://example.test/mcp", Headers: MCPHeaderList{{Name: "Authorization", Value: "Bearer x"}}},
		{Name: "skip-me"},
	})
	if len(out) != 2 {
		t.Fatalf("len=%d want 2", len(out))
	}
	stdio := out[0].(map[string]interface{})
	if stdio["name"] != "fs" || stdio["command"] != "npx" {
		t.Fatalf("stdio=%v", stdio)
	}
	if _, hasType := stdio["type"]; hasType {
		t.Fatalf("stdio should omit type, got %v", stdio)
	}
	env := stdio["env"].([]map[string]string)
	if len(env) != 1 || env[0]["name"] != "FOO" {
		t.Fatalf("env=%v", env)
	}
	http := out[1].(map[string]interface{})
	if http["type"] != "http" || http["url"] != "https://example.test/mcp" {
		t.Fatalf("http=%v", http)
	}
}

func TestParseMCPServersEnvObject(t *testing.T) {
	servers, err := ParseMCPServers([]interface{}{
		map[string]interface{}{
			"name":    "fs",
			"command": "npx",
			"env":     map[string]interface{}{"A": "1", "B": "2"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 {
		t.Fatalf("len=%d", len(servers))
	}
	if len(servers[0].Env) != 2 {
		t.Fatalf("env=%v", servers[0].Env)
	}
}
