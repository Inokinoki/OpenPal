package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

// Message - WebSocket message structure
type Message struct {
	Command   string                 `json:"command"`
	Data      map[string]interface{} `json:"data"`
	Timestamp int64                  `json:"timestamp"`
	Seq       int64                  `json:"seq,omitempty"`
	Type      string                 `json:"type,omitempty"`
}

// Config - CLI configuration
type Config struct {
	URL         string
	QuestID     string
	Provider    string
	Task        string
	Interactive bool
	Verbose     bool
	TestMode    bool
	Duration    int
}

// Output buffer for accumulating chunks
type OutputBuffer struct {
	mu       sync.Mutex
	buffer   strings.Builder
	lastType string
	shown    bool // Track if icon already shown
	timer    *time.Timer
}

var outputBuf = &OutputBuffer{}

// flushAfter - 延迟 flush 时间（毫秒）
const flushAfter = 50 * time.Millisecond

func main() {
	// Parse flags
	url := flag.String("url", "ws://localhost:8765/ws", "WebSocket server URL")
	questID := flag.String("quest-id", "test-quest", "Quest ID")
	provider := flag.String("provider", "claude", "AI provider")
	task := flag.String("task", "", "Task description")
	sessionID := flag.String("session-id", "", "ACP session ID to resume")
	interactive := flag.Bool("i", false, "Interactive mode")
	verbose := flag.Bool("v", false, "Verbose output (show raw messages)")
	testMode := flag.Bool("test", false, "Test mode (run automated tests)")
	duration := flag.Int("duration", 30, "Test duration in seconds (default: 30)")
	flag.Parse()

	config := Config{
		URL:         *url,
		QuestID:     *questID,
		Provider:    *provider,
		Task:        *task,
		Interactive: *interactive,
		Verbose:     *verbose,
		TestMode:    *testMode,
		Duration:    *duration,
	}

	// Connect to WebSocket
	conn, _, err := websocket.DefaultDialer.Dial(config.URL, nil)
	if err != nil {
		log.Fatalf("❌ Failed to connect: %v", err)
	}
	defer conn.Close()

	fmt.Printf("✅ Connected to %s (quest: %s)\n", config.URL, config.QuestID)

	// Handle interrupts
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, syscall.SIGINT, syscall.SIGTERM)

	// Send heartbeat every 5 seconds
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			msg := Message{
				Command:   "heartbeat",
				Timestamp: time.Now().UnixMilli(),
				Data: map[string]interface{}{
					"quest_id": config.QuestID,
				},
			}
			if err := conn.WriteJSON(msg); err != nil && config.Verbose {
				log.Printf("❌ Heartbeat failed: %v", err)
			}
		}
	}()

	// Read loop
	go func() {
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				if config.Verbose {
					log.Printf("❌ Read error: %v", err)
				}
				return
			}

			// Parse message
			var msg Message
			if err := json.Unmarshal(message, &msg); err != nil {
				if config.Verbose {
					fmt.Printf("📥 %s\n", string(message))
				}
				continue
			}

			// Handle message
			handleMessage(msg, config.Verbose)
		}
	}()

	// Send initial task if provided
		if config.Task != "" {
		fmt.Printf("📤 Sending: %s\n", config.Task)
		data := map[string]interface{}{
			"task": config.Task,
		}
		if *sessionID != "" {
			data["session_id"] = *sessionID
		}
		msg := Message{
			Command:   "start_task",
			Timestamp: time.Now().UnixMilli(),
			Data:      data,
		}
		if err := conn.WriteJSON(msg); err != nil {
			log.Printf("❌ Send failed: %v", err)
		}
	}

	// Interactive mode or wait
	if config.Interactive {
		fmt.Printf("💬 Interactive mode (Ctrl+C to exit)\n")
		reader := bufio.NewReader(os.Stdin)
		for {
			select {
			case <-interrupt:
				flushBuffer()
				fmt.Printf("\n👋 Disconnecting...\n")
				return
			default:
				input, err := reader.ReadString('\n')
				if err != nil {
					continue
				}
				input = strings.TrimSpace(input)
				if input == "" {
					continue
				}

				flushBuffer()
				fmt.Printf("📤 %s\n", input)

				msg := Message{
					Command:   "send_input",
					Timestamp: time.Now().UnixMilli(),
					Data: map[string]interface{}{
						"content": input,
					},
				}
				if err := conn.WriteJSON(msg); err != nil {
					log.Printf("❌ Send failed: %v", err)
					continue
				}
			}
		}
	} else {
		fmt.Printf("⏳ Waiting for %d seconds (Ctrl+C to exit)...\n", config.Duration)
		<-interrupt
		flushBuffer()
		fmt.Printf("\n👋 Disconnecting...\n")
	}
}

func stringField(data map[string]interface{}, key string) (string, bool) {
	if data == nil {
		return "", false
	}
	s, ok := data[key].(string)
	return s, ok && s != ""
}

