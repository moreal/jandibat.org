package observability

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"syscall"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestNewLoggerProductionWritesOneJSONRecordWithFixedFields(t *testing.T) {
	var output bytes.Buffer
	logger, sync, err := NewLogger(Config{
		Service:  "api",
		Resource: Resource{BuildSHA: "abc123", Environment: "production", Region: "icn"},
		Output:   zapcore.AddSync(&output),
	})
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	Log(logger, "http.request_failed", SafeString("request_id", "request-123"))
	if err := sync(); err != nil {
		t.Fatalf("sync logger: %v", err)
	}

	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("log lines = %d, want 1: %q", len(lines), output.String())
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("decode JSON log: %v: %q", err, lines[0])
	}
	for key, want := range map[string]string{
		"service": "api", "build_sha": "abc123", "environment": "production", "region": "icn",
		"event": "http.request_failed", "request_id": "request-123",
	} {
		if got := record[key]; got != want {
			t.Errorf("record[%q] = %#v, want %q", key, got, want)
		}
	}
}

func TestHTTPServerErrorLogDropsRawDiagnosticText(t *testing.T) {
	logger, output := newProductionTestLogger(t)
	serverLog := NewHTTPServerErrorLog(logger)
	serverLog.Print("http: panic serving 127.0.0.1: opaque-server-secret\ngoroutine 42 [running]")

	if strings.Count(output.String(), "\n") != 1 {
		t.Fatalf("server diagnostic emitted multiple records: %q", output.String())
	}
	for _, forbidden := range []string{"opaque-server-secret", "panic serving", "goroutine 42"} {
		if strings.Contains(output.String(), forbidden) {
			t.Fatalf("server diagnostic exposed %q: %s", forbidden, output.String())
		}
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record); err != nil {
		t.Fatalf("decode server diagnostic: %v: %s", err, output.String())
	}
	if record["event"] != "http.server_error" {
		t.Fatalf("server diagnostic = %#v", record)
	}
}

func TestLogBoundsEventAndAddsTypedRequestFields(t *testing.T) {
	core, observed := observer.New(zap.InfoLevel)
	logger := zap.New(newSafeCore(core,
		zap.String("service", "api"), zap.String("build_sha", "unknown"),
		zap.String("environment", "test"), zap.String("region", "unknown"),
	))
	Log(logger, "ATTACK event", SafeString("request_id", "request-123"), zap.String("method", "POST"))

	entries := observed.All()
	if len(entries) != 1 {
		t.Fatalf("observed entries = %d, want 1", len(entries))
	}
	fields := entries[0].ContextMap()
	if fields["event"] != "unknown" || fields["request_id"] != "request-123" || fields["method"] != "POST" {
		t.Fatalf("observed fields = %#v", fields)
	}
}

func TestSafeFieldsRedactBeforeEncodingAndPreventLineInjection(t *testing.T) {
	var output bytes.Buffer
	logger, _, err := NewLogger(Config{
		Service: "api", Resource: Resource{Environment: "production"}, Output: zapcore.AddSync(&output),
	})
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	Log(logger, "api.process_stopped",
		SafeString("authorization", "Bearer token-value"),
		SafeString("proxy_authorization", "Basic dXNlcjpwYXNz"),
		SafeString("url", "https://example.test/callback?access_token=query-secret&api_key=key-secret"),
		SafeError(errors.New("postgresql://service:p%40ssword@db.example/app?password=db-secret\nforged=true")),
	)

	if strings.Count(output.String(), "\n") != 1 {
		t.Fatalf("log injection created multiple records: %q", output.String())
	}
	for _, secret := range []string{"token-value", "dXNlcjpwYXNz", "query-secret", "key-secret", "p%40ssword", "db-secret", "\nforged=true"} {
		if strings.Contains(output.String(), secret) {
			t.Errorf("JSON log leaked %q: %s", secret, output.String())
		}
	}
	if got := strings.Count(output.String(), "[REDACTED]"); got != 4 {
		t.Fatalf("redaction count = %d, want 4: %s", got, output.String())
	}
	if !strings.Contains(output.String(), `"failure_type":"*errors.errorString"`) {
		t.Fatalf("bounded error classification missing: %s", output.String())
	}
}

