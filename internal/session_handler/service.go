package session_handler

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// QueryThreads discovers all threads across all providers.
func QueryThreads() ([]ThreadInfo, error) {
	var all []ThreadInfo
	for _, kind := range ProviderKinds() {
		p, err := NewProvider(kind, "")
		if err != nil {
			continue
		}
		threads, err := queryProviderThreads(p, kind)
		if err != nil {
			continue
		}
		all = append(all, threads...)
	}
	return all, nil
}

// QueryThreadsByProvider discovers threads for a specific provider.
func QueryThreadsByProvider(kind ProviderKind) ([]ThreadInfo, error) {
	p, err := NewProvider(kind, "")
	if err != nil {
		return nil, err
	}
	return queryProviderThreads(p, kind)
}

// queryProviderThreads discovers sessions for a single provider.
// Uses a ThreadQuerier interface to avoid unsafe type assertions.
func queryProviderThreads(p Provider, kind ProviderKind) ([]ThreadInfo, error) {
	if qp, ok := p.(interface{ QueryProviderThreads() ([]ThreadInfo, error) }); ok {
		return qp.QueryProviderThreads()
	}
	return nil, nil
}

// ResolveThread resolves a thread by provider and session ID.
func ResolveThread(provider ProviderKind, sessionID string) (*ResolvedThread, error) {
	p, err := NewProvider(provider, "")
	if err != nil {
		return nil, err
	}
	return p.Resolve(sessionID)
}

// ReadThread reads all messages from a thread.
// Convenience function that resolves then reads.
func ReadThread(provider ProviderKind, sessionID string) ([]ThreadMessage, error) {
	p, err := NewProvider(provider, "")
	if err != nil {
		return nil, err
	}
	thread, err := p.Resolve(sessionID)
	if err != nil {
		return nil, err
	}
	return p.ReadMessages(thread)
}

// RenderThreadMarkdown renders thread messages as markdown.
func RenderThreadMarkdown(messages []ThreadMessage) string {
	var sb strings.Builder
	for _, msg := range messages {
		sb.WriteString(fmt.Sprintf("## %s\n\n%s\n\n", strings.Title(string(msg.Role)), msg.Text))
	}
	return sb.String()
}

// --- Provider-specific query implementations ---

func queryClaudeThreads(p *claudeProvider) ([]ThreadInfo, error) {
	projectsRoot := filepath.Join(p.rootDir, "projects")
	if _, err := os.Stat(projectsRoot); os.IsNotExist(err) {
		return nil, nil
	}

	seen := make(map[string]bool)
	var threads []ThreadInfo

	// From sessions-index.json
	filepath.Walk(projectsRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || info.Name() != "sessions-index.json" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var idx claudeSessionIndex
		if json.Unmarshal(data, &idx) != nil {
			return nil
		}
		for _, e := range idx.Entries {
			if !seen[e.SessionID] {
				seen[e.SessionID] = true
				threads = append(threads, ThreadInfo{
					SessionID: e.SessionID,
					Provider:  ProviderClaude,
					Path:      e.FullPath,
				})
			}
		}
		return nil
	})

	// From filename
	filepath.Walk(projectsRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(info.Name(), ".jsonl") {
			return nil
		}
		sid := strings.TrimSuffix(info.Name(), ".jsonl")
		if !seen[sid] {
			seen[sid] = true
			threads = append(threads, ThreadInfo{
				SessionID: sid,
				Provider:  ProviderClaude,
				Path:      path,
				ModTime:   info.ModTime(),
			})
		}
		return nil
	})

	return threads, nil
}

func queryCodexThreads(p *codexProvider) ([]ThreadInfo, error) {
	var threads []ThreadInfo

	// From SQLite state
	matches, _ := filepath.Glob(filepath.Join(p.rootDir, "state*.sqlite"))
	for _, dbPath := range matches {
		db, err := sql.Open("sqlite3", dbPath)
		if err != nil {
			continue
		}
		func() {
			defer db.Close()
			rows, err := db.Query(`SELECT id, rollout_path FROM threads`)
			if err != nil {
				return
			}
			defer rows.Close()
			for rows.Next() {
				var id, rollout string
				if rows.Scan(&id, &rollout) == nil {
					threads = append(threads, ThreadInfo{
						SessionID: id,
						Provider:  ProviderCodex,
						Path:      rollout,
					})
				}
			}
		}()
	}

	// From filesystem
	for _, dir := range []string{"sessions", "archived_sessions"} {
		dirPath := filepath.Join(p.rootDir, dir)
		filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if rolloutPattern.MatchString(info.Name()) {
				sub := rolloutPattern.FindStringSubmatch(info.Name())
				if len(sub) >= 2 {
					threads = append(threads, ThreadInfo{
						SessionID: sub[1],
						Provider:  ProviderCodex,
						Path:      path,
						ModTime:   info.ModTime(),
					})
				}
			}
			return nil
		})
	}

	return threads, nil
}

