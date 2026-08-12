package operations

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const RedactedValue = "[REDACTED]"

var (
	ErrInvalidAuditEvent    = errors.New("operations: invalid audit event")
	ErrUnsupportedAuditData = errors.New("operations: unsupported audit metadata value")

	authorizationPattern = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[^\s,;]+`)
	querySecretPattern   = regexp.MustCompile(`(?i)(access_token|refresh_token|api_key|password|secret)=([^&\s]+)`)
	auditSecretShape     = regexp.MustCompile(`(?i)(-----BEGIN [A-Z ]+ PRIVATE KEY-----|ghp_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|[?&](access_token|refresh_token|code|token)=[^&"\s]{8,})`)
)

type AuditOutcome string

const (
	AuditSucceeded AuditOutcome = "succeeded"
	AuditDenied    AuditOutcome = "denied"
	AuditFailed    AuditOutcome = "failed"

	AuditActorAnonymous      = "anonymous"
	AuditActorUser           = "user"
	AuditActorSystem         = "system"
	AuditActorCustomProvider = "custom_provider"
)

// AuditEvent is deliberately structured so downstream sinks do not need to
// parse log messages to answer who did what to which resource.
type AuditEvent struct {
	ID         string
	OccurredAt time.Time
	Actor      AuditActor
	Action     string
	Target     AuditTarget
	Outcome    AuditOutcome
	RequestID  string
	SourceIP   string
	Metadata   map[string]any
}

type AuditActor struct {
	Type string
	ID   string
}

type AuditTarget struct {
	Type string
	ID   string
}

// NewAuditEventID returns an RFC 4122 version 4 UUID suitable for the audit
// table's UUID primary key. It deliberately has no logging fallback because a
// predictable identifier would weaken correlation guarantees.
func NewAuditEventID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate audit event ID: %w", err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

// AuditEventSink is the application port for immutable, append-only audit
// events. Implementations must honor context cancellation.
type AuditEventSink interface {
	WriteAuditEvent(context.Context, AuditEvent) error
}

// AuditRecorder enforces validation and redaction before an event crosses the
// sink boundary.
type AuditRecorder struct {
	sink     AuditEventSink
	redactor Redactor
}

func NewAuditRecorder(sink AuditEventSink, extraSensitiveKeys ...string) (*AuditRecorder, error) {
	if sink == nil {
		return nil, fmt.Errorf("%w: sink is required", ErrInvalidAuditEvent)
	}
	return &AuditRecorder{sink: sink, redactor: NewRedactor(extraSensitiveKeys...)}, nil
}

func (recorder *AuditRecorder) Record(ctx context.Context, event AuditEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateAuditEvent(event); err != nil {
		return err
	}
	redacted, err := recorder.redactor.RedactEvent(event)
	if err != nil {
		return err
	}
	return recorder.sink.WriteAuditEvent(ctx, redacted)
}

type Redactor struct {
	sensitiveKeys map[string]struct{}
}

func NewRedactor(extraSensitiveKeys ...string) Redactor {
	keys := []string{
		"authorization", "proxy-authorization", "cookie", "set-cookie",
		"password", "passwd", "secret", "client-secret", "access-token",
		"refresh-token", "session-token", "session-id", "api-key",
		"ingest-key", "ingestion-key", "credential", "private-key",
	}
	sensitive := make(map[string]struct{}, len(keys)+len(extraSensitiveKeys))
	for _, key := range append(keys, extraSensitiveKeys...) {
		if normalized := normalizeAuditKey(key); normalized != "" {
			sensitive[normalized] = struct{}{}
		}
	}
	return Redactor{sensitiveKeys: sensitive}
}

func (redactor Redactor) RedactEvent(event AuditEvent) (AuditEvent, error) {
	metadata, err := redactor.RedactMetadata(event.Metadata)
	if err != nil {
		return AuditEvent{}, err
	}
	result := event
	result.OccurredAt = event.OccurredAt.UTC()
	result.Actor.ID = redactAuditIdentifier(event.Actor.ID)
	result.Action = redactString(event.Action)
	result.Target.ID = redactAuditIdentifier(event.Target.ID)
	result.RequestID = redactString(event.RequestID)
	result.SourceIP = redactString(event.SourceIP)
	result.Metadata = metadata
	return result, nil
}

// redactAuditIdentifier applies a stricter policy than general metadata
// strings. Actor and target identifiers must be stable internal identifiers,
// never credential-bearing URLs or header values. Preserve ordinary IDs while
// collapsing credential-shaped values and stripping query/fragment and CRLF
// injection suffixes before they reach the append-only audit store.
func redactAuditIdentifier(value string) string {
	if decoded, err := url.PathUnescape(value); err == nil {
		value = decoded
	}
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r", ""), "\n", "")
	if authorizationPattern.MatchString(value) || querySecretPattern.MatchString(value) || auditSecretShape.MatchString(value) {
		return RedactedValue
	}
	if index := strings.IndexAny(value, "?#"); index >= 0 {
		value = value[:index]
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return redactString(value)
}

func (redactor Redactor) RedactMetadata(metadata map[string]any) (map[string]any, error) {
	if metadata == nil {
		return nil, nil
	}
	result := make(map[string]any, len(metadata))
	for key, value := range metadata {
		if redactor.sensitive(key) {
			result[key] = RedactedValue
			continue
		}
		redacted, err := redactor.redactValue(value)
		if err != nil {
			return nil, fmt.Errorf("%w at %q: %v", ErrUnsupportedAuditData, key, err)
		}
		result[key] = redacted
	}
	return result, nil
}

func (redactor Redactor) redactValue(value any) (any, error) {
	switch typed := value.(type) {
	case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return typed, nil
	case string:
		return redactString(typed), nil
	case time.Time:
		return typed.UTC(), nil
	case []string:
		result := make([]string, len(typed))
		for index, item := range typed {
			result[index] = redactString(item)
		}
		return result, nil
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			redacted, err := redactor.redactValue(item)
			if err != nil {
				return nil, fmt.Errorf("index %d: %w", index, err)
			}
			result[index] = redacted
		}
		return result, nil
	case map[string]string:
		converted := make(map[string]any, len(typed))
		for key, item := range typed {
			converted[key] = item
		}
		return redactor.RedactMetadata(converted)
	case map[string]any:
		return redactor.RedactMetadata(typed)
	default:
		return nil, fmt.Errorf("type %T", value)
	}
}

func (redactor Redactor) sensitive(key string) bool {
	_, ok := redactor.sensitiveKeys[normalizeAuditKey(key)]
	return ok
}

func normalizeAuditKey(value string) string {
	var normalized strings.Builder
	for _, character := range strings.ToLower(strings.TrimSpace(value)) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			normalized.WriteRune(character)
		}
	}
	return normalized.String()
}