func TestLoggerCoreRejectsUnsafeFieldsAndProtectsFixedSchema(t *testing.T) {
	var output bytes.Buffer
	logger, _, err := NewLogger(Config{
		Service:  "api",
		Resource: Resource{BuildSHA: "abc123", Environment: "production", Region: "icn"},
		Output:   zapcore.AddSync(&output),
	})
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}

	logger.With(
		zap.String("service", "derived-service-secret"),
		zap.String("failure_type", "derived-failure-type-secret"),
		zap.String("authorization", "derived-opaque-secret"),
		zap.Error(errors.New("derived-unlabelled-error-secret")),
		zap.Any("credentials", map[string]any{"secret": "opaque-sensitive-any-secret"}),
		zap.Any("derived_payload", map[string]any{"password": "derived-nested-secret"}),
		zap.Inline(hostileInlineFields{}),
		zap.Namespace("derived_namespace"),
		zap.String("region", "derived-region-secret"),
	).Info("http.request_completed",
		zap.String("event", "forged.event"),
		zap.String("message", "message-field-secret"),
		zap.String("token_value", "call-opaque-secret"),
		zap.String("error", "string-error-secret"),
		zap.String("error_type", "type-field-secret"),
		zap.String("failure_type", "failure-field-secret"),
		zap.Error(errors.New("call-unlabelled-error-secret")),
		zap.Any("payload", map[string]any{"secret": "call-nested-secret"}),
		zap.Inline(hostileInlineFields{}),
		zap.Namespace("call_namespace"),
		zap.String("request_id", "request-123"),
	)

	encoded := output.String()
	for _, secret := range []string{
		"derived-service-secret", "derived-failure-type-secret", "derived-opaque-secret", "derived-unlabelled-error-secret", "opaque-sensitive-any-secret",
		"derived-nested-secret", "inline-secret", "derived-region-secret", "forged.event",
		"message-field-secret", "call-opaque-secret", "string-error-secret", "type-field-secret", "failure-field-secret",
		"call-unlabelled-error-secret", "call-nested-secret",
	} {
		if strings.Contains(encoded, secret) {
			t.Errorf("logger leaked %q: %s", secret, encoded)
		}
	}
	for _, key := range []string{"message", "service", "build_sha", "environment", "region", "event"} {
		if got := strings.Count(encoded, `"`+key+`":`); got != 1 {
			t.Errorf("JSON key %q count = %d, want 1: %s", key, got, encoded)
		}
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record); err != nil {
		t.Fatalf("decode JSON log: %v: %s", err, encoded)
	}
	for key, want := range map[string]string{
		"service": "api", "build_sha": "abc123", "environment": "production", "region": "icn",
		"event": "http.request_completed", "request_id": "request-123",
		"authorization": "[REDACTED]", "credentials": "[REDACTED]", "token_value": "[REDACTED]",
	} {
		if got := record[key]; got != want {
			t.Errorf("record[%q] = %#v, want %q", key, got, want)
		}
	}
	if _, exists := record["payload"]; exists {
		t.Fatalf("unsafe nested payload was encoded: %#v", record)
	}
	if _, exists := record["derived_namespace"]; exists {
		t.Fatalf("namespace was encoded: %#v", record)
	}
	if _, exists := record["call_namespace"]; exists {
		t.Fatalf("namespace was encoded: %#v", record)
	}
	for _, key := range []string{"error_type", "failure_type"} {
		if got := strings.Count(encoded, `"`+key+`":`); got != 0 {
			t.Errorf("raw classification key %q count = %d, want 0: %s", key, got, encoded)
		}
	}
}

func TestRawZapErrorsAreDroppedAcrossLoggingPaths(t *testing.T) {
	for _, test := range []struct {
		name string
		emit func(*zap.Logger)
	}{
		{name: "direct info", emit: func(logger *zap.Logger) {
			logger.Info("test.direct", zap.Error(errors.New("direct-error-secret")))
		}},
		{name: "Log helper", emit: func(logger *zap.Logger) {
			Log(logger, "test.log", zap.Error(errors.New("helper-error-secret")))
		}},
		{name: "derived With", emit: func(logger *zap.Logger) {
			logger.With(zap.Error(errors.New("derived-error-secret"))).Info("test.derived")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			logger, output := newProductionTestLogger(t)
			test.emit(logger)
			encoded := output.String()
			for _, secret := range []string{"direct-error-secret", "helper-error-secret", "derived-error-secret"} {
				if strings.Contains(encoded, secret) {
					t.Errorf("raw Zap error leaked %q: %s", secret, encoded)
				}
			}
			for _, key := range []string{"error_type", "failure_type"} {
				if got := strings.Count(encoded, `"`+key+`":`); got != 0 {
					t.Errorf("raw Zap error emitted %q %d times, want 0: %s", key, got, encoded)
				}
			}
		})
	}
}

