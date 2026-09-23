package subjects

import "time"

type UserStatus string

const (
	UserStatusActive          UserStatus = "active"
	UserStatusDisabled        UserStatus = "disabled"
	UserStatusPending         UserStatus = "pending"
	UserStatusDeletionPending UserStatus = "deletion_pending"
)

// User is the API-safe authenticated user profile. It intentionally contains
// no credential or session data.
type User struct {
	ID              string     `json:"id"`
	PrimaryEmail    string     `json:"primaryEmail"`
	EmailVerifiedAt *time.Time `json:"emailVerifiedAt"`
	Status          UserStatus `json:"status"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

type HeatmapTheme string

const (
	ThemeSystem      HeatmapTheme = "system"
	ThemeLight       HeatmapTheme = "light"
	ThemeDark        HeatmapTheme = "dark"
	ThemeGitHubLight HeatmapTheme = "github-light"
	ThemeGitHubDark  HeatmapTheme = "github-dark"
)

type WeekStart string

const (
	WeekStartSunday WeekStart = "sunday"
	WeekStartMonday WeekStart = "monday"
)

type FailurePolicy string

const (
	FailureKeepStale FailurePolicy = "keep_stale"
	FailurePurge     FailurePolicy = "purge"
)

type UserSettings struct {
	Locale    string       `json:"locale"`
	Timezone  string       `json:"timezone"`
	Theme     HeatmapTheme `json:"theme"`
	UpdatedAt time.Time    `json:"updatedAt"`
}

// Subject is an API-safe subject plus its internal ownership key. OwnerUserID
// is excluded from JSON because it is not part of the public OpenAPI schema.
type Subject struct {
	ID          string    `json:"id"`
	OwnerUserID string    `json:"-"`
	Handle      string    `json:"handle"`
	DisplayName *string   `json:"displayName"`
	Timezone    string    `json:"timezone"`
	IsPublic    bool      `json:"isPublic"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type SubjectSettings struct {
	SubjectID           string        `json:"-"`
	Timezone            string        `json:"timezone"`
	IsPublic            bool          `json:"isPublic"`
	DefaultTheme        HeatmapTheme  `json:"defaultTheme"`
	WeekStart           WeekStart     `json:"weekStart"`
	SyncEnabled         bool          `json:"syncEnabled"`
	SyncIntervalMinutes int           `json:"syncIntervalMinutes"`
	FailurePolicy       FailurePolicy `json:"failurePolicy"`
	UpdatedAt           time.Time     `json:"updatedAt"`
}

type UpdateUserSettingsInput struct {
	Locale   *string
	Timezone *string
	Theme    *HeatmapTheme
}

type CreateSubjectInput struct {
	Handle      string
	DisplayName *string
	Timezone    string
	IsPublic    *bool
}

// UpdateSubjectInput uses DisplayNameSet to distinguish an omitted value from
// the explicit JSON null used to clear a display name.
type UpdateSubjectInput struct {
	Handle         *string
	DisplayNameSet bool
	DisplayName    *string
}

type UpdateSubjectSettingsInput struct {
	Timezone            *string
	IsPublic            *bool
	DefaultTheme        *HeatmapTheme
	WeekStart           *WeekStart
	SyncEnabled         *bool
	SyncIntervalMinutes *int
	FailurePolicy       *FailurePolicy
}

type ListSubjectsInput struct {
	Cursor string
	Limit  int
}

type PageInfo struct {
	NextCursor  *string `json:"nextCursor,omitempty"`
	HasNextPage bool    `json:"hasNextPage"`
}

type SubjectList struct {
	Subjects []Subject `json:"subjects"`
	PageInfo PageInfo  `json:"pageInfo"`
}

// SubjectPage contains a typed keyset page. The transport owns cursor encoding.
type SubjectPage struct {
	Subjects    []Subject
	HasNextPage bool
}

type SubjectCursor struct {
	CreatedAt time.Time
	ID        string
}

func defaultUserSettings(now time.Time) UserSettings {
	return UserSettings{
		Locale:    "en-US",
		Timezone:  "UTC",
		Theme:     ThemeSystem,
		UpdatedAt: now,
	}
}

func defaultSubjectSettings(subject Subject, now time.Time) SubjectSettings {
	return SubjectSettings{
		SubjectID:           subject.ID,
		Timezone:            subject.Timezone,
		IsPublic:            subject.IsPublic,
		DefaultTheme:        ThemeSystem,
		WeekStart:           WeekStartSunday,
		SyncEnabled:         true,
		SyncIntervalMinutes: 60,
		FailurePolicy:       FailureKeepStale,
		UpdatedAt:           now,
	}
}
