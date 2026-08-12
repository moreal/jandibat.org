package subjects

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	defaultListLimit = 25
	maximumListLimit = 100
	maximumCursorLen = 1024
)

type Config struct {
	Now   func() time.Time
	NewID func() (string, error)
}

type Service struct {
	repository Repository
	now        func() time.Time
	newID      func() (string, error)
}

func NewService(repository Repository, config Config) (*Service, error) {
	if repository == nil {
		return nil, ErrMissingStore
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.NewID == nil {
		config.NewID = randomSubjectID
	}
	return &Service{repository: repository, now: config.Now, newID: config.NewID}, nil
}

// ProvisionUser makes an authenticated user visible to the subjects boundary.
// A repository must retain existing preferences when this method is called for
// an already-provisioned user.
func (service *Service) ProvisionUser(ctx context.Context, user User) error {
	if err := validateUser(user); err != nil {
		return err
	}
	return service.repository.SaveUser(ctx, cloneUser(user), defaultUserSettings(service.now().UTC()))
}

func (service *Service) GetCurrentUser(ctx context.Context, userID string) (User, error) {
	if strings.TrimSpace(userID) == "" {
		return User{}, ErrUnauthenticated
	}
	user, err := service.repository.GetUser(ctx, userID)
	if errors.Is(err, ErrNotFound) {
		return User{}, ErrUnauthenticated
	}
	if err != nil {
		return User{}, fmt.Errorf("get current user: %w", err)
	}
	if user.Status != UserStatusActive {
		return User{}, ErrForbidden
	}
	return cloneUser(user), nil
}

func (service *Service) GetUserSettings(ctx context.Context, userID string) (UserSettings, error) {
	if _, err := service.GetCurrentUser(ctx, userID); err != nil {
		return UserSettings{}, err
	}
	settings, err := service.repository.GetUserSettings(ctx, userID)
	if err != nil {
		return UserSettings{}, fmt.Errorf("get user settings: %w", err)
	}
	return settings, nil
}

func (service *Service) UpdateUserSettings(ctx context.Context, userID string, input UpdateUserSettingsInput) (UserSettings, error) {
	if input.Locale == nil && input.Timezone == nil && input.Theme == nil {
		return UserSettings{}, invalid("at least one user setting is required")
	}
	current, err := service.GetUserSettings(ctx, userID)
	if err != nil {
		return UserSettings{}, err
	}
	if input.Locale != nil {
		current.Locale = *input.Locale
	}
	if input.Timezone != nil {
		current.Timezone = *input.Timezone
	}
	if input.Theme != nil {
		current.Theme = *input.Theme
	}
	current.UpdatedAt = service.now().UTC()
	if err := validateUserSettings(current); err != nil {
		return UserSettings{}, err
	}
	if err := service.repository.SaveUserSettings(ctx, userID, current); err != nil {
		return UserSettings{}, fmt.Errorf("save user settings: %w", err)
	}
	return current, nil
}

func (service *Service) CreateSubject(ctx context.Context, ownerUserID string, input CreateSubjectInput) (Subject, error) {
	if _, err := service.GetCurrentUser(ctx, ownerUserID); err != nil {
		return Subject{}, err
	}
	if err := validateHandle(input.Handle); err != nil {
		return Subject{}, err
	}
	if input.DisplayName != nil {
		if err := validateDisplayName(*input.DisplayName, false); err != nil {
			return Subject{}, err
		}
	}
	if err := validateTimezone(input.Timezone); err != nil {
		return Subject{}, err
	}
	id, err := service.newID()
	if err != nil {
		return Subject{}, fmt.Errorf("generate subject id: %w", err)
	}
	now := service.now().UTC()
	isPublic := true
	if input.IsPublic != nil {
		isPublic = *input.IsPublic
	}
	subject := Subject{
		ID:          id,
		OwnerUserID: ownerUserID,
		Handle:      input.Handle,
		DisplayName: cloneString(input.DisplayName),
		Timezone:    input.Timezone,
		IsPublic:    isPublic,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := validateSubject(subject); err != nil {
		return Subject{}, err
	}
	settings := defaultSubjectSettings(subject, now)
	claimed, err := service.repository.ClaimOrCreateSubject(ctx, subject, settings)
	if err != nil {
		return Subject{}, fmt.Errorf("create subject: %w", err)
	}
	return cloneSubject(claimed), nil
}

func (service *Service) ListSubjects(ctx context.Context, ownerUserID string, input ListSubjectsInput) (SubjectList, error) {
	if _, err := service.GetCurrentUser(ctx, ownerUserID); err != nil {
		return SubjectList{}, err
	}
	limit := input.Limit
	if limit == 0 {
		limit = defaultListLimit
	}
	if limit < 1 || limit > maximumListLimit {
		return SubjectList{}, invalid("limit must be between 1 and %d", maximumListLimit)
	}
	var after *SubjectCursor
	if input.Cursor != "" {
		decoded, err := decodeCursor(input.Cursor)
		if err != nil {
			return SubjectList{}, err
		}
		after = &decoded
	}
	items, err := service.repository.ListSubjects(ctx, ownerUserID, after, limit+1)
	if err != nil {
		return SubjectList{}, fmt.Errorf("list subjects: %w", err)
	}
	result := SubjectList{Subjects: make([]Subject, 0, min(limit, len(items)))}
	for index, subject := range items {
		if index == limit {
			result.PageInfo.HasNextPage = true
			break
		}
		result.Subjects = append(result.Subjects, cloneSubject(subject))
	}
	if result.PageInfo.HasNextPage && len(result.Subjects) != 0 {
		last := result.Subjects[len(result.Subjects)-1]
		cursor := encodeCursor(SubjectCursor{CreatedAt: last.CreatedAt, ID: last.ID})
		result.PageInfo.NextCursor = &cursor
	}
	return result, nil
}

// GetSubject permits anonymous access only to public subjects. An authenticated
// non-owner is treated the same way; ownership never follows from knowing an ID.
func (service *Service) GetSubject(ctx context.Context, actorUserID, identifier string) (Subject, error) {
	if err := validateHandle(identifier); err != nil {
		return Subject{}, err
	}
	subject, err := service.repository.GetSubject(ctx, identifier)
	if err != nil {
		return Subject{}, fmt.Errorf("get subject: %w", err)
	}
	if !subject.IsPublic {
		if actorUserID != subject.OwnerUserID {
			return Subject{}, ErrForbidden
		}
		if _, err := service.GetCurrentUser(ctx, actorUserID); err != nil {
			return Subject{}, err
		}
	}
	return cloneSubject(subject), nil
}

// OwnsSubject implements the narrow authorization port used by nested HTTP
// resources. A missing subject is reported as not owned so callers do not
// learn whether another user's private identifier exists.
func (service *Service) OwnsSubject(ctx context.Context, userID, identifier string) (bool, error) {
	if strings.TrimSpace(userID) == "" {
		return false, ErrUnauthenticated
	}
	if err := validateHandle(identifier); err != nil {
		return false, err
	}
	if _, err := service.GetCurrentUser(ctx, userID); err != nil {
		return false, err
	}
	subject, err := service.repository.GetSubject(ctx, identifier)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("authorize subject: %w", err)
	}
	return subject.OwnerUserID == userID, nil
}

// AuthorizeSubjectRead returns whether a readable subject is public. Unknown
// identifiers are treated as public forge subjects for the public-activity MVP.
func (service *Service) AuthorizeSubjectRead(ctx context.Context, userID, identifier string) (bool, error) {
	if err := validateHandle(identifier); err != nil {
		return true, err
	}
	subject, err := service.repository.GetSubject(ctx, identifier)
	if errors.Is(err, ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return true, fmt.Errorf("authorize subject read: %w", err)
	}
	if subject.IsPublic {
		return true, nil
	}
	if userID == "" {
		return false, ErrUnauthenticated
	}
	if subject.OwnerUserID != userID {
		return false, ErrForbidden
	}
	if _, err := service.GetCurrentUser(ctx, userID); err != nil {
		return false, err
	}
	return false, nil
}

// ResolveSubjectID canonicalizes a managed handle while leaving unknown forge
// subject identifiers unchanged for public provider collection.
func (service *Service) ResolveSubjectID(ctx context.Context, identifier string) (string, error) {
	id, _, err := service.ResolveSubjectReference(ctx, identifier)
	return id, err
}

func (service *Service) ResolveSubjectReference(ctx context.Context, identifier string) (string, string, error) {
	if err := validateHandle(identifier); err != nil {
		return "", "", err
	}
	subject, err := service.repository.GetSubject(ctx, identifier)
	if errors.Is(err, ErrNotFound) {
		return identifier, identifier, nil
	}
	if err != nil {
		return "", "", fmt.Errorf("resolve subject: %w", err)
	}
	return subject.ID, subject.Handle, nil
}

func (service *Service) UpdateSubject(ctx context.Context, actorUserID, identifier string, input UpdateSubjectInput) (Subject, error) {
	if input.Handle == nil && !input.DisplayNameSet {
		return Subject{}, invalid("at least one subject property is required")
	}
	subject, err := service.ownedSubject(ctx, actorUserID, identifier)
	if err != nil {
		return Subject{}, err
	}
	if input.Handle != nil {
		if err := validateHandle(*input.Handle); err != nil {
			return Subject{}, err
		}
		subject.Handle = *input.Handle
	}
	if input.DisplayNameSet {
		if input.DisplayName != nil {
			if err := validateDisplayName(*input.DisplayName, true); err != nil {
				return Subject{}, err
			}
		}
		subject.DisplayName = cloneString(input.DisplayName)
	}
	subject.UpdatedAt = service.now().UTC()
	if err := service.repository.SaveSubject(ctx, subject); err != nil {
		return Subject{}, fmt.Errorf("save subject: %w", err)
	}
	return cloneSubject(subject), nil
}

func (service *Service) DeleteSubject(ctx context.Context, actorUserID, identifier string) error {
	subject, err := service.ownedSubject(ctx, actorUserID, identifier)
	if err != nil {
		return err
	}
	if err := service.repository.DeleteSubject(ctx, subject.ID); err != nil {
		return fmt.Errorf("delete subject: %w", err)
	}
	return nil
}

// AuthorizeSubjectDeletion resolves a subject to its stable internal ID after
// checking that the actor is still active and owns it. HTTP deletion uses this
// boundary before handing the stable ID to the durable operations workflow.
func (service *Service) AuthorizeSubjectDeletion(ctx context.Context, actorUserID, identifier string) (Subject, error) {
	return service.ownedSubject(ctx, actorUserID, identifier)
}

func (service *Service) GetSubjectSettings(ctx context.Context, actorUserID, identifier string) (SubjectSettings, error) {
	subject, err := service.ownedSubject(ctx, actorUserID, identifier)
	if err != nil {
		return SubjectSettings{}, err
	}
	settings, err := service.repository.GetSubjectSettings(ctx, subject.ID)
	if err != nil {
		return SubjectSettings{}, fmt.Errorf("get subject settings: %w", err)
	}
	return settings, nil
}

func (service *Service) UpdateSubjectSettings(ctx context.Context, actorUserID, identifier string, input UpdateSubjectSettingsInput) (SubjectSettings, error) {
	if input.Timezone == nil && input.IsPublic == nil && input.DefaultTheme == nil && input.WeekStart == nil &&
		input.SyncEnabled == nil && input.SyncIntervalMinutes == nil && input.FailurePolicy == nil {
		return SubjectSettings{}, invalid("at least one subject setting is required")
	}
	current, err := service.GetSubjectSettings(ctx, actorUserID, identifier)
	if err != nil {
		return SubjectSettings{}, err
	}
	if input.Timezone != nil {
		current.Timezone = *input.Timezone
	}
	if input.IsPublic != nil {
		current.IsPublic = *input.IsPublic
	}
	if input.DefaultTheme != nil {
		current.DefaultTheme = *input.DefaultTheme
	}
	if input.WeekStart != nil {
		current.WeekStart = *input.WeekStart
	}
	if input.SyncEnabled != nil {
		current.SyncEnabled = *input.SyncEnabled
	}
	if input.SyncIntervalMinutes != nil {
		current.SyncIntervalMinutes = *input.SyncIntervalMinutes
	}
	if input.FailurePolicy != nil {
		current.FailurePolicy = *input.FailurePolicy
	}
	current.UpdatedAt = service.now().UTC()
	if err := validateSubjectSettings(current); err != nil {
		return SubjectSettings{}, err
	}
	if err := service.repository.SaveSubjectSettings(ctx, current); err != nil {
		return SubjectSettings{}, fmt.Errorf("save subject settings: %w", err)
	}
	return current, nil
}

func (service *Service) ownedSubject(ctx context.Context, actorUserID, identifier string) (Subject, error) {
	if strings.TrimSpace(actorUserID) == "" {
		return Subject{}, ErrUnauthenticated
	}
	if err := validateHandle(identifier); err != nil {
		return Subject{}, err
	}
	if _, err := service.GetCurrentUser(ctx, actorUserID); err != nil {
		return Subject{}, err
	}
	subject, err := service.repository.GetSubject(ctx, identifier)
	if err != nil {
		return Subject{}, fmt.Errorf("get owned subject: %w", err)
	}
	if subject.OwnerUserID != actorUserID {
		return Subject{}, ErrForbidden
	}
	return subject, nil
}

func randomSubjectID() (string, error) {
	buffer := make([]byte, 18)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return "sub_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buffer)), nil
}

func encodeCursor(cursor SubjectCursor) string {
	payload := cursor.CreatedAt.UTC().Format(time.RFC3339Nano) + "\n" + cursor.ID
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

func decodeCursor(encoded string) (SubjectCursor, error) {
	if len(encoded) > maximumCursorLen {
		return SubjectCursor{}, invalid("cursor is too long")
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return SubjectCursor{}, invalid("cursor is malformed")
	}
	parts := strings.Split(string(payload), "\n")
	if len(parts) != 2 || parts[1] == "" {
		return SubjectCursor{}, invalid("cursor is malformed")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return SubjectCursor{}, invalid("cursor is malformed")
	}
	if err := validateNonBlankLength("cursor subject id", parts[1], 1, 64); err != nil {
		return SubjectCursor{}, err
	}
	return SubjectCursor{CreatedAt: createdAt, ID: parts[1]}, nil
}

func cloneUser(user User) User {
	if user.EmailVerifiedAt != nil {
		value := *user.EmailVerifiedAt
		user.EmailVerifiedAt = &value
	}
	return user
}

func cloneSubject(subject Subject) Subject {
	subject.DisplayName = cloneString(subject.DisplayName)
	return subject
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
