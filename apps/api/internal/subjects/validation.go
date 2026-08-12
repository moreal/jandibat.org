package subjects

import (
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	handlePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	localePattern = regexp.MustCompile(`^[A-Za-z]{2,8}(?:-[A-Za-z0-9]{1,8})*$`)
)

func validateUser(user User) error {
	if err := validateNonBlankLength("user id", user.ID, 1, 64); err != nil {
		return err
	}
	address, err := mail.ParseAddress(user.PrimaryEmail)
	if err != nil || address.Address != user.PrimaryEmail || strings.ContainsAny(user.PrimaryEmail, "\r\n") {
		return invalid("primary email must be a valid address")
	}
	if !validUserStatus(user.Status) {
		return invalid("unsupported user status %q", user.Status)
	}
	if user.CreatedAt.IsZero() || user.UpdatedAt.IsZero() || user.UpdatedAt.Before(user.CreatedAt) {
		return invalid("user timestamps are invalid")
	}
	return nil
}

func validateUserSettings(settings UserSettings) error {
	if count := utf8.RuneCountInString(settings.Locale); count < 2 || count > 35 || !localePattern.MatchString(settings.Locale) {
		return invalid("locale must be a BCP 47 style tag between 2 and 35 characters")
	}
	if err := validateTimezone(settings.Timezone); err != nil {
		return err
	}
	if !validTheme(settings.Theme) {
		return invalid("unsupported heatmap theme %q", settings.Theme)
	}
	if settings.UpdatedAt.IsZero() {
		return invalid("settings updated time is required")
	}
	return nil
}

func validateSubject(subject Subject) error {
	if err := validateNonBlankLength("subject id", subject.ID, 1, 64); err != nil {
		return err
	}
	if err := validateNonBlankLength("owner user id", subject.OwnerUserID, 1, 64); err != nil {
		return err
	}
	if err := validateHandle(subject.Handle); err != nil {
		return err
	}
	if subject.DisplayName != nil {
		if err := validateDisplayName(*subject.DisplayName, true); err != nil {
			return err
		}
	}
	if err := validateTimezone(subject.Timezone); err != nil {
		return err
	}
	if subject.CreatedAt.IsZero() || subject.UpdatedAt.IsZero() || subject.UpdatedAt.Before(subject.CreatedAt) {
		return invalid("subject timestamps are invalid")
	}
	return nil
}

func validateSubjectSettings(settings SubjectSettings) error {
	if err := validateNonBlankLength("subject id", settings.SubjectID, 1, 64); err != nil {
		return err
	}
	if err := validateTimezone(settings.Timezone); err != nil {
		return err
	}
	if !validTheme(settings.DefaultTheme) {
		return invalid("unsupported heatmap theme %q", settings.DefaultTheme)
	}
	if settings.WeekStart != WeekStartSunday && settings.WeekStart != WeekStartMonday {
		return invalid("unsupported week start %q", settings.WeekStart)
	}
	if settings.SyncIntervalMinutes < 15 || settings.SyncIntervalMinutes > 10080 {
		return invalid("sync interval must be between 15 and 10080 minutes")
	}
	if settings.FailurePolicy != FailureKeepStale && settings.FailurePolicy != FailurePurge {
		return invalid("unsupported failure policy %q", settings.FailurePolicy)
	}
	if settings.UpdatedAt.IsZero() {
		return invalid("settings updated time is required")
	}
	return nil
}

func validateHandle(handle string) error {
	if count := utf8.RuneCountInString(handle); count < 1 || count > 64 || !handlePattern.MatchString(handle) {
		return invalid("handle must match %s and contain at most 64 characters", handlePattern.String())
	}
	return nil
}

func validateDisplayName(name string, allowEmpty bool) error {
	count := utf8.RuneCountInString(name)
	if count > 100 || (!allowEmpty && count == 0) {
		return invalid("display name must contain between 1 and 100 characters")
	}
	return nil
}

func validateTimezone(name string) error {
	if err := validateNonBlankLength("timezone", name, 1, 64); err != nil {
		return err
	}
	if _, err := time.LoadLocation(name); err != nil {
		return invalid("timezone %q is not recognized", name)
	}
	return nil
}

func validateNonBlankLength(field, value string, minimum, maximum int) error {
	count := utf8.RuneCountInString(value)
	if strings.TrimSpace(value) == "" || count < minimum || count > maximum {
		return invalid("%s must contain between %d and %d characters", field, minimum, maximum)
	}
	return nil
}

func validTheme(theme HeatmapTheme) bool {
	switch theme {
	case ThemeSystem, ThemeLight, ThemeDark, ThemeGitHubLight, ThemeGitHubDark:
		return true
	default:
		return false
	}
}

func validUserStatus(status UserStatus) bool {
	return status == UserStatusActive || status == UserStatusDisabled || status == UserStatusPending || status == UserStatusDeletionPending
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidInput, fmt.Sprintf(format, args...))
}
