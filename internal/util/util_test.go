package util

import (
	"encoding/json"
	"sync"
	"testing"
)

// ============== Debug Logger Tests ==============

// TestDebugLog tests debug logging functionality
func TestDebugLog(t *testing.T) {
	// Capture log output
	var logged bool
	originalLog := debugLog

	// Set custom logger for testing
	debugLog = func(format string, args ...interface{}) {
		logged = true
	}
	defer func() { debugLog = originalLog }()

	// Call DebugLog
	DebugLog("test message: %v", 42)

	if !logged {
		t.Error("Expected debugLog to be called")
	}
}

// TestSetDebugLog tests setting custom debug logger
func TestSetDebugLog(t *testing.T) {
	var called bool
	customLogger := func(format string, args ...interface{}) {
		called = true
	}

	// Save original
	originalLog := debugLog
	defer func() { debugLog = originalLog }()

	// Set custom logger
	SetDebugLog(customLogger)

	// Use it
	DebugLog("test")

	if !called {
		t.Error("Expected custom logger to be called")
	}
}

// ============== Line Buffer Pool Tests ==============

// TestGetPutLineBuffer tests line buffer pool get/put
func TestGetPutLineBuffer(t *testing.T) {
	// Get buffer from pool
	buf := GetLineBuffer()

	if buf == nil {
		t.Fatal("Expected non-nil buffer")
	}

	if cap(*buf) != 4096 {
		t.Errorf("Expected buffer capacity 4096, got %d", cap(*buf))
	}

	// Use buffer
	*buf = append(*buf, []byte("test data")...)

	if len(*buf) != 9 {
		t.Errorf("Expected length 9, got %d", len(*buf))
	}

	// Return to pool
	PutLineBuffer(buf)

	// Verify reset
	if len(*buf) != 0 {
		t.Error("Expected buffer length to be reset after PutLineBuffer")
	}
}

// TestLineBufferPoolReuse tests line buffer pool reuse
func TestLineBufferPoolReuse(t *testing.T) {
	// Get buffer, use it, return it
	buf1 := GetLineBuffer()
	*buf1 = append(*buf1, []byte("first use")...)
	PutLineBuffer(buf1)

	// Get again - should be same buffer (from pool)
	buf2 := GetLineBuffer()

	// Length should be reset but capacity preserved
	if len(*buf2) != 0 {
		t.Error("Expected reused buffer to have length 0")
	}

	if cap(*buf2) != 4096 {
		t.Error("Expected reused buffer to preserve capacity")
	}

	PutLineBuffer(buf2)
}

// ============== ParseTextOutput Tests ==============

// TestParseTextOutput tests text output parsing
func TestParseTextOutput(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantType string
	}{
		{"empty", "", "chunk"},
		{"plain_text", "Hello world", "chunk"},
		{"code_block_start", "```go", "code_block"},
		{"code_block_end", "```", "code_block"},
		{"editing_file", "Editing main.go", "file_operation"},
		{"creating_file", "Creating new file", "file_operation"},
		{"reading_file", "Reading config.json", "file_operation"},
		{"deleting_file", "Deleting old file", "file_operation"},
		{"running_command", "Running npm install", "command"},
		{"executing_command", "Executing git status", "command"},
		{"mixed_case_editing", "EDITING file.txt", "file_operation"},
		{"mixed_case_running", "RUNNING tests", "command"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseTextOutput(tt.input)

			if result == nil {
				t.Fatal("Expected non-nil result")
			}

			if result["type"] != tt.wantType {
				t.Errorf("Expected type=%s, got %v", tt.wantType, result["type"])
			}

			if result["content"] != tt.input {
				t.Errorf("Expected content=%q, got %v", tt.input, result["content"])
			}
		})
	}
}

// TestParseTextOutputContentExtraction tests content extraction
func TestParseTextOutputContentExtraction(t *testing.T) {
	input := "This is test content"
	result := ParseTextOutput(input)

	content, ok := result["content"].(string)
	if !ok {
		t.Fatal("Expected content to be string")
	}

	if content != input {
		t.Errorf("Expected content %q, got %q", input, content)
	}
}

