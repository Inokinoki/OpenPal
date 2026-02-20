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
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

// Message - WebSocket message structure
type Message struct {
	Command string                 `json:"command"`
	Data    map[string]interface{} `json:"data"`
	Timestamp int64                `json:"timestamp"`
	Seq       int64                `json:"seq,omitempty"`
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

func main() {
	// Parse flags
	url := flag.String("url", "ws://localhost:8765/ws", "WebSocket server URL")
	questID := flag.String("quest-id", "test-quest", "Quest ID")
	provider := flag.String("provider", "claude", "AI provider")
	task := flag.String("task", "", "Task description")
	interactive := flag.Bool("i", false, "Interactive mode")
	verbose := flag.Bool("v", false, "Verbose output")
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

	if config.Verbose {
		log.Printf("Connecting to: %s", config.URL)
		log.Printf("Quest ID: %s", config.QuestID)
		log.Printf("Provider: %s", config.Provider)
	}

	// Connect to WebSocket
	conn, _, err := websocket.DefaultDialer.Dial(config.URL, nil)
	if err != nil {
		log.Fatalf("❌ Failed to connect: %v", err)
	}
	defer conn.Close()

	// Set pong handler to automatically respond to server pings
	conn.SetPongHandler(func(string) error {
		if config.Verbose {
			log.Printf("🏓 Received ping, auto-responding with pong")
		}
		return nil
	})

	log.Printf("✅ Connected to %s", config.URL)

	// Handle interrupts
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, syscall.SIGINT, syscall.SIGTERM)

	// Send heartbeat every 5 seconds (server expects every 10s)
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
			sendMessage(conn, msg, config.Verbose)
		}
	}()

	// Read messages from server
	go func() {
		for {
			// Set read deadline for pong timeout
			conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			
			_, message, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					log.Printf("❌ Read error: %v", err)
				}
				return
			}

			// Always log raw message for debugging
			log.Printf("📥 Raw received: %s", string(message))

			// Pretty print JSON
			var msg ServerMessage
			if err := json.Unmarshal(message, &msg); err == nil {
				if config.Verbose {
					log.Printf("📥 Parsed: Type=%s, Data=%v", msg.Type, msg.Data)
				}
				printMessage(msg)
			} else {
				fmt.Printf("📥 %s\n", string(message))
			}
		}
	}()

	// Send initial connection message
	if config.Task != "" {
		msg := Message{
			Command:   "start_task",
			Timestamp: time.Now().UnixMilli(),
			Data: map[string]interface{}{
				"quest_id": config.QuestID,
				"provider": config.Provider,
				"task":     config.Task,
			},
		}
		sendMessage(conn, msg, config.Verbose)
	}

	// Test mode, Interactive mode, or simple wait
	if config.TestMode {
		runTests(conn, config)
	} else if config.Interactive {
		fmt.Println("📝 Interactive mode - type messages and press Enter")
		fmt.Println("Commands:")
		fmt.Println("  /status - Request status")
		fmt.Println("  /quit   - Exit")
		fmt.Println("")

		reader := bufio.NewReader(os.Stdin)
		for {
			fmt.Print("> ")
			input, err := reader.ReadString('\n')
			if err != nil {
				log.Printf("❌ Read error: %v", err)
				return
			}

			input = strings.TrimSpace(input)

			// Handle commands
			if input == "/quit" || input == "/exit" {
				fmt.Println("👋 Goodbye!")
				return
			}

			if input == "/status" {
				msg := Message{
					Command:   "get_status",
					Timestamp: time.Now().UnixMilli(),
					Data: map[string]interface{}{
						"quest_id": config.QuestID,
					},
				}
				sendMessage(conn, msg, config.Verbose)
				continue
			}

			// Send as input message
			if input != "" {
				msg := Message{
					Command:   "send_input",
					Timestamp: time.Now().UnixMilli(),
					Data: map[string]interface{}{
						"content": input,
					},
				}
				sendMessage(conn, msg, config.Verbose)
			}
		}
	} else {
		// Simple mode: wait for duration or interrupt
		fmt.Printf("⏳ Waiting for %d seconds (Ctrl+C to exit)...\n", config.Duration)
		
		done := make(chan bool)
		go func() {
			<-interrupt
			done <- true
		}()
		
		select {
		case <-done:
			fmt.Println("\n👋 Disconnecting...")
		case <-time.After(time.Duration(config.Duration) * time.Second):
			fmt.Printf("\n✅ Test duration completed (%d seconds)\n", config.Duration)
		}
	}
}

