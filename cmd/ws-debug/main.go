package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/gorilla/websocket"
)

// Message structures
type ClientMessage struct {
	Command   string                 `json:"command"`
	Data      map[string]interface{} `json:"data"`
	Timestamp int64                  `json:"timestamp"`
}

type ServerMessage struct {
	Type      string                 `json:"type"`
	Data      map[string]interface{} `json:"data"`
	Timestamp int64                  `json:"timestamp"`
}

// LogEntry - Single log entry
type LogEntry struct {
	Time      string
	Direction string // → sent, ← received
	Type      string
	Content   string
}

// DebugApp - TUI application
type DebugApp struct {
	app           *tview.Application
	wsLogList     *tview.List
	cliLogList    *tview.List
	statusText    *tview.TextView
	wsMessages    []LogEntry
	cliMessages   []LogEntry
	conn          *websocket.Conn
	connected     bool
	url           string
	questID       string
	autoScroll    bool
}

// NewDebugApp - Create new debug app
func NewDebugApp(url, questID string) *DebugApp {
	return &DebugApp{
		app:        tview.NewApplication(),
		wsLogList:  tview.NewList(),
		cliLogList: tview.NewList(),
		statusText: tview.NewTextView(),
		url:        url,
		questID:    questID,
		autoScroll: true,
	}
}

// Run - Start the application
func (d *DebugApp) Run() error {
	d.setupUI()
	d.setStatus("Connecting...", tcell.ColorYellow)
	
	// Connect in background
	go func() {
		if err := d.connect(); err != nil {
			d.setStatus(fmt.Sprintf("Connection failed: %v", err), tcell.ColorRed)
		} else {
			d.setStatus("Connected", tcell.ColorGreen)
			go d.readLoop()
			go d.heartbeatLoop()
		}
	}()

	return d.app.Run()
}

func (d *DebugApp) setupUI() {
	// WebSocket log panel (left)
	wsLogBox := tview.NewBox().SetBorder(true).SetTitle(" WebSocket Messages (WebSocket 消息) ")
	d.wsLogList.SetBorder(false)
	d.wsLogList.SetHighlightFullLine(true)
	d.wsLogList.SetSelectedBackgroundColor(tcell.ColorDarkCyan)
	
	wsLogFlex := tview.NewFlex().SetDirection(tview.FlexRow)
	wsLogFlex.AddItem(d.wsLogList, 0, 1, false)
	wsLogFlex.AddItem(wsLogBox, 0, 1, false)

	// CLI log panel (right)
	cliLogBox := tview.NewBox().SetBorder(true).SetTitle(" CLI Input/Output (CLI 输入输出) ")
	d.cliLogList.SetBorder(false)
	d.cliLogList.SetHighlightFullLine(true)
	d.cliLogList.SetSelectedBackgroundColor(tcell.ColorDarkGreen)
	
	cliLogFlex := tview.NewFlex().SetDirection(tview.FlexRow)
	cliLogFlex.AddItem(d.cliLogList, 0, 1, false)
	cliLogFlex.AddItem(cliLogBox, 0, 1, false)

	// Status bar
	d.statusText.SetDynamicColors(true)
	d.statusText.SetBorder(true).SetTitle(" Status (状态) ")
	d.statusText.SetText("[yellow]Connecting...[-]")

	// Main layout
	mainFlex := tview.NewFlex().SetDirection(tview.FlexColumn)
	mainFlex.AddItem(wsLogFlex, 0, 1, false)
	mainFlex.AddItem(cliLogFlex, 0, 1, false)

	// Bottom panel with controls
	controlsText := tview.NewTextView()
	controlsText.SetText(`[yellow]Controls:[-]
  [green]s[-] Send message  [green]h[-] Send heartbeat  [green]c[-] Clear logs
  [green]a[-] Toggle auto-scroll  [green]q[-] Quit`)
	controlsText.SetBorder(true).SetTitle(" Controls (控制) ")

	// Input field
	inputField := tview.NewInputField()
	inputField.SetLabel("Send: ")
	inputField.SetFieldWidth(50)
	inputField.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEnter {
			text := inputField.GetText()
			if text != "" {
				d.sendMessage("send_input", map[string]interface{}{"content": text})
				inputField.SetText("")
			}
		}
	})

	// Input layout
	inputFlex := tview.NewFlex().SetDirection(tview.FlexRow)
	inputFlex.AddItem(controlsText, 4, 0, false)
	inputFlex.AddItem(inputField, 1, 0, true)

	// Full layout
	fullFlex := tview.NewFlex().SetDirection(tview.FlexRow)
	fullFlex.AddItem(mainFlex, 0, 1, false)
	fullFlex.AddItem(inputFlex, 6, 0, false)
	fullFlex.AddItem(d.statusText, 3, 0, false)

	// Keyboard shortcuts
	fullFlex.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Rune() {
		case 'q':
			d.app.Stop()
			return nil
		case 'c':
			d.clearLogs()
			return nil
		case 'a':
			d.autoScroll = !d.autoScroll
			return nil
		case 'h':
			d.sendMessage("heartbeat", map[string]interface{}{"quest_id": d.questID})
			return nil
		case 's':
			d.app.SetFocus(inputField)
			return nil
		}
		return event
	})

	d.app.SetRoot(fullFlex, true)
}

