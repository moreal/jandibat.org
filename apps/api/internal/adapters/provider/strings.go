package provider

import "strings"

func stringTrimSpace(value string) string    { return strings.TrimSpace(value) }
func stringTrim(value, cutset string) string { return strings.Trim(value, cutset) }
