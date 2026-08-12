package subjects

import "context"

// Repository is the persistence contract for users, subjects, and their
// preferences. Subject creation persists its initial settings atomically.
type Repository interface {
	SaveUser(ctx context.Context, user User, settings UserSettings) error
	GetUser(ctx context.Context, userID string) (User, error)
	GetUserSettings(ctx context.Context, userID string) (UserSettings, error)
	SaveUserSettings(ctx context.Context, userID string, settings UserSettings) error

	CreateSubject(ctx context.Context, subject Subject, settings SubjectSettings) error
	// ClaimOrCreateSubject atomically adopts an ownerless public shadow with the
	// same handle, preserving its ID and activity FKs, or creates the supplied
	// subject when no handle exists. A subject owned by anyone is a conflict.
	ClaimOrCreateSubject(ctx context.Context, subject Subject, settings SubjectSettings) (Subject, error)
	GetSubject(ctx context.Context, identifier string) (Subject, error)
	ListSubjects(ctx context.Context, ownerUserID string, after *SubjectCursor, limit int) ([]Subject, error)
	SaveSubject(ctx context.Context, subject Subject) error
	DeleteSubject(ctx context.Context, subjectID string) error

	GetSubjectSettings(ctx context.Context, subjectID string) (SubjectSettings, error)
	SaveSubjectSettings(ctx context.Context, settings SubjectSettings) error
}
