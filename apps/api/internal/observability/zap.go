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
	logger := zap.New(zapcore.NewCore(encoder, output, zap.InfoLevel)).With(
		zap.String("service", service),
		zap.String("build_sha", resource.BuildSHA),
		zap.String("environment", resource.Environment),
		zap.String("region", resource.Region),
	)
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
	filtered := make([]zap.Field, 0, len(fields)+1)
	for _, field := range fields {
		switch field.Key {
		case "service", "build_sha", "environment", "region", "event":
			continue
		default:
			filtered = append(filtered, field)
		}
	}
	event = boundedLogEvent(event)
	filtered = append(filtered, zap.String("event", event))
	logger.Info(event, filtered...)
}

// SafeString redacts credential-shaped content before Zap encodes the field.
func SafeString(key, value string) zap.Field {
	return zap.String(key, redactLogMessage(value))
}

// SafeError redacts credential-shaped error text before Zap encodes the field.
func SafeError(err error) zap.Field {
	if err == nil {
		return zap.Skip()
	}
	return SafeString("error", err.Error())
}