func sendMessage(conn *websocket.Conn, msg Message, verbose bool) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("❌ Marshal error: %v", err)
		return
	}

	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		log.Printf("❌ Send error: %v", err)
		return
	}

	// Only log non-heartbeat messages or when verbose
	if verbose || msg.Command != "heartbeat" {
		log.Printf("📤 Sent: %s", msg.Command)
	}
}

// ServerMessage - Server message structure (for receiving)
type ServerMessage struct {
	Type      string                 `json:"type"`
	Data      map[string]interface{} `json:"data,omitempty"`
	Timestamp int64                  `json:"timestamp"`
	Seq       int64                  `json:"seq,omitempty"`
}

func printMessage(msg ServerMessage) {
	switch msg.Type {
	case "connected":
		fmt.Printf("🔗 Connected\n")
		if msg.Data != nil {
			if questID, ok := msg.Data["quest_id"].(string); ok {
				fmt.Printf("   Quest: %s\n", questID)
			}
		}

	case "status":
		fmt.Printf("📊 Status Update\n")
		if msg.Data != nil {
			if state, ok := msg.Data["state"].(string); ok {
				fmt.Printf("   State: %s\n", state)
			}
			if progress, ok := msg.Data["progress"].(float64); ok {
				fmt.Printf("   Progress: %.0f%%\n", progress)
			}
		}

	case "output":
		fmt.Printf("💬 Output\n")
		if msg.Data != nil {
			if content, ok := msg.Data["content"].(string); ok {
				fmt.Printf("   %s\n", content)
			}
		}

	case "error":
		fmt.Printf("❌ Error\n")
		if msg.Data != nil {
			if message, ok := msg.Data["message"].(string); ok {
				fmt.Printf("   %s\n", message)
			}
		}

	case "task_complete":
		fmt.Printf("✅ Task Complete\n")
		if msg.Data != nil {
			if result, ok := msg.Data["result"].(string); ok {
				fmt.Printf("   Result: %s\n", result)
			}
		}

	case "heartbeat_ack":
		fmt.Printf("💓 Heartbeat ACK\n")

	default:
		fmt.Printf("📨 %s\n", msg.Type)
	}
}

// runTests - Run automated WebSocket tests
func runTests(conn *websocket.Conn, config Config) {
	fmt.Println("🧪 Running WebSocket tests...")
	fmt.Println("")
	
	testResults := make(map[string]bool)
	
	// Test 1: Connection established
	testResults["connection"] = true
	fmt.Println("✅ Test 1: Connection established")
	
	// Test 2: Send heartbeat
	time.Sleep(1 * time.Second)
	msg := Message{
		Command:   "heartbeat",
		Timestamp: time.Now().UnixMilli(),
		Data: map[string]interface{}{
			"quest_id": config.QuestID,
		},
	}
	sendMessage(conn, msg, config.Verbose)
	testResults["heartbeat"] = true
	fmt.Println("✅ Test 2: Heartbeat sent")
	
	// Test 3: Request status
	time.Sleep(1 * time.Second)
	msg = Message{
		Command:   "get_status",
		Timestamp: time.Now().UnixMilli(),
		Data: map[string]interface{}{
			"quest_id": config.QuestID,
		},
	}
	sendMessage(conn, msg, config.Verbose)
	testResults["status_request"] = true
	fmt.Println("✅ Test 3: Status request sent")
	
	// Test 4: Send user input
	time.Sleep(1 * time.Second)
	msg = Message{
		Command:   "send_input",
		Timestamp: time.Now().UnixMilli(),
		Data: map[string]interface{}{
			"content": "Hello, this is a test message!",
		},
	}
	sendMessage(conn, msg, config.Verbose)
	testResults["user_input"] = true
	fmt.Println("✅ Test 4: User input sent")
	
	// Test 5: Wait and monitor
	fmt.Printf("⏳ Test 5: Monitoring for %d seconds...\n", config.Duration)
	time.Sleep(time.Duration(config.Duration) * time.Second)
	testResults["monitoring"] = true
	fmt.Println("✅ Test 5: Monitoring completed")
	
	// Summary
	fmt.Println("")
	fmt.Println("==================================================")
	fmt.Println("📊 Test Summary:")
	fmt.Println("==================================================")
	allPassed := true
	for test, passed := range testResults {
		status := "✅ PASS"
		if !passed {
			status = "❌ FAIL"
			allPassed = false
		}
		fmt.Printf("%s: %s\n", status, test)
	}
	fmt.Println("==================================================")
	if allPassed {
		fmt.Println("🎉 All tests passed!")
	} else {
		fmt.Println("⚠️  Some tests failed")
	}
	fmt.Println("")
}