func (d *DebugApp) connect() error {
	var err error
	d.conn, _, err = websocket.DefaultDialer.Dial(d.url, nil)
	if err != nil {
		return err
	}

	d.connected = true
	d.conn.SetPongHandler(func(string) error {
		d.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	return nil
}

func (d *DebugApp) readLoop() {
	for {
		_, message, err := d.conn.ReadMessage()
		if err != nil {
			d.app.QueueUpdateDraw(func() {
				d.setStatus(fmt.Sprintf("Disconnected: %v", err), tcell.ColorRed)
				d.connected = false
			})
			return
		}

		var msg ServerMessage
		if err := json.Unmarshal(message, &msg); err == nil {
			d.addWSLog("←", msg.Type, formatData(msg.Data))
			
			// Add to CLI log if it's output
			if msg.Type == "output" || msg.Type == "chunk" || msg.Type == "error" {
				if content, ok := msg.Data["content"].(string); ok {
					d.addCLILog("←", msg.Type, content)
				}
			}
		} else {
			d.addWSLog("←", "raw", string(message))
		}
	}
}

func (d *DebugApp) heartbeatLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if d.connected {
			d.sendMessage("heartbeat", map[string]interface{}{"quest_id": d.questID})
		}
	}
}

func (d *DebugApp) sendMessage(cmd string, data map[string]interface{}) {
	if !d.connected || d.conn == nil {
		return
	}

	msg := ClientMessage{
		Command:   cmd,
		Data:      data,
		Timestamp: time.Now().UnixMilli(),
	}

	jsonData, _ := json.Marshal(msg)
	if err := d.conn.WriteMessage(websocket.TextMessage, jsonData); err != nil {
		d.addWSLog("→", cmd, fmt.Sprintf("Error: %v", err))
		return
	}

	d.addWSLog("→", cmd, formatData(data))
	
	// If it's user input, also add to CLI log
	if cmd == "send_input" {
		if content, ok := data["content"].(string); ok {
			d.addCLILog("→", "input", content)
		}
	}
}

func (d *DebugApp) addWSLog(direction, msgType, content string) {
	d.app.QueueUpdateDraw(func() {
		entry := LogEntry{
			Time:      time.Now().Format("15:04:05"),
			Direction: direction,
			Type:      msgType,
			Content:   truncate(content, 80),
		}
		d.wsMessages = append(d.wsMessages, entry)
		
		color := tcell.ColorWhite
		if direction == "→" {
			color = tcell.ColorBlue
		} else {
			color = tcell.ColorGreen
		}
		
		d.wsLogList.AddItem(
			fmt.Sprintf("[%s]%s [%s]%s[-] %s", 
				color, direction, tcell.ColorYellow, msgType, truncate(content, 60)),
			"",
			0,
			nil,
		)
		
		if d.autoScroll && d.wsLogList.GetItemCount() > 20 {
			d.wsLogList.RemoveItem(0)
		}
	})
}

func (d *DebugApp) addCLILog(direction, msgType, content string) {
	d.app.QueueUpdateDraw(func() {
		entry := LogEntry{
			Time:      time.Now().Format("15:04:05"),
			Direction: direction,
			Type:      msgType,
			Content:   truncate(content, 80),
		}
		d.cliMessages = append(d.cliMessages, entry)
		
		color := tcell.ColorWhite
		if direction == "→" {
			color = tcell.ColorBlue
		} else {
			color = tcell.ColorGreen
		}
		
		d.cliLogList.AddItem(
			fmt.Sprintf("[%s]%s [%s]%s[-] %s", 
				color, direction, tcell.ColorYellow, msgType, truncate(content, 60)),
			"",
			0,
			nil,
		)
		
		if d.autoScroll && d.cliLogList.GetItemCount() > 20 {
			d.cliLogList.RemoveItem(0)
		}
	})
}

func (d *DebugApp) setStatus(text string, color tcell.Color) {
	d.statusText.SetText(fmt.Sprintf("[%s] %s [-]", color, text))
}

func (d *DebugApp) clearLogs() {
	d.app.QueueUpdateDraw(func() {
		d.wsLogList.Clear()
		d.cliLogList.Clear()
		d.wsMessages = []LogEntry{}
		d.cliMessages = []LogEntry{}
	})
}

func formatData(data map[string]interface{}) string {
	if len(data) == 0 {
		return "{}"
	}
	
	parts := []string{}
	for k, v := range data {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func main() {
	url := flag.String("url", "ws://localhost:8765/ws", "WebSocket URL")
	questID := flag.String("quest-id", "debug", "Quest ID")
	flag.Parse()

	fmt.Println("🔍 Starting WebSocket Debug TUI...")
	fmt.Printf("URL: %s\n", *url)
	fmt.Printf("Quest ID: %s\n", *questID)
	fmt.Println("")
	fmt.Println("Controls:")
	fmt.Println("  s - Send message")
	fmt.Println("  h - Send heartbeat")
	fmt.Println("  c - Clear logs")
	fmt.Println("  a - Toggle auto-scroll")
	fmt.Println("  q - Quit")
	fmt.Println("")

	app := NewDebugApp(*url, *questID)
	if err := app.Run(); err != nil {
		log.Fatalf("Failed to run: %v", err)
	}
}
