package subjects

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// MemoryRepository is a thread-safe reference adapter for local development
// and tests. Pointer-bearing values are defensively copied at its boundary.
type MemoryRepository struct {
	mu sync.RWMutex

	users             map[string]User
	userIDByEmail     map[string]string
	userSettings      map[string]UserSettings
	subjects          map[string]Subject
	subjectIDByLookup map[string]string
	subjectSettings   map[string]SubjectSettings
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		users:             make(map[string]User),
		userIDByEmail:     make(map[string]string),
		userSettings:      make(map[string]UserSettings),
		subjects:          make(map[string]Subject),
		subjectIDByLookup: make(map[string]string),
		subjectSettings:   make(map[string]SubjectSettings),
	}
}

func (repository *MemoryRepository) SaveUser(ctx context.Context, user User, settings UserSettings) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateUser(user); err != nil {
		return err
	}
	if err := validateUserSettings(settings); err != nil {
		return err
	}
	emailKey := strings.ToLower(user.PrimaryEmail)
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if ownerID, exists := repository.userIDByEmail[emailKey]; exists && ownerID != user.ID {
		return ErrConflict
	}
	if existing, exists := repository.users[user.ID]; exists {
		if existing.PrimaryEmail != user.PrimaryEmail {
			delete(repository.userIDByEmail, strings.ToLower(existing.PrimaryEmail))
		}
		// Provisioning mirrors profile data after authentication; account status
		// is controlled by the account lifecycle and must never be reactivated.
		user.Status = existing.Status
	}
	repository.users[user.ID] = cloneUser(user)
	repository.userIDByEmail[emailKey] = user.ID
	if _, exists := repository.userSettings[user.ID]; !exists {
		repository.userSettings[user.ID] = settings
	}
	return nil
}

