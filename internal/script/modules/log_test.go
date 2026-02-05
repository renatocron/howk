//go:build !integration

package modules

import (
	"bytes"
	"testing"

	"github.com/rs/zerolog"
	lua "github.com/yuin/gopher-lua"
)

// =============================================================================
// tableToMap Tests
// =============================================================================

func TestTableToMap_EmptyTable(t *testing.T) {
	L := lua.NewState()
	defer L.Close()

	tbl := L.NewTable()
	result := tableToMap(tbl)

	if len(result) != 0 {
		t.Errorf("Expected empty map, got %d items", len(result))
	}
}

func TestTableToMap_NilTable(t *testing.T) {
	result := tableToMap(nil)

	if len(result) != 0 {
		t.Errorf("Expected empty map for nil, got %d items", len(result))
	}
}

func TestTableToMap_StringValues(t *testing.T) {
	L := lua.NewState()
	defer L.Close()

	tbl := L.NewTable()
	tbl.RawSetString("name", lua.LString("test"))
	tbl.RawSetString("message", lua.LString("hello world"))

	result := tableToMap(tbl)

	if result["name"] != "test" {
		t.Errorf("Expected name='test', got %v", result["name"])
	}
	if result["message"] != "hello world" {
		t.Errorf("Expected message='hello world', got %v", result["message"])
	}
}

func TestTableToMap_NumberValues(t *testing.T) {
	L := lua.NewState()
	defer L.Close()

	tbl := L.NewTable()
	tbl.RawSetString("count", lua.LNumber(42))
	tbl.RawSetString("pi", lua.LNumber(3.14159))
	tbl.RawSetString("negative", lua.LNumber(-10))

	result := tableToMap(tbl)

	// Integer should be int64
	if result["count"] != int64(42) {
		t.Errorf("Expected count=int64(42), got %T %v", result["count"], result["count"])
	}

	// Float should be float64
	if result["pi"] != 3.14159 {
		t.Errorf("Expected pi=3.14159, got %T %v", result["pi"], result["pi"])
	}

	// Negative integer should be int64
	if result["negative"] != int64(-10) {
		t.Errorf("Expected negative=int64(-10), got %T %v", result["negative"], result["negative"])
	}
}

func TestTableToMap_BooleanValues(t *testing.T) {
	L := lua.NewState()
	defer L.Close()

	tbl := L.NewTable()
	tbl.RawSetString("active", lua.LTrue)
	tbl.RawSetString("deleted", lua.LFalse)

	result := tableToMap(tbl)

	if result["active"] != true {
		t.Errorf("Expected active=true, got %v", result["active"])
	}
	if result["deleted"] != false {
		t.Errorf("Expected deleted=false, got %v", result["deleted"])
	}
}

func TestTableToMap_NestedTable(t *testing.T) {
	L := lua.NewState()
	defer L.Close()

	inner := L.NewTable()
	inner.RawSetString("x", lua.LNumber(10))
	inner.RawSetString("y", lua.LNumber(20))

	outer := L.NewTable()
	outer.RawSetString("point", inner)
	outer.RawSetString("name", lua.LString("origin"))

	result := tableToMap(outer)

	// Check outer string
	if result["name"] != "origin" {
		t.Errorf("Expected name='origin', got %v", result["name"])
	}

	// Check nested table
	nested, ok := result["point"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected nested map, got %T", result["point"])
	}
	if nested["x"] != int64(10) {
		t.Errorf("Expected nested x=10, got %v", nested["x"])
	}
	if nested["y"] != int64(20) {
		t.Errorf("Expected nested y=20, got %v", nested["y"])
	}
}

func TestTableToMap_MixedTypes(t *testing.T) {
	L := lua.NewState()
	defer L.Close()

	tbl := L.NewTable()
	tbl.RawSetString("id", lua.LNumber(123))
	tbl.RawSetString("name", lua.LString("test"))
	tbl.RawSetString("enabled", lua.LTrue)
	tbl.RawSetString("score", lua.LNumber(99.5))

	result := tableToMap(tbl)

	if len(result) != 4 {
		t.Errorf("Expected 4 items, got %d", len(result))
	}

	if result["id"] != int64(123) {
		t.Errorf("Expected id=int64(123), got %T %v", result["id"], result["id"])
	}
	if result["name"] != "test" {
		t.Errorf("Expected name='test', got %v", result["name"])
	}
	if result["enabled"] != true {
		t.Errorf("Expected enabled=true, got %v", result["enabled"])
	}
	if result["score"] != 99.5 {
		t.Errorf("Expected score=99.5, got %v", result["score"])
	}
}

// =============================================================================
// makeLogFn Tests
// =============================================================================