// ============== CloneMap Tests ==============

// TestCloneMap tests map cloning
func TestCloneMap(t *testing.T) {
	original := map[string]interface{}{
		"key1": "value1",
		"key2": 42.0, // Use float64 for JSON compatibility
		"key3": true,
	}

	cloned := CloneMap(original)

	// Verify cloned content
	if cloned["key1"] != "value1" {
		t.Errorf("Expected key1=value1, got %v", cloned["key1"])
	}
	if cloned["key2"] != 42.0 {
		t.Errorf("Expected key2=42.0, got %v", cloned["key2"])
	}
	if cloned["key3"] != true {
		t.Errorf("Expected key3=true, got %v", cloned["key3"])
	}

	// Verify independent copies
	cloned["key1"] = "modified"
	if original["key1"] == "modified" {
		t.Error("Expected original to be unchanged after clone modification")
	}
}

// TestCloneMapNil tests cloning nil map
func TestCloneMapNil(t *testing.T) {
	var nilMap map[string]interface{}
	result := CloneMap(nilMap)

	if result != nil {
		t.Error("Expected nil result for nil input")
	}
}

// TestCloneMapEmpty tests cloning empty map
func TestCloneMapEmpty(t *testing.T) {
	empty := make(map[string]interface{})
	result := CloneMap(empty)

	if result == nil {
		t.Error("Expected non-nil result for empty map")
	}

	if len(result) != 0 {
		t.Errorf("Expected empty result, got %d elements", len(result))
	}
}

// TestCloneMapNested tests cloning nested maps
func TestCloneMapNested(t *testing.T) {
	original := map[string]interface{}{
		"outer": map[string]interface{}{
			"inner": "value",
		},
	}

	cloned := CloneMap(original)

	// Verify nested structure
	outer, ok := cloned["outer"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected outer to be map")
	}

	if outer["inner"] != "value" {
		t.Errorf("Expected inner=value, got %v", outer["inner"])
	}

	// Verify independence
	outer["inner"] = "modified"
	origOuter := original["outer"].(map[string]interface{})
	if origOuter["inner"] == "modified" {
		t.Error("Expected original to be unchanged")
	}
}

// TestCloneMapWithSlice tests cloning map with slices
func TestCloneMapWithSlice(t *testing.T) {
	original := map[string]interface{}{
		"items": []interface{}{"a", "b", "c"},
	}

	cloned := CloneMap(original)

	items, ok := cloned["items"].([]interface{})
	if !ok {
		t.Fatal("Expected items to be slice")
	}

	if len(items) != 3 {
		t.Errorf("Expected 3 items, got %d", len(items))
	}
}

// ============== CloneMapInterface Tests ==============

// TestCloneMapInterfacePrimitives tests cloning primitive types
func TestCloneMapInterfacePrimitives(t *testing.T) {
	tests := []struct {
		name  string
		input interface{}
	}{
		{"string", "hello"},
		{"float64", 3.14},
		{"bool", true},
		{"nil", nil},
		{"zero_int", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CloneMapInterface(tt.input)

			// Primitives should be equal
			if result != tt.input {
				t.Errorf("Expected %v, got %v", tt.input, result)
			}
		})
	}
}

// TestCloneMapInterfaceSlice tests cloning slices
func TestCloneMapInterfaceSlice(t *testing.T) {
	original := []interface{}{"a", 1, true}
	result := CloneMapInterface(original)

	cloned, ok := result.([]interface{})
	if !ok {
		t.Fatal("Expected result to be []interface{}")
	}

	if len(cloned) != 3 {
		t.Errorf("Expected 3 elements, got %d", len(cloned))
	}

	// Verify independence
	cloned[0] = "modified"
	if original[0] == "modified" {
		t.Error("Expected original to be unchanged")
	}
}

// TestCloneMapInterfaceEmptySlice tests cloning empty slice
func TestCloneMapInterfaceEmptySlice(t *testing.T) {
	empty := make([]interface{}, 0)
	result := CloneMapInterface(empty)

	cloned, ok := result.([]interface{})
	if !ok {
		t.Fatal("Expected result to be []interface{}")
	}

	if len(cloned) != 0 {
		t.Errorf("Expected empty slice, got %d elements", len(cloned))
	}
}

