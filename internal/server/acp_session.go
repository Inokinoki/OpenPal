package server

import (
	"os"
	"path/filepath"
	"strings"
)

const acpSessionIDFile = "acp_session_id"

func acpSessionIDPath(sessionDir, taskID string) string {
	return filepath.Join(sessionDir, taskID, acpSessionIDFile)
}

func readACPSessionID(sessionDir, taskID string) string {
	data, err := os.ReadFile(acpSessionIDPath(sessionDir, taskID))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func writeACPSessionID(sessionDir, taskID, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	dir := filepath.Join(sessionDir, taskID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(acpSessionIDPath(sessionDir, taskID), []byte(sessionID+"\n"), 0644)
}