func handleLegacyACPChunk(data map[string]interface{}, verbose bool) {
	method, _ := data["method"].(string)
	if method == "session/update" {
		params, _ := data["params"].(map[string]interface{})
		update, _ := params["update"].(map[string]interface{})
		if update == nil {
			update = params
		}
		updateType, _ := update["sessionUpdate"].(string)
		content, _ := update["content"].(map[string]interface{})
		text, _ := content["text"].(string)
		switch updateType {
		case "agent_message_chunk":
			if text != "" {
				outputBuf.add(text, "message")
			}
		case "agent_thought_chunk":
			if verbose && text != "" {
				outputBuf.add(text, "thought")
			}
		}
		return
	}
	if _, ok := data["result"]; ok {
		flushBuffer()
		outputBuf.mu.Lock()
		outputBuf.shown = false
		outputBuf.lastType = ""
		outputBuf.mu.Unlock()
		fmt.Printf("\n")
	}
}

// handleMessage - Handle received messages (only show agent events)
func handleMessage(msg Message, verbose bool) {
	switch msg.Type {
	case "status":
		return

	case "heartbeat_ack":
		return

	case "chunk", "thinking":
		if text, ok := stringField(msg.Data, "content"); ok && text != "" {
			kind := "message"
			if msg.Type == "thinking" {
				kind = "thought"
			}
			outputBuf.add(text, kind)
			break
		}
		// Legacy raw ACP JSON-RPC forwarded as a chunk
		if data, ok := msg.Data["jsonrpc"].(string); ok && data == "2.0" {
			handleLegacyACPChunk(msg.Data, verbose)
		}

	case "result":
		flushBuffer()
		outputBuf.mu.Lock()
		outputBuf.shown = false
		outputBuf.lastType = ""
		outputBuf.mu.Unlock()
		fmt.Printf("\n")
		if verbose {
			fmt.Printf("✅ Complete %v\n", msg.Data["result"])
		}

	case "permission_request":
		flushBuffer()
		fmt.Printf("🔐 Permission requested: %v\n", msg.Data["tool_call"])
		fmt.Printf("   Reply with WebSocket command approve or reject\n")

	case "tool_call", "tool_call_update":
		if verbose {
			flushBuffer()
			fmt.Printf("🔧 %s: %v\n", msg.Type, msg.Data["content"])
		}

	case "error":
		flushBuffer()
		if data, ok := msg.Data["message"].(string); ok {
			fmt.Printf("❌ Error: %s\n", data)
		} else if data, ok := msg.Data["content"].(string); ok {
			fmt.Printf("❌ Error: %s\n", data)
		} else {
			fmt.Printf("❌ Error: %v\n", msg.Data)
		}

	default:
		if verbose {
			flushBuffer()
			fmt.Printf("📨 %s: %v\n", msg.Type, msg.Data)
		}
	}
}

// add text to buffer and schedule flush
// Optimized: minimize mutex hold time, avoid redundant operations
// Fixed: Proper timer cancellation to prevent race conditions
func (b *OutputBuffer) add(text, typ string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Fast path: type changed, flush previous buffer
	if b.lastType != "" && b.lastType != typ {
		if b.buffer.Len() > 0 {
			b.doFlush()
		}
		fmt.Printf("\n")
		b.shown = false // Reset for new type
	}

	// Append text using strings.Builder (efficient concatenation)
	b.buffer.WriteString(text)
	b.lastType = typ

	// Cancel existing timer (avoid multiple concurrent timers)
	// Fixed: Properly drain timer channel to prevent race condition
	if b.timer != nil && !b.timer.Stop() {
		// Timer already fired, drain the channel to prevent stale callback
		select {
		case <-b.timer.C:
		default:
		}
	}

	// Schedule flush after delay (debounce rapid chunks)
	b.timer = time.AfterFunc(flushAfter, func() {
		b.mu.Lock()
		if b.buffer.Len() > 0 {
			b.doFlush()
		}
		b.mu.Unlock()
	})
}

// flush buffer to output
func flushBuffer() {
	outputBuf.mu.Lock()
	defer outputBuf.mu.Unlock()
	if outputBuf.timer != nil {
		outputBuf.timer.Stop()
	}
	outputBuf.doFlush()
}

func (b *OutputBuffer) doFlush() {
	if b.buffer.Len() == 0 {
		return
	}

	// Get buffer content once
	content := b.buffer.String()

	// Print icon only once at the start
	if !b.shown {
		if b.lastType == "message" {
			fmt.Printf("🤖 %s", content)
		} else if b.lastType == "thought" {
			fmt.Printf("💭 %s", content)
		}
		b.shown = true
	} else {
		// Subsequent chunks - just print text
		fmt.Printf("%s", content)
	}

	// Flush stdout immediately for streaming effect
	os.Stdout.Sync()

	// Reset buffer (reuse the same builder to avoid reallocation)
	b.buffer.Reset()
}