func TestSafeErrorProducesOneCanonicalClassificationAcrossLoggingPaths(t *testing.T) {
	for _, test := range []struct {
		name string
		emit func(*zap.Logger, error)
	}{
		{name: "direct info", emit: func(logger *zap.Logger, err error) {
			logger.Info("test.direct", zap.String("failure_type", "direct-spoof-secret"), SafeError(err))
		}},
		{name: "Log helper", emit: func(logger *zap.Logger, err error) {
			Log(logger, "test.log", zap.String("failure_type", "helper-spoof-secret"), SafeError(err))
		}},
		{name: "derived With", emit: func(logger *zap.Logger, err error) {
			logger.With(zap.String("failure_type", "derived-spoof-secret"), SafeError(err)).Info("test.derived", SafeError(err))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			logger, output := newProductionTestLogger(t)
			test.emit(logger, classifiedTestError{})
			encoded := output.String()
			for _, secret := range []string{"direct-spoof-secret", "helper-spoof-secret", "derived-spoof-secret", classifiedErrorSecret} {
				if strings.Contains(encoded, secret) {
					t.Errorf("SafeError log leaked %q: %s", secret, encoded)
				}
			}
			if got := strings.Count(encoded, `"failure_type":`); got != 1 {
				t.Fatalf("failure_type count = %d, want 1: %s", got, encoded)
			}
			if got := strings.Count(encoded, `"error_type":`); got != 0 {
				t.Fatalf("error_type count = %d, want 0: %s", got, encoded)
			}
			var record map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record); err != nil {
				t.Fatalf("decode JSON log: %v: %s", err, encoded)
			}
			if record["failure_type"] != "observability.classifiedTestError" {
				t.Fatalf("failure_type = %#v", record["failure_type"])
			}
		})
	}
}

func TestResourceFromEnvironmentDefaultsToDevelopment(t *testing.T) {
	t.Setenv("APP_ENV", "")
	resource := ResourceFromEnvironment("")
	if resource.Environment != "development" {
		t.Fatalf("environment = %q, want development", resource.Environment)
	}
}

func TestLoggerSyncIgnoresTerminalErrorsOnly(t *testing.T) {
	for _, test := range []struct {
		name    string
		syncErr error
		wantErr bool
	}{
		{name: "invalid descriptor", syncErr: syscall.EINVAL},
		{name: "not a terminal", syncErr: syscall.ENOTTY},
		{name: "disk failure", syncErr: errors.New("disk failure"), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := &syncErrorWriter{syncErr: test.syncErr}
			_, sync, err := NewLogger(Config{Service: "api", Resource: Resource{Environment: "test"}, Output: output})
			if err != nil {
				t.Fatalf("NewLogger: %v", err)
			}
			err = sync()
			if (err != nil) != test.wantErr {
				t.Fatalf("sync error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

type syncErrorWriter struct {
	bytes.Buffer
	syncErr error
}

func (writer *syncErrorWriter) Sync() error { return writer.syncErr }

func newProductionTestLogger(t *testing.T) (*zap.Logger, *bytes.Buffer) {
	t.Helper()
	var output bytes.Buffer
	logger, _, err := NewLogger(Config{
		Service: "api", Resource: Resource{Environment: "production"}, Output: zapcore.AddSync(&output),
	})
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	return logger, &output
}

const classifiedErrorSecret = "classified-error-secret"

type classifiedTestError struct{}

func (classifiedTestError) Error() string { return classifiedErrorSecret }

type hostileInlineFields struct{}

func (hostileInlineFields) MarshalLogObject(encoder zapcore.ObjectEncoder) error {
	encoder.AddString("service", "inline-service-secret")
	encoder.AddString("inline_value", "inline-secret")
	return nil
}
