package server

import (
	"openpal/internal/adapter"
)

func parseClientInput(entryType string, data map[string]interface{}) InputMessage {
	msg := InputMessage{Type: entryType}
	if data == nil {
		return msg
	}

	if entryType == "task" {
		msg.Content = stringField(data, "task")
		if msg.Content == "" {
			msg.Content = stringField(data, "content")
		}
	} else {
		msg.Content = stringField(data, "content")
		if msg.Content == "" {
			msg.Content = stringField(data, "task")
		}
	}

	msg.SessionID = stringField(data, "session_id")
	if msg.SessionID == "" {
		msg.SessionID = stringField(data, "sessionId")
	}

	if raw, ok := data["attachments"]; ok {
		atts, err := adapter.ParseAttachments(raw)
		if err == nil {
			msg.Attachments = atts
		}
	}

	if raw, ok := data["mcp_servers"]; ok {
		servers, err := adapter.ParseMCPServers(raw)
		if err == nil {
			msg.MCPServers = servers
			msg.HasMCPServers = true
		}
	} else if raw, ok := data["mcpServers"]; ok {
		servers, err := adapter.ParseMCPServers(raw)
		if err == nil {
			msg.MCPServers = servers
			msg.HasMCPServers = true
		}
	}

	return msg
}

func stringField(data map[string]interface{}, key string) string {
	s, _ := data[key].(string)
	return s
}