// TestCloneMapInterfaceStringSlice tests cloning string slice
func TestCloneMapInterfaceStringSlice(t *testing.T) {
	original := []string{"a", "b", "c"}
	result := CloneMapInterface(original)

	cloned, ok := result.([]string)
	if !ok {
		t.Fatal("Expected result to be []string")
	}

	if len(cloned) != 3 {
		t.Errorf("Expected 3 elements, got %d", len(cloned))
	}
}

// TestCloneMapInterfaceMapStringString tests cloning map[string]string
func TestCloneMapInterfaceMapStringString(t *testing.T) {
	original := map[string]string{"k1": "v1", "k2": "v2"}
	result := CloneMapInterface(original)

	cloned, ok := result.(map[string]string)
	if !ok {
		t.Fatal("Expected result to be map[string]string")
	}

	if len(cloned) != 2 {
		t.Errorf("Expected 2 elements, got %d", len(cloned))
	}
}

// TestCloneMapInterfaceByteSlice tests cloning byte slice
func TestCloneMapInterfaceByteSlice(t *testing.T) {
	original := []byte("hello")
	result := CloneMapInterface(original)

	cloned, ok := result.([]byte)
	if !ok {
		t.Fatal("Expected result to be []byte")
	}

	if string(cloned) != "hello" {
		t.Errorf("Expected 'hello', got '%s'", string(cloned))
	}
}

// ============== Error Classification Tests ==============