func (repository *MemoryRepository) GetUser(ctx context.Context, userID string) (User, error) {
	if err := ctx.Err(); err != nil {
		return User{}, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	user, exists := repository.users[userID]
	if !exists {
		return User{}, ErrNotFound
	}
	return cloneUser(user), nil
}

func (repository *MemoryRepository) GetUserSettings(ctx context.Context, userID string) (UserSettings, error) {
	if err := ctx.Err(); err != nil {
		return UserSettings{}, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	settings, exists := repository.userSettings[userID]
	if !exists {
		return UserSettings{}, ErrNotFound
	}
	return settings, nil
}

func (repository *MemoryRepository) SaveUserSettings(ctx context.Context, userID string, settings UserSettings) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateUserSettings(settings); err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, exists := repository.users[userID]; !exists {
		return ErrNotFound
	}
	repository.userSettings[userID] = settings
	return nil
}

func (repository *MemoryRepository) CreateSubject(ctx context.Context, subject Subject, settings SubjectSettings) error {
	_, err := repository.ClaimOrCreateSubject(ctx, subject, settings)
	return err
}

func (repository *MemoryRepository) ClaimOrCreateSubject(ctx context.Context, subject Subject, settings SubjectSettings) (Subject, error) {
	if err := ctx.Err(); err != nil {
		return Subject{}, err
	}
	if err := validateSubject(subject); err != nil {
		return Subject{}, err
	}
	if err := validateSubjectSettings(settings); err != nil {
		return Subject{}, err
	}
	if settings.SubjectID != subject.ID || settings.Timezone != subject.Timezone || settings.IsPublic != subject.IsPublic {
		return Subject{}, invalid("initial subject settings do not match the subject")
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, exists := repository.users[subject.OwnerUserID]; !exists {
		return Subject{}, ErrNotFound
	}
	if existingID, exists := repository.subjectIDByLookup[subject.Handle]; exists {
		existing := repository.subjects[existingID]
		if existing.OwnerUserID != "" {
			return Subject{}, ErrConflict
		}
		existing.OwnerUserID = subject.OwnerUserID
		existing.DisplayName = cloneString(subject.DisplayName)
		existing.Timezone = subject.Timezone
		existing.IsPublic = subject.IsPublic
		existing.UpdatedAt = subject.UpdatedAt
		settings.SubjectID = existing.ID
		settings.Timezone = existing.Timezone
		settings.IsPublic = existing.IsPublic
		repository.subjects[existing.ID] = cloneSubject(existing)
		repository.subjectSettings[existing.ID] = settings
		return cloneSubject(existing), nil
	}
	if _, exists := repository.subjectIDByLookup[subject.ID]; exists {
		return Subject{}, ErrConflict
	}
	repository.subjects[subject.ID] = cloneSubject(subject)
	repository.subjectIDByLookup[subject.ID] = subject.ID
	repository.subjectIDByLookup[subject.Handle] = subject.ID
	repository.subjectSettings[subject.ID] = settings
	return cloneSubject(subject), nil
}

func (repository *MemoryRepository) GetSubject(ctx context.Context, identifier string) (Subject, error) {
	if err := ctx.Err(); err != nil {
		return Subject{}, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	id, exists := repository.subjectIDByLookup[identifier]
	if !exists {
		return Subject{}, ErrNotFound
	}
	return cloneSubject(repository.subjects[id]), nil
}

func (repository *MemoryRepository) ListSubjects(ctx context.Context, ownerUserID string, after *SubjectCursor, limit int) ([]Subject, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit < 1 {
		return nil, ErrInvalidInput
	}
	repository.mu.RLock()
	items := make([]Subject, 0)
	for _, subject := range repository.subjects {
		if subject.OwnerUserID == ownerUserID {
			items = append(items, cloneSubject(subject))
		}
	}
	repository.mu.RUnlock()
	sort.Slice(items, func(left, right int) bool {
		if !items[left].CreatedAt.Equal(items[right].CreatedAt) {
			return items[left].CreatedAt.After(items[right].CreatedAt)
		}
		return items[left].ID < items[right].ID
	})
	result := make([]Subject, 0, min(limit, len(items)))
	for _, subject := range items {
		if after != nil && !subjectAfter(subject, *after) {
			continue
		}
		result = append(result, subject)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func (repository *MemoryRepository) SaveSubject(ctx context.Context, subject Subject) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSubject(subject); err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	existing, exists := repository.subjects[subject.ID]
	if !exists {
		return ErrNotFound
	}
	if subject.OwnerUserID != existing.OwnerUserID || subject.Timezone != existing.Timezone || subject.IsPublic != existing.IsPublic {
		return ErrConflict
	}
	if lookupID, exists := repository.subjectIDByLookup[subject.Handle]; exists && lookupID != subject.ID {
		return ErrConflict
	}
	if existing.Handle != subject.Handle {
		delete(repository.subjectIDByLookup, existing.Handle)
		repository.subjectIDByLookup[subject.Handle] = subject.ID
	}
	repository.subjects[subject.ID] = cloneSubject(subject)
	return nil
}

func (repository *MemoryRepository) DeleteSubject(ctx context.Context, subjectID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	subject, exists := repository.subjects[subjectID]
	if !exists {
		return ErrNotFound
	}
	delete(repository.subjectIDByLookup, subject.ID)
	delete(repository.subjectIDByLookup, subject.Handle)
	delete(repository.subjects, subjectID)
	delete(repository.subjectSettings, subjectID)
	return nil
}

func (repository *MemoryRepository) GetSubjectSettings(ctx context.Context, subjectID string) (SubjectSettings, error) {
	if err := ctx.Err(); err != nil {
		return SubjectSettings{}, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	settings, exists := repository.subjectSettings[subjectID]
	if !exists {
		return SubjectSettings{}, ErrNotFound
	}
	return settings, nil
}

// SaveSubjectSettings also updates the denormalized timezone and public flag on
// Subject, keeping the two OpenAPI resources consistent in one critical section.
func (repository *MemoryRepository) SaveSubjectSettings(ctx context.Context, settings SubjectSettings) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSubjectSettings(settings); err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	subject, exists := repository.subjects[settings.SubjectID]
	if !exists {
		return ErrNotFound
	}
	repository.subjectSettings[settings.SubjectID] = settings
	subject.Timezone = settings.Timezone
	subject.IsPublic = settings.IsPublic
	subject.UpdatedAt = settings.UpdatedAt
	repository.subjects[subject.ID] = subject
	return nil
}

func subjectAfter(subject Subject, cursor SubjectCursor) bool {
	if subject.CreatedAt.Before(cursor.CreatedAt) {
		return true
	}
	return subject.CreatedAt.Equal(cursor.CreatedAt) && subject.ID > cursor.ID
}
