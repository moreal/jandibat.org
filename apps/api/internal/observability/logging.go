package observability

import (
	"regexp"
	"strings"
)

var (
	logAuthorizationPattern = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[^\s,;]+`)
	logSecretPattern        = regexp.MustCompile(`(?i)(access_token|refresh_token|api_key|password|secret)=([^&\s]+)`)
	logDatabaseURLPattern   = regexp.MustCompile(`(?i)(postgres(?:ql)?://[^:/\s]+:)[^@\s]+@`)
)

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
