package modules

import (
	lua "github.com/yuin/gopher-lua"
	"github.com/rs/zerolog"
)

// LogModule provides structured logging for Lua scripts
type LogModule struct {
	logger zerolog.Logger
}

// NewLogModule creates a new log module with the given logger
func NewLogModule(logger zerolog.Logger) *LogModule {
	return &LogModule{
		logger: logger.With().Str("component", "lua_script").Logger(),
	}
}

// LoadLog loads the log module into the Lua state
func (l *LogModule) LoadLog(L *lua.LState, webhookID string) {
	// Create log table
	logTable := L.NewTable()
	
	// Add log methods
	L.SetField(logTable, "info", L.NewFunction(l.makeLogFn("info", webhookID)))
	L.SetField(logTable, "debug", L.NewFunction(l.makeLogFn("debug", webhookID)))
	L.SetField(logTable, "warn", L.NewFunction(l.makeLogFn("warn", webhookID)))
	L.SetField(logTable, "error", L.NewFunction(l.makeLogFn("error", webhookID)))
	
	// Set as global
	L.SetGlobal("log", logTable)
}

// makeLogFn creates a logging function for the given level
func (l *LogModule) makeLogFn(level, webhookID string) lua.LGFunction {
	return func(L *lua.LState) int {
		// Get message (required first argument)
		msg := L.OptString(1, "")
		
		// Get optional fields table
		var fields map[string]interface{}
		if L.GetTop() >= 2 {
			fieldsTbl := L.OptTable(2, nil)
			if fieldsTbl != nil {
				fields = tableToMap(fieldsTbl)
			}
		}
		
		// Build log event
		event := l.logger.With().
			Str("webhook_id", webhookID).
			Logger()
		
		// Add custom fields
		for k, v := range fields {
			event = event.With().Interface(k, v).Logger()
		}
		
		// Log at appropriate level
		switch level {
		case "debug":
			event.Debug().Msg(msg)
		case "info":
			event.Info().Msg(msg)
		case "warn":
			event.Warn().Msg(msg)
		case "error":
			event.Error().Msg(msg)
		default:
			event.Info().Msg(msg)
		}
		
		return 0
	}
}

// tableToMap converts a Lua table to a Go map[string]interface{}
func tableToMap(tbl *lua.LTable) map[string]interface{} {
	result := make(map[string]interface{})
	if tbl == nil {
		return result
	}
	
	tbl.ForEach(func(k, v lua.LValue) {
		key := k.String()
		
		switch val := v.(type) {
		case lua.LNumber:
			// Try to determine if it's an int or float
			if float64(val) == float64(int64(val)) {
				result[key] = int64(val)
			} else {
				result[key] = float64(val)
			}
		case lua.LString:
			result[key] = string(val)
		case lua.LBool:
			result[key] = bool(val)
		case *lua.LTable:
			result[key] = tableToMap(val)
		default:
			result[key] = v.String()
		}
	})
	
	return result
}