// TestIsTransientError tests transient error detection
func TestIsTransientError(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"closed", "connection closed", true},
		{"broken_pipe", "broken pipe", true},
		{"connection_reset", "connection reset by peer", true},
		{"eof", "EOF", true},
		{"timeout", "i/o timeout", true},
		{"no_route", "no route to host", true},
		{"connection_refused", "connection refused", true},
		{"normal_error", "invalid input", false},
		{"empty", "", false},
		{"nil_like", "nil", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsTransientError(tt.input)
			if got != tt.want {
				t.Errorf("IsTransientError(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// TestIsConnectionError tests connection error detection
func TestIsConnectionError(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"closed", "connection closed", true},
		{"broken_pipe", "broken pipe", true},
		{"connection_reset", "connection reset", true},
		{"eof", "EOF", true},
		{"no_route", "no route to host", true},
		{"connection_refused", "connection refused", true},
		{"timeout", "i/o timeout", false}, // timeout is transient but not connection error
		{"normal_error", "file not found", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsConnectionError(tt.input)
			if got != tt.want {
				t.Errorf("IsConnectionError(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// ============== ClearMap Tests ==============

// TestClearMap tests map clearing
func TestClearMap(t *testing.T) {
	m := map[string]interface{}{
		"key1": "value1",
		"key2": 42,
	}

	ClearMap(m)

	if len(m) != 0 {
		t.Errorf("Expected empty map after ClearMap, got %d elements", len(m))
	}
}

// TestClearMapEmpty tests clearing empty map
func TestClearMapEmpty(t *testing.T) {
	m := make(map[string]interface{})

	// Should not panic
	ClearMap(m)

	if len(m) != 0 {
		t.Error("Expected empty map to remain empty")
	}
}

// TestClearMapNil tests clearing nil map
func TestClearMapNil(t *testing.T) {
	var nilMap map[string]interface{}

	// Should not panic
	ClearMap(nilMap)

	if nilMap != nil {
		t.Error("Expected nil map to remain nil")
	}
}

// ============== ClearSlice Tests ==============

// TestClearSlice tests slice clearing
func TestClearSlice(t *testing.T) {
	s := []int{1, 2, 3, 4, 5}

	cleared := ClearSlice(s)

	if len(cleared) != 0 {
		t.Errorf("Expected length 0, got %d", len(cleared))
	}

	if cap(cleared) != 5 {
		t.Errorf("Expected capacity 5, got %d", cap(cleared))
	}
}

// TestClearSliceEmpty tests clearing empty slice
func TestClearSliceEmpty(t *testing.T) {
	s := make([]string, 0, 10)

	cleared := ClearSlice(s)

	if len(cleared) != 0 {
		t.Errorf("Expected length 0, got %d", len(cleared))
	}

	if cap(cleared) != 10 {
		t.Errorf("Expected capacity 10, got %d", cap(cleared))
	}
}

// ============== Map Accessor Tests ==============

// TestGetString tests string extraction from map
func TestGetString(t *testing.T) {
	m := map[string]interface{}{
		"exists":    "value",
		"wrongType": 42,
	}

	if GetString(m, "exists") != "value" {
		t.Error("Expected to extract existing string")
	}

	if GetString(m, "wrongType") != "" {
		t.Error("Expected empty string for wrong type")
	}

	if GetString(m, "missing") != "" {
		t.Error("Expected empty string for missing key")
	}
}

// TestGetFloat64 tests float64 extraction from map
func TestGetFloat64(t *testing.T) {
	m := map[string]interface{}{
		"exists":    3.14,
		"wrongType": "string",
	}

	if GetFloat64(m, "exists") != 3.14 {
		t.Error("Expected to extract existing float64")
	}

	if GetFloat64(m, "wrongType") != 0 {
		t.Error("Expected 0 for wrong type")
	}

	if GetFloat64(m, "missing") != 0 {
		t.Error("Expected 0 for missing key")
	}
}

// TestGetBool tests bool extraction from map
func TestGetBool(t *testing.T) {
	m := map[string]interface{}{
		"exists":    true,
		"falseVal":  false,
		"wrongType": "string",
	}

	if !GetBool(m, "exists") {
		t.Error("Expected to extract true")
	}

	if GetBool(m, "falseVal") {
		t.Error("Expected to extract false")
	}

	if GetBool(m, "wrongType") {
		t.Error("Expected false for wrong type")
	}
}

// TestGetMap tests nested map extraction from map
func TestGetMap(t *testing.T) {
	nested := map[string]interface{}{"key": "value"}
	m := map[string]interface{}{
		"exists":    nested,
		"wrongType": "string",
	}

	result := GetMap(m, "exists")
	if result == nil {
		t.Error("Expected to extract nested map")
	}
	if result["key"] != "value" {
		t.Error("Expected nested map values to be accessible")
	}

	if GetMap(m, "wrongType") != nil {
		t.Error("Expected nil for wrong type")
	}

	if GetMap(m, "missing") != nil {
		t.Error("Expected nil for missing key")
	}
}

// TestGetStringSlice tests string slice extraction from map
func TestGetStringSlice(t *testing.T) {
	m := map[string]interface{}{
		"stringSlice":    []string{"a", "b", "c"},
		"interfaceSlice": []interface{}{"x", "y", "z"},
		"mixedSlice":     []interface{}{"a", 1, "b"}, // Should only extract strings
		"wrongType":      "string",
	}

	// Test []string
	result := GetStringSlice(m, "stringSlice")
	if len(result) != 3 {
		t.Errorf("Expected 3 elements, got %d", len(result))
	}

	// Test []interface{} conversion
	result = GetStringSlice(m, "interfaceSlice")
	if len(result) != 3 {
		t.Errorf("Expected 3 elements from []interface{}, got %d", len(result))
	}

	// Test mixed slice (should skip non-strings)
	result = GetStringSlice(m, "mixedSlice")
	if len(result) != 2 {
		t.Errorf("Expected 2 string elements, got %d", len(result))
	}

	// Test wrong type
	result = GetStringSlice(m, "wrongType")
	if result != nil {
		t.Error("Expected nil for wrong type")
	}
}

// TestGetInt64 tests int64 extraction from map
func TestGetInt64(t *testing.T) {
	m := map[string]interface{}{
		"float64":   42.0,
		"int64":     int64(100),
		"int":       200,
		"wrongType": "string",
	}

	if GetInt64(m, "float64") != 42 {
		t.Error("Expected to convert float64 to int64")
	}

	if GetInt64(m, "int64") != 100 {
		t.Error("Expected to extract int64")
	}

	if GetInt64(m, "int") != 200 {
		t.Error("Expected to convert int to int64")
	}

	if GetInt64(m, "wrongType") != 0 {
		t.Error("Expected 0 for wrong type")
	}
}

// TestHasKey tests map key existence check
func TestHasKey(t *testing.T) {
	m := map[string]interface{}{
		"exists": "value",
		"empty":  nil,
	}

	if !HasKey(m, "exists") {
		t.Error("Expected key to exist")
	}

	if !HasKey(m, "empty") {
		t.Error("Expected to detect key even with nil value")
	}

	if HasKey(m, "missing") {
		t.Error("Expected key to not exist")
	}
}

// TestGetStringOrDefault tests string extraction with default
func TestGetStringOrDefault(t *testing.T) {
	m := map[string]interface{}{
		"exists":    "value",
		"wrongType": 42,
	}

	if GetStringOrDefault(m, "exists", "default") != "value" {
		t.Error("Expected to extract existing value")
	}

	if GetStringOrDefault(m, "wrongType", "default") != "default" {
		t.Error("Expected default for wrong type")
	}

	if GetStringOrDefault(m, "missing", "default") != "default" {
		t.Error("Expected default for missing key")
	}
}

// ============== Pool Tests ==============

// TestMapPool tests map pool functionality
func TestMapPool(t *testing.T) {
	// Get map from pool
	m := mapPool.Get().(map[string]interface{})

	// Use it
	m["key"] = "value"

	// Clear and return
	ClearMap(m)
	mapPool.Put(m)

	// Get again - should be reusable
	m2 := mapPool.Get().(map[string]interface{})
	if len(m2) != 0 {
		t.Error("Expected pooled map to be cleared")
	}
}

// TestJSONBufferPool tests JSON buffer pool
func TestJSONBufferPool(t *testing.T) {
	// Get buffer
	buf := JSONBufferPool.Get().(*[]byte)

	if cap(*buf) != 4096 {
		t.Errorf("Expected buffer capacity 4096, got %d", cap(*buf))
	}

	// Use it
	*buf = append(*buf, []byte("json data")...)

	// Return to pool
	*buf = (*buf)[:0]
	JSONBufferPool.Put(buf)

	// Get again - should be reusable
	buf2 := JSONBufferPool.Get().(*[]byte)
	if len(*buf2) != 0 {
		t.Error("Expected pooled buffer to be cleared")
	}
}

// TestMarshalJSONWithPool tests JSON marshaling
func TestMarshalJSONWithPool(t *testing.T) {
	data := map[string]interface{}{
		"key":  "value",
		"num":  42,
		"bool": true,
	}

	result, err := MarshalJSONWithPool(data)
	if err != nil {
		t.Fatalf("Failed to marshal JSON: %v", err)
	}

	// Verify can unmarshal back
	var parsed map[string]interface{}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if parsed["key"] != "value" {
		t.Errorf("Expected key=value, got %v", parsed["key"])
	}
}

// ============== Concurrent Pool Access Tests ==============

// TestConcurrentLineBufferPool tests concurrent line buffer pool access
func TestConcurrentLineBufferPool(t *testing.T) {
	var wg sync.WaitGroup
	numGoroutines := 10

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// Get, use, return
			buf := GetLineBuffer()
			*buf = append(*buf, []byte("test")...)
			PutLineBuffer(buf)
		}(i)
	}

	wg.Wait()
}

// TestConcurrentMapPool tests concurrent map pool access
func TestConcurrentMapPool(t *testing.T) {
	var wg sync.WaitGroup
	numGoroutines := 10

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// Get, use, clear, return
			m := mapPool.Get().(map[string]interface{})
			m["key"] = "value"
			ClearMap(m)
			mapPool.Put(m)
		}(i)
	}

	wg.Wait()
}

// TestConcurrentCloneMap tests concurrent CloneMap operations
func TestConcurrentCloneMap(t *testing.T) {
	original := map[string]interface{}{
		"key1": "value1",
		"key2": 42,
	}

	var wg sync.WaitGroup
	numGoroutines := 10

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cloned := CloneMap(original)
			if len(cloned) != 2 {
				t.Errorf("Expected 2 keys, got %d", len(cloned))
			}
		}()
	}

	wg.Wait()
}
