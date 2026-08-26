package adapter

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MaxInlineAttachmentBytes is the cap for inlined image/resource blobs
// sent as ACP ContentBlocks (base64 on the wire).
const MaxInlineAttachmentBytes = 8 << 20

// PromptAttachment is a user-facing attachment on start_task / send_input.
// Type is one of: text, image, file, resource_link, resource.
type PromptAttachment struct {
	Type     string `json:"type,omitempty"`
	Text     string `json:"text,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
	Data     string `json:"data,omitempty"`
	Path     string `json:"path,omitempty"`
	URI      string `json:"uri,omitempty"`
	Name     string `json:"name,omitempty"`
}

// MCPEnvVar is an ACP env entry {name, value}.
type MCPEnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// MCPHeader is an ACP HTTP header {name, value}.
type MCPHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// MCPEnvList accepts either [{"name","value"}] or {"KEY":"VAL"}.
type MCPEnvList []MCPEnvVar

// UnmarshalJSON accepts an ACP env array or a JSON object map.
func (e *MCPEnvList) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*e = MCPEnvList{}
		return nil
	}
	var arr []MCPEnvVar
	if err := json.Unmarshal(data, &arr); err == nil {
		*e = arr
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("mcp env: expected array or object: %w", err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(MCPEnvList, 0, len(keys))
	for _, k := range keys {
		out = append(out, MCPEnvVar{Name: k, Value: m[k]})
	}
	*e = out
	return nil
}

// MCPHeaderList accepts either [{"name","value"}] or {"Key":"Val"}.
type MCPHeaderList []MCPHeader

// UnmarshalJSON accepts an ACP header array or a JSON object map.
func (h *MCPHeaderList) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*h = MCPHeaderList{}
		return nil
	}
	var arr []MCPHeader
	if err := json.Unmarshal(data, &arr); err == nil {
		*h = arr
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("mcp headers: expected array or object: %w", err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(MCPHeaderList, 0, len(keys))
	for _, k := range keys {
		out = append(out, MCPHeader{Name: k, Value: m[k]})
	}
	*h = out
	return nil
}

// MCPServer is a session-scoped MCP server for session/new and session/load.
// Type is "", "stdio", "http", or "sse". Empty type means stdio.
type MCPServer struct {
	Type    string        `json:"type,omitempty"`
	Name    string        `json:"name"`
	Command string        `json:"command,omitempty"`
	Args    []string      `json:"args,omitempty"`
	Env     MCPEnvList    `json:"env,omitempty"`
	URL     string        `json:"url,omitempty"`
	Headers MCPHeaderList `json:"headers,omitempty"`
}

// ParseAttachments decodes a JSON array (or nil) into prompt attachments.
func ParseAttachments(raw interface{}) ([]PromptAttachment, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.([]interface{})
	if !ok {
		data, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("attachments: %w", err)
		}
		var typed []PromptAttachment
		if err := json.Unmarshal(data, &typed); err != nil {
			return nil, fmt.Errorf("attachments: %w", err)
		}
		return typed, nil
	}
	out := make([]PromptAttachment, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		out = append(out, attachmentFromMap(m))
	}
	return out, nil
}

func attachmentFromMap(m map[string]interface{}) PromptAttachment {
	return PromptAttachment{
		Type:     firstString(m, "type"),
		Text:     firstString(m, "text"),
		MIMEType: firstString(m, "mime_type", "mimeType"),
		Data:     firstString(m, "data"),
		Path:     firstString(m, "path", "file"),
		URI:      firstString(m, "uri"),
		Name:     firstString(m, "name"),
	}
}

func firstString(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// ParseMCPServers decodes a JSON array (or nil) into MCP server configs.
func ParseMCPServers(raw interface{}) ([]MCPServer, error) {
	if raw == nil {
		return nil, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("mcp_servers: %w", err)
	}
	var servers []MCPServer
	if err := json.Unmarshal(data, &servers); err != nil {
		return nil, fmt.Errorf("mcp_servers: %w", err)
	}
	return servers, nil
}

// MCPServersToACP converts servers to ACP mcpServers values.
// Stdio entries omit type; HTTP/SSE include type, url, and headers.
func MCPServersToACP(servers []MCPServer) []interface{} {
	out := make([]interface{}, 0, len(servers))
	for _, s := range servers {
		if m := s.toACPMap(); m != nil {
			out = append(out, m)
		}
	}
	return out
}

func (s MCPServer) toACPMap() map[string]interface{} {
	kind := strings.ToLower(strings.TrimSpace(s.Type))
	name := strings.TrimSpace(s.Name)
	if name == "" {
		return nil
	}
	switch kind {
	case "http", "sse":
		if strings.TrimSpace(s.URL) == "" {
			return nil
		}
		m := map[string]interface{}{
			"type": kind,
			"name": name,
			"url":  s.URL,
		}
		headers := make([]map[string]string, 0, len(s.Headers))
		for _, h := range s.Headers {
			if h.Name == "" {
				continue
			}
			headers = append(headers, map[string]string{"name": h.Name, "value": h.Value})
		}
		m["headers"] = headers
		return m
	default:
		if strings.TrimSpace(s.Command) == "" {
			return nil
		}
		args := s.Args
		if args == nil {
			args = []string{}
		}
		env := make([]map[string]string, 0, len(s.Env))
		for _, e := range s.Env {
			if e.Name == "" {
				continue
			}
			env = append(env, map[string]string{"name": e.Name, "value": e.Value})
		}
		return map[string]interface{}{
			"name":    name,
			"command": s.Command,
			"args":    args,
			"env":     env,
		}
	}
}

// BuildPromptBlocks turns text + attachments into ACP ContentBlocks.
func BuildPromptBlocks(text string, atts []PromptAttachment, workDir string) ([]map[string]interface{}, error) {
	blocks := make([]map[string]interface{}, 0, 1+len(atts))
	if text != "" {
		blocks = append(blocks, map[string]interface{}{
			"type": "text",
			"text": text,
		})
	}
	for i, att := range atts {
		block, err := attachmentBlock(att, workDir)
		if err != nil {
			return nil, fmt.Errorf("attachment %d: %w", i, err)
		}
		if block != nil {
			blocks = append(blocks, block)
		}
	}
	if len(blocks) == 0 {
		blocks = append(blocks, map[string]interface{}{
			"type": "text",
			"text": "",
		})
	}
	return blocks, nil
}

func attachmentBlock(att PromptAttachment, workDir string) (map[string]interface{}, error) {
	kind := strings.ToLower(strings.TrimSpace(att.Type))
	if kind == "" {
		switch {
		case att.Data != "" || strings.HasPrefix(strings.ToLower(att.MIMEType), "image/"):
			kind = "image"
		case att.Text != "":
			kind = "text"
		case att.URI != "":
			kind = "resource_link"
		case att.Path != "":
			kind = "file"
		default:
			return nil, fmt.Errorf("missing type")
		}
	}

	switch kind {
	case "text":
		if att.Text == "" {
			return nil, fmt.Errorf("text attachment missing text")
		}
		return map[string]interface{}{"type": "text", "text": att.Text}, nil
	case "image":
		return imageBlock(att, workDir)
	case "resource":
		return resourceBlock(att, workDir)
	case "resource_link":
		return resourceLinkBlock(att, workDir)
	case "file":
		return fileBlock(att, workDir)
	default:
		return nil, fmt.Errorf("unsupported attachment type %q", att.Type)
	}
}

func imageBlock(att PromptAttachment, workDir string) (map[string]interface{}, error) {
	mime := att.MIMEType
	data := att.Data
	if data == "" && att.Path != "" {
		path := resolvePath(att.Path, workDir)
		if mime == "" {
			mime = mimeFromPath(path, "image/png")
		}
		raw, err := readFileCapped(path, MaxInlineAttachmentBytes)
		if err != nil {
			if isTooLarge(err) {
				return resourceLinkFromPath(path, att.Name, mime), nil
			}
			return nil, err
		}
		data = base64.StdEncoding.EncodeToString(raw)
	}
	if data == "" && att.URI != "" && looksLikeFileURI(att.URI) {
		path := fileURIPath(att.URI)
		if mime == "" {
			mime = mimeFromPath(path, "image/png")
		}
		raw, err := readFileCapped(path, MaxInlineAttachmentBytes)
		if err != nil {
			if isTooLarge(err) {
				return resourceLinkFromPath(path, att.Name, mime), nil
			}
			return nil, err
		}
		data = base64.StdEncoding.EncodeToString(raw)
	}
	if data == "" {
		return nil, fmt.Errorf("image attachment missing data or path")
	}
	if decodedLen(data) > MaxInlineAttachmentBytes {
		return nil, fmt.Errorf("image exceeds %d bytes", MaxInlineAttachmentBytes)
	}
	if mime == "" {
		mime = "image/png"
	}
	block := map[string]interface{}{
		"type":     "image",
		"mimeType": mime,
		"data":     data,
	}
	if att.URI != "" {
		block["uri"] = att.URI
	}
	return block, nil
}

func resourceBlock(att PromptAttachment, workDir string) (map[string]interface{}, error) {
	uri := att.URI
	if uri == "" && att.Path != "" {
		uri = fileURI(resolvePath(att.Path, workDir))
	}
	if uri == "" {
		return nil, fmt.Errorf("resource attachment missing uri or path")
	}
	res := map[string]interface{}{"uri": uri}
	if att.MIMEType != "" {
		res["mimeType"] = att.MIMEType
	}
	if att.Text != "" {
		res["text"] = att.Text
	} else if att.Data != "" {
		res["blob"] = att.Data
	}
	return map[string]interface{}{
		"type":     "resource",
		"resource": res,
	}, nil
}

func resourceLinkBlock(att PromptAttachment, workDir string) (map[string]interface{}, error) {
	uri := att.URI
	name := att.Name
	mime := att.MIMEType
	if uri == "" && att.Path != "" {
		path := resolvePath(att.Path, workDir)
		uri = fileURI(path)
		if name == "" {
			name = filepath.Base(path)
		}
		if mime == "" {
			mime = mimeFromPath(path, "")
		}
	}
	if uri == "" {
		return nil, fmt.Errorf("resource_link missing uri or path")
	}
	if name == "" {
		name = uri
	}
	block := map[string]interface{}{
		"type": "resource_link",
		"uri":  uri,
		"name": name,
	}
	if mime != "" {
		block["mimeType"] = mime
	}
	return block, nil
}

func fileBlock(att PromptAttachment, workDir string) (map[string]interface{}, error) {
	if att.Path == "" && att.URI == "" {
		return nil, fmt.Errorf("file attachment missing path")
	}
	path := att.Path
	if path == "" && looksLikeFileURI(att.URI) {
		path = fileURIPath(att.URI)
	}
	if path == "" {
		return resourceLinkBlock(att, workDir)
	}
	path = resolvePath(path, workDir)
	mime := att.MIMEType
	if mime == "" {
		mime = mimeFromPath(path, "")
	}
	if isImageMIME(mime) || isImagePath(path) {
		img := att
		img.Type = "image"
		img.Path = path
		img.MIMEType = mime
		return imageBlock(img, "")
	}
	return resourceLinkFromPath(path, att.Name, mime), nil
}

func resourceLinkFromPath(path, name, mime string) map[string]interface{} {
	if name == "" {
		name = filepath.Base(path)
	}
	block := map[string]interface{}{
		"type": "resource_link",
		"uri":  fileURI(path),
		"name": name,
	}
	if mime != "" {
		block["mimeType"] = mime
	}
	return block
}

func resolvePath(path, workDir string) string {
	if path == "" {
		return path
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if workDir != "" {
		return filepath.Clean(filepath.Join(workDir, path))
	}
	return filepath.Clean(path)
}

func fileURI(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	abs = filepath.ToSlash(abs)
	if !strings.HasPrefix(abs, "/") {
		abs = "/" + abs
	}
	return (&url.URL{Scheme: "file", Path: abs}).String()
}

func looksLikeFileURI(uri string) bool {
	return strings.HasPrefix(strings.ToLower(uri), "file:")
}

func fileURIPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return strings.TrimPrefix(uri, "file://")
	}
	if u.Path != "" {
		return u.Path
	}
	return strings.TrimPrefix(uri, "file://")
}

func mimeFromPath(path, fallback string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".txt":
		return "text/plain"
	case ".md":
		return "text/markdown"
	case ".json":
		return "application/json"
	case ".go":
		return "text/x-go"
	default:
		return fallback
	}
}

func isImageMIME(mime string) bool {
	return strings.HasPrefix(strings.ToLower(mime), "image/")
}

func isImagePath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg":
		return true
	default:
		return false
	}
}

type tooLargeError struct {
	size int64
}

func (e tooLargeError) Error() string {
	return fmt.Sprintf("file exceeds %d bytes", MaxInlineAttachmentBytes)
}

func isTooLarge(err error) bool {
	_, ok := err.(tooLargeError)
	return ok
}

func readFileCapped(path string, capBytes int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > capBytes {
		return nil, tooLargeError{size: info.Size()}
	}
	return os.ReadFile(path)
}

func decodedLen(b64 string) int {
	return base64.StdEncoding.DecodedLen(len(strings.TrimSpace(b64)))
}
