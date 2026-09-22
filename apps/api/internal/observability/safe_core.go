package observability

import (
	"reflect"
	"strings"
	"unicode"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	redactedLogValue = "[REDACTED]"
	maxLogFieldKey   = 64
	maxLogType       = 96
)

var reservedLogFields = map[string]struct{}{
	"timestamp": {}, "level": {}, "message": {}, "logger": {}, "caller": {}, "stacktrace": {},
	"service": {}, "build_sha": {}, "environment": {}, "region": {}, "event": {},
}

// safeCore owns the fixed record schema and sanitizes both fields attached by
// Logger.With and fields supplied at a call site. The underlying encoder never
// sees caller-controlled namespaces, marshalers, reflected values, or errors.
type safeCore struct {
	core    zapcore.Core
	fixed   []zap.Field
	context []zap.Field
}

func newSafeCore(core zapcore.Core, fixed ...zap.Field) zapcore.Core {
	return &safeCore{core: core, fixed: append([]zap.Field(nil), fixed...)}
}

func (core *safeCore) Enabled(level zapcore.Level) bool { return core.core.Enabled(level) }

func (core *safeCore) With(fields []zap.Field) zapcore.Core {
	clone := &safeCore{
		core:    core.core,
		fixed:   core.fixed,
		context: append([]zap.Field(nil), core.context...),
	}
	clone.context = append(clone.context, sanitizeLogFields(fields)...)
	return clone
}

func (core *safeCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if !core.Enabled(entry.Level) {
		return checked
	}
	return checked.AddCore(entry, core)
}

func (core *safeCore) Write(entry zapcore.Entry, fields []zap.Field) error {
	event := boundedLogEvent(entry.Message)
	entry.Message = event
	safe := make([]zap.Field, 0, len(core.fixed)+len(core.context)+len(fields)+1)
	safe = append(safe, core.fixed...)
	safe = append(safe, core.context...)
	safe = append(safe, sanitizeLogFields(fields)...)
	safe = append(safe, zap.String("event", event))
	return core.core.Write(entry, safe)
}

func (core *safeCore) Sync() error { return core.core.Sync() }

func sanitizeLogFields(fields []zap.Field) []zap.Field {
	safe := make([]zap.Field, 0, len(fields))
	for _, field := range fields {
		if sanitized, ok := sanitizeLogField(field); ok {
			safe = append(safe, sanitized)
		}
	}
	return safe
}

func sanitizeLogField(field zap.Field) (zap.Field, bool) {
	if field.Type == zapcore.SkipType {
		return zap.Field{}, false
	}
	if !validLogFieldKey(field.Key) {
		return zap.Field{}, false
	}
	if _, reserved := reservedLogFields[field.Key]; reserved {
		return zap.Field{}, false
	}
	if field.Type == zapcore.ErrorType {
		return zap.String(field.Key+"_type", boundedLogType(field.Interface)), true
	}
	if sensitiveLogKey(field.Key) {
		return zap.String(field.Key, redactedLogValue), true
	}

	switch field.Type {
	case zapcore.StringType:
		return zap.String(field.Key, redactLogMessage(field.String)), true
	case zapcore.ByteStringType:
		value, ok := field.Interface.([]byte)
		if !ok {
			return zap.Field{}, false
		}
		return zap.String(field.Key, redactLogMessage(string(value))), true
	case zapcore.BoolType, zapcore.DurationType,
		zapcore.Float64Type, zapcore.Float32Type,
		zapcore.Int64Type, zapcore.Int32Type, zapcore.Int16Type, zapcore.Int8Type,
		zapcore.Uint64Type, zapcore.Uint32Type, zapcore.Uint16Type, zapcore.Uint8Type, zapcore.UintptrType:
		return field, true
	case zapcore.UnknownType, zapcore.ArrayMarshalerType, zapcore.ObjectMarshalerType,
		zapcore.BinaryType, zapcore.Complex128Type, zapcore.Complex64Type,
		zapcore.TimeType, zapcore.TimeFullType, zapcore.ReflectType,
		zapcore.NamespaceType, zapcore.StringerType, zapcore.ErrorType,
		zapcore.SkipType, zapcore.InlineMarshalerType:
		return zap.Field{}, false
	}
	return zap.Field{}, false
}

func validLogFieldKey(key string) bool {
	if key == "" || len(key) > maxLogFieldKey {
		return false
	}
	for index, character := range key {
		if (character >= 'a' && character <= 'z') || character == '_' || (index > 0 && character >= '0' && character <= '9') {
			continue
		}
		return false
	}
	return true
}

func sensitiveLogKey(key string) bool {
	key = strings.ReplaceAll(strings.ToLower(key), "-", "_")
	if key == "error" || key == "err" || strings.HasPrefix(key, "error_") ||
		strings.HasSuffix(key, "_error") || strings.Contains(key, "_error_") {
		return true
	}
	for _, marker := range []string{
		"authorization", "token", "password", "passwd", "secret", "credential", "cookie", "session",
		"api_key", "apikey", "private_key", "database_url", "databaseurl",
	} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return key == "dsn"
}

func boundedLogType(value any) string {
	if value == nil {
		return "nil"
	}
	name := reflect.TypeOf(value).String()
	if name == "" || len(name) > maxLogType {
		return "unknown"
	}
	for _, character := range name {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || strings.ContainsRune("*./_[]-", character) {
			continue
		}
		return "unknown"
	}
	return name
}
