package observability

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Config defines one process logger. Output is primarily an injection seam for
// tests; processes write to stderr when it is omitted.
type Config struct {
	Service     string
	Resource    Resource
	Development bool
	Output      zapcore.WriteSyncer
}

// NewLogger constructs a process-owned logger and its shutdown sync function.
// Production emits one JSON object per line; non-production uses the console
// encoder for local readability.
func NewLogger(config Config) (*zap.Logger, func() error, error) {
	service := strings.TrimSpace(config.Service)
	if service == "" {
		return nil, nil, fmt.Errorf("observability: logger service is required")
	}
	resource := normalizeResource(config.Resource)
	output := config.Output
	if output == nil {
		output = zapcore.Lock(os.Stderr)
	}
	encoderConfig := zapcore.EncoderConfig{
		TimeKey: "timestamp", LevelKey: "level", MessageKey: "message",
		EncodeTime: zapcore.ISO8601TimeEncoder, EncodeLevel: zapcore.LowercaseLevelEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
	}
	var encoder zapcore.Encoder
	if config.Development || resource.Environment != "production" {
		encoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder
		encoder = zapcore.NewConsoleEncoder(encoderConfig)
	} else {
		encoder = zapcore.NewJSONEncoder(encoderConfig)
	}
	core := newSafeCore(zapcore.NewCore(encoder, output, zap.InfoLevel),
		zap.String("service", service),
		zap.String("build_sha", resource.BuildSHA),
		zap.String("environment", resource.Environment),
		zap.String("region", resource.Region),
	)
	logger := zap.New(core)
	return logger, func() error {
		err := logger.Sync()
		if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTTY) {
			return nil
		}
		return err
	}, nil
}

// Log records one validated event. Fixed fields are owned by NewLogger; this
// boundary prevents callers from replacing them with untrusted values.
func Log(logger *zap.Logger, event string, fields ...zap.Field) {
	if logger == nil {
		return
	}
	event = boundedLogEvent(event)
	safe := sanitizeLogFields(fields)
	safe = append(safe, zap.String("event", event))
	logger.Info(event, safe...)
}

// SafeString redacts credential-shaped content before Zap encodes the field.
func SafeString(key, value string) zap.Field {
	field, ok := sanitizeLogField(zap.String(key, value))
	if !ok {
		return zap.Skip()
	}
	return field
}

// SafeError records only the bounded concrete error type. Opaque error text is
// intentionally excluded because it can contain credentials without labels.
func SafeError(err error) zap.Field {
	if err == nil {
		return zap.Skip()
	}
	return zap.String("failure_type", boundedLogType(err))
}
