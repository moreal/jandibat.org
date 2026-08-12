package observability

import (
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
)

var (
	logAuthorizationPattern = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[^\s,;]+`)
	logSecretPattern        = regexp.MustCompile(`(?i)(access_token|refresh_token|api_key|password|secret)=([^&\s]+)`)
	logDatabaseURLPattern   = regexp.MustCompile(`(?i)(postgres(?:ql)?://[^:/\s]+:)[^@\s]+@`)
)

// Logf writes one single-line, resource-correlated operational record. Event
// names are fixed call-site values; the message is quoted so errors and
// request-derived values cannot inject extra fields or log lines.
func Logf(event, format string, args ...any) {
	Default().Logf(event, format, args...)
}

// Logf writes through the standard process logger with this registry's
// deployment identity.
func (registry *Registry) Logf(event, format string, args ...any) {
	if registry == nil {
		registry = Default()
	}
	registry.mu.RLock()
	resource := registry.resource
	registry.mu.RUnlock()
	log.Print(FormatLog(resource, event, fmt.Sprintf(format, args...)))
}

// FormatLog produces the stable key-value body used by Logf. It is exported so
// process packages can test their logging contract without replacing global
// logger output.
func FormatLog(resource Resource, event, message string) string {
	resource = normalizeResource(resource)
	event = boundedLogEvent(event)
	message = redactLogMessage(message)
	return "build_sha=" + strconv.Quote(resource.BuildSHA) +
		" environment=" + strconv.Quote(resource.Environment) +
		" region=" + strconv.Quote(resource.Region) +
		" event=" + strconv.Quote(event) +
		" message=" + strconv.Quote(message)
}

func redactLogMessage(message string) string {
	message = logAuthorizationPattern.ReplaceAllString(message, "$1 [REDACTED]")
	message = logSecretPattern.ReplaceAllString(message, "$1=[REDACTED]")
	return logDatabaseURLPattern.ReplaceAllString(message, "$1[REDACTED]@")
}

func boundedLogEvent(event string) string {
	event = strings.TrimSpace(event)
	if event == "" || len(event) > 96 {
		return "unknown"
	}
	for _, character := range event {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '.' || character == '_' {
			continue
		}
		return "unknown"
	}
	return event
}