func queryCopilotThreads(p *copilotProvider) ([]ThreadInfo, error) {
	stateDir := filepath.Join(p.rootDir, "session-state")
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return nil, nil
	}

	var threads []ThreadInfo
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			eventsFile := filepath.Join(stateDir, name, "events.jsonl")
			if _, err := os.Stat(eventsFile); err == nil {
				threads = append(threads, ThreadInfo{
					SessionID: name,
					Provider:  ProviderCopilot,
					Path:      eventsFile,
				})
			}
		} else if strings.HasSuffix(name, ".jsonl") {
			sid := strings.TrimSuffix(name, ".jsonl")
			threads = append(threads, ThreadInfo{
				SessionID: sid,
				Provider:  ProviderCopilot,
				Path:      filepath.Join(stateDir, name),
			})
		}
	}
	return threads, nil
}

func queryGeminiThreads(p *geminiProvider) ([]ThreadInfo, error) {
	tmpDir := filepath.Join(p.rootDir, "tmp")
	var threads []ThreadInfo
	filepath.Walk(tmpDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.HasPrefix(info.Name(), "session-") && strings.HasSuffix(info.Name(), ".json") {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			var session struct {
				SessionID string `json:"sessionId"`
			}
			if json.Unmarshal(data, &session) == nil && session.SessionID != "" {
				threads = append(threads, ThreadInfo{
					SessionID: session.SessionID,
					Provider:  ProviderGemini,
					Path:      path,
					ModTime:   info.ModTime(),
				})
			}
		}
		return nil
	})
	return threads, nil
}

func queryAmpThreads(p *ampProvider) ([]ThreadInfo, error) {
	threadsDir := filepath.Join(p.rootDir, "threads")
	entries, err := os.ReadDir(threadsDir)
	if err != nil {
		return nil, nil
	}

	var threads []ThreadInfo
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			sid := strings.TrimSuffix(e.Name(), ".json")
			threads = append(threads, ThreadInfo{
				SessionID: sid,
				Provider:  ProviderAmp,
				Path:      filepath.Join(threadsDir, e.Name()),
			})
		}
	}
	return threads, nil
}

func queryKimiThreads(p *kimiProvider) ([]ThreadInfo, error) {
	sessionsDir := filepath.Join(p.rootDir, "sessions")
	var threads []ThreadInfo
	filepath.Walk(sessionsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || info.Name() != "context.jsonl" {
			return nil
		}
		sid := filepath.Base(filepath.Dir(path))
		threads = append(threads, ThreadInfo{
			SessionID: sid,
			Provider:  ProviderKimi,
			Path:      path,
			ModTime:   info.ModTime(),
		})
		return nil
	})
	return threads, nil
}

func queryPiThreads(p *piProvider) ([]ThreadInfo, error) {
	sessionsDir := filepath.Join(p.rootDir, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return nil, nil
	}

	var threads []ThreadInfo
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			path := filepath.Join(sessionsDir, e.Name())
			if sid := extractPiSessionID(path); sid != "" {
				threads = append(threads, ThreadInfo{
					SessionID: sid,
					Provider:  ProviderPi,
					Path:      path,
					ModTime:   time.Now(), // Pi entries don't expose ModTime from DirEntry
				})
			}
		}
	}
	return threads, nil
}

func queryCursorThreads(p *cursorProvider) ([]ThreadInfo, error) {
	chatsDir := filepath.Join(p.rootDir, "chats")
	var threads []ThreadInfo
	filepath.Walk(chatsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || info.Name() != "store.db" {
			return nil
		}
		dir := filepath.Dir(path)
		sid := filepath.Base(dir)
		threads = append(threads, ThreadInfo{
			SessionID: sid,
			Provider:  ProviderCursor,
			Path:      path,
			ModTime:   info.ModTime(),
		})
		return nil
	})
	return threads, nil
}

// extractPiSessionID reads the session header from a Pi JSONL file.
func extractPiSessionID(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for i := 0; i < 20 && scanner.Scan(); i++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if json.Unmarshal([]byte(line), &entry) == nil && entry.Type == "session" && entry.ID != "" {
			return entry.ID
		}
	}
	return ""
}