func TestMakeLogFn_BasicMessage(t *testing.T) {
	var buf bytes.Buffer
	logger := zerolog.New(&buf)
	logModule := NewLogModule(logger)

	L := lua.NewState()
	defer L.Close()

	// Load the log module
	logModule.LoadLog(L, "webhook-123")

	// Test info log
	err := L.DoString(`log.info("test message")`)
	if err != nil {
		t.Fatalf("Failed to execute Lua: %v", err)
	}

	output := buf.String()
	if !bytes.Contains([]byte(output), []byte("test message")) {
		t.Errorf("Expected log to contain 'test message', got: %s", output)
	}
	if !bytes.Contains([]byte(output), []byte("webhook-123")) {
		t.Errorf("Expected log to contain webhook_id, got: %s", output)
	}
}

func TestMakeLogFn_WithFields(t *testing.T) {
	var buf bytes.Buffer
	logger := zerolog.New(&buf)
	logModule := NewLogModule(logger)

	L := lua.NewState()
	defer L.Close()

	// Load the log module
	logModule.LoadLog(L, "webhook-456")

	// Test log with fields
	err := L.DoString(`log.info("user action", {user_id = 42, action = "click"})`)
	if err != nil {
		t.Fatalf("Failed to execute Lua: %v", err)
	}

	output := buf.String()
	if !bytes.Contains([]byte(output), []byte("user action")) {
		t.Errorf("Expected log to contain 'user action', got: %s", output)
	}
	if !bytes.Contains([]byte(output), []byte("user_id")) {
		t.Errorf("Expected log to contain user_id field, got: %s", output)
	}
	if !bytes.Contains([]byte(output), []byte("click")) {
		t.Errorf("Expected log to contain action field, got: %s", output)
	}
}

func TestMakeLogFn_AllLevels(t *testing.T) {
	var buf bytes.Buffer
	logger := zerolog.New(&buf)
	logModule := NewLogModule(logger)

	L := lua.NewState()
	defer L.Close()

	logModule.LoadLog(L, "webhook-levels")

	// Test all log levels
	err := L.DoString(`
		log.debug("debug message")
		log.info("info message")
		log.warn("warn message")
		log.error("error message")
	`)
	if err != nil {
		t.Fatalf("Failed to execute Lua: %v", err)
	}

	output := buf.String()

	// Check all messages are logged
	for _, level := range []string{"debug", "info", "warn", "error"} {
		expected := level + " message"
		if !bytes.Contains([]byte(output), []byte(expected)) {
			t.Errorf("Expected log to contain '%s', got: %s", expected, output)
		}
	}
}

func TestMakeLogFn_EmptyMessage(t *testing.T) {
	var buf bytes.Buffer
	logger := zerolog.New(&buf)
	logModule := NewLogModule(logger)

	L := lua.NewState()
	defer L.Close()

	logModule.LoadLog(L, "webhook-empty")

	// Test empty message
	err := L.DoString(`log.info("")`)
	if err != nil {
		t.Fatalf("Failed to execute Lua: %v", err)
	}

	output := buf.String()
	if output == "" {
		t.Error("Expected some log output even for empty message")
	}
}

func TestMakeLogFn_NoFields(t *testing.T) {
	var buf bytes.Buffer
	logger := zerolog.New(&buf)
	logModule := NewLogModule(logger)

	L := lua.NewState()
	defer L.Close()

	logModule.LoadLog(L, "webhook-nofields")

	// Test log without fields table
	err := L.DoString(`log.info("simple message")`)
	if err != nil {
		t.Fatalf("Failed to execute Lua: %v", err)
	}

	output := buf.String()
	if !bytes.Contains([]byte(output), []byte("simple message")) {
		t.Errorf("Expected log to contain 'simple message', got: %s", output)
	}
}

func TestMakeLogFn_NestedFields(t *testing.T) {
	var buf bytes.Buffer
	logger := zerolog.New(&buf)
	logModule := NewLogModule(logger)

	L := lua.NewState()
	defer L.Close()

	logModule.LoadLog(L, "webhook-nested")

	// Test log with nested fields - nested tables get converted to maps
	err := L.DoString(`log.info("nested test", {data = {x = 1, y = 2}, name = "point"})`)
	if err != nil {
		t.Fatalf("Failed to execute Lua: %v", err)
	}

	output := buf.String()
	if !bytes.Contains([]byte(output), []byte("nested test")) {
		t.Errorf("Expected log to contain 'nested test', got: %s", output)
	}
	// The nested data should be present in some form
	if !bytes.Contains([]byte(output), []byte("data")) {
		t.Errorf("Expected log to contain 'data' field, got: %s", output)
	}
}

func TestLogModule_Creation(t *testing.T) {
	logger := zerolog.New(nil)
	logModule := NewLogModule(logger)

	if logModule == nil {
		t.Error("Expected LogModule to be created")
	}

	L := lua.NewState()
	defer L.Close()

	// Should not panic
	logModule.LoadLog(L, "webhook-test")

	// Verify log global is set
	logVal := L.GetGlobal("log")
	if logVal.Type() != lua.LTTable {
		t.Errorf("Expected 'log' to be a table, got %s", logVal.Type().String())
	}
}