func redactString(value string) string {
	value = authorizationPattern.ReplaceAllString(value, "$1 "+RedactedValue)
	return querySecretPattern.ReplaceAllString(value, "$1="+RedactedValue)
}

func validateAuditEvent(event AuditEvent) error {
	if strings.TrimSpace(event.ID) == "" || event.OccurredAt.IsZero() || strings.TrimSpace(event.Actor.Type) == "" ||
		strings.TrimSpace(event.Action) == "" || strings.TrimSpace(event.Target.Type) == "" || strings.TrimSpace(event.RequestID) == "" {
		return fmt.Errorf("%w: id, occurred_at, actor type, action, target type, and request ID are required", ErrInvalidAuditEvent)
	}
	if utf8.RuneCountInString(event.Action) > 128 || utf8.RuneCountInString(event.Target.Type) > 64 || utf8.RuneCountInString(event.RequestID) > 255 {
		return fmt.Errorf("%w: action, target type, or request ID exceeds persistence limits", ErrInvalidAuditEvent)
	}
	switch event.Actor.Type {
	case AuditActorAnonymous, AuditActorUser, AuditActorSystem, AuditActorCustomProvider:
	default:
		return fmt.Errorf("%w: unknown actor type %q", ErrInvalidAuditEvent, event.Actor.Type)
	}
	switch event.Outcome {
	case AuditSucceeded, AuditDenied, AuditFailed:
		return nil
	default:
		return fmt.Errorf("%w: unknown outcome %q", ErrInvalidAuditEvent, event.Outcome)
	}
}

// ValidateAuditEvent lets persistence adapters apply the same invariant when
// they are used directly instead of through AuditRecorder.
func ValidateAuditEvent(event AuditEvent) error {
	return validateAuditEvent(event)
}

// MemoryAuditSink is a concurrency-safe development and test adapter. It also
// redacts defensively so direct writes cannot accidentally retain secrets.
type MemoryAuditSink struct {
	mu       sync.RWMutex
	events   []AuditEvent
	redactor Redactor
}

func NewMemoryAuditSink(extraSensitiveKeys ...string) *MemoryAuditSink {
	return &MemoryAuditSink{redactor: NewRedactor(extraSensitiveKeys...)}
}

func (sink *MemoryAuditSink) WriteAuditEvent(ctx context.Context, event AuditEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateAuditEvent(event); err != nil {
		return err
	}
	redacted, err := sink.redactor.RedactEvent(event)
	if err != nil {
		return err
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.events = append(sink.events, redacted)
	return nil
}

func (sink *MemoryAuditSink) Events(ctx context.Context) ([]AuditEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sink.mu.RLock()
	defer sink.mu.RUnlock()
	result := make([]AuditEvent, 0, len(sink.events))
	for _, event := range sink.events {
		cloned, err := sink.redactor.RedactEvent(event)
		if err != nil {
			return nil, err
		}
		result = append(result, cloned)
	}
	return result, nil
}

// SortedEvents is useful for deterministic export and tests. The sink keeps
// insertion order; this method returns an independent timestamp/ID ordered copy.
func (sink *MemoryAuditSink) SortedEvents(ctx context.Context) ([]AuditEvent, error) {
	events, err := sink.Events(ctx)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(events, func(left, right int) bool {
		if events[left].OccurredAt.Equal(events[right].OccurredAt) {
			return events[left].ID < events[right].ID
		}
		return events[left].OccurredAt.Before(events[right].OccurredAt)
	})
	return events, nil
}
