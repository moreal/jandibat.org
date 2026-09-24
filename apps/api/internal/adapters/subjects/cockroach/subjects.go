package cockroach

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/subjects/cockroach/generated"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

func (store *Store) CreateSubject(ctx context.Context, subject subjects.Subject, settings subjects.SubjectSettings) error {
	_, err := store.ClaimOrCreateSubject(ctx, subject, settings)
	return err
}

func (store *Store) ClaimOrCreateSubject(ctx context.Context, subject subjects.Subject, settings subjects.SubjectSettings) (subjects.Subject, error) {
	if subject.ID == "" || subject.OwnerUserID == "" || subject.Handle == "" || settings.SubjectID != subject.ID || settings.Timezone != subject.Timezone || settings.IsPublic != subject.IsPublic || subject.CreatedAt.IsZero() || settings.UpdatedAt.IsZero() || settings.SyncIntervalMinutes < 15 || settings.SyncIntervalMinutes > 10080 {
		return subjects.Subject{}, subjects.ErrInvalidInput
	}
	var created subjects.Subject
	err := store.inTx(ctx, func(txctx context.Context, tx pgx.Tx) error {
		var err error
		var claimed bool
		if subject.DisplayName == nil {
			row, queryErr := generated.ClaimPublicSubject(txctx, tx, subject.Handle, subject.OwnerUserID, subject.Timezone, subject.IsPublic, subject.UpdatedAt)
			err = queryErr
			if row != nil {
				created = subjectFrom(row.Id, row.OwnerUserId, row.DisplayName, row.Handle, row.Timezone, row.IsPublic, row.CreatedAt, row.UpdatedAt)
				claimed = true
			}
		} else {
			row, queryErr := generated.ClaimPublicSubjectWithDisplay(txctx, tx, subject.Handle, subject.OwnerUserID, *subject.DisplayName, subject.Timezone, subject.IsPublic, subject.UpdatedAt)
			err = queryErr
			if row != nil {
				created = subjectFrom(row.Id, row.OwnerUserId, row.DisplayName, row.Handle, row.Timezone, row.IsPublic, row.CreatedAt, row.UpdatedAt)
				claimed = true
			}
		}
		if err != nil {
			return persistenceError(err)
		}
		if claimed {
			settings.SubjectID, settings.Timezone, settings.IsPublic = created.ID, created.Timezone, created.IsPublic
		} else {
			owner := subject.OwnerUserID
			if _, err = generated.InsertSubject(txctx, tx, subject.ID, &owner, subject.Handle, subject.DisplayName, subject.Timezone, subject.IsPublic, subject.CreatedAt, subject.UpdatedAt); err != nil {
				return persistenceError(err)
			}
			created = subject
		}
		_, err = generated.UpsertSubjectSettings(txctx, tx, settings.SubjectID, string(settings.DefaultTheme), string(settings.WeekStart), settings.SyncEnabled, int32(settings.SyncIntervalMinutes), string(settings.FailurePolicy), settings.UpdatedAt) // #nosec G115 -- entry validation limits the interval to 15..10080 before the transaction.
		return persistenceError(err)
	})
	return created, err
}

func (store *Store) GetSubject(ctx context.Context, identifier string) (subjects.Subject, error) {
	row, err := generated.GetSubjectByIdentifier(ctx, store.executor(ctx), identifier)
	if err != nil {
		return subjects.Subject{}, persistenceError(err)
	}
	if row == nil {
		return subjects.Subject{}, subjects.ErrNotFound
	}
	return subjectFrom(row.Id, row.OwnerUserId, row.DisplayName, row.Handle, row.Timezone, row.IsPublic, row.CreatedAt, row.UpdatedAt), nil
}

func (store *Store) ListSubjects(ctx context.Context, ownerID string, after *subjects.SubjectCursor, limit int) ([]subjects.Subject, error) {
	if limit < 1 {
		return nil, subjects.ErrInvalidInput
	}
	var rows []generated.ListSubjectsFirstPageRow
	var err error
	if after == nil {
		rows, err = generated.ListSubjectsFirstPage(ctx, store.executor(ctx), ownerID, int64(limit))
	} else {
		var cursorRows []generated.ListSubjectsAfterRow
		cursorRows, err = generated.ListSubjectsAfter(ctx, store.executor(ctx), ownerID, after.CreatedAt, after.ID, int64(limit))
		rows = make([]generated.ListSubjectsFirstPageRow, 0, len(cursorRows))
		for _, row := range cursorRows {
			rows = append(rows, generated.ListSubjectsFirstPageRow(row))
		}
	}
	if err != nil {
		return nil, persistenceError(err)
	}
	items := make([]subjects.Subject, 0, len(rows))
	for _, row := range rows {
		items = append(items, subjectFrom(row.Id, row.OwnerUserId, row.DisplayName, row.Handle, row.Timezone, row.IsPublic, row.CreatedAt, row.UpdatedAt))
	}
	return items, nil
}

func (store *Store) SaveSubject(ctx context.Context, subject subjects.Subject) error {
	if subject.ID == "" || subject.OwnerUserID == "" || subject.Handle == "" || subject.UpdatedAt.IsZero() {
		return subjects.ErrInvalidInput
	}
	var count int64
	var err error
	if subject.DisplayName == nil {
		count, err = generated.UpdateSubjectWithoutDisplay(ctx, store.executor(ctx), subject.ID, subject.Handle, subject.UpdatedAt, subject.OwnerUserID, subject.Timezone, subject.IsPublic)
	} else {
		count, err = generated.UpdateSubject(ctx, store.executor(ctx), subject.ID, subject.Handle, *subject.DisplayName, subject.UpdatedAt, subject.OwnerUserID, subject.Timezone, subject.IsPublic)
	}
	if err != nil {
		return persistenceError(err)
	}
	if count != 0 {
		return nil
	}
	exists, err := generated.SubjectExists(ctx, store.executor(ctx), subject.ID)
	if err != nil {
		return persistenceError(err)
	}
	if !exists.Exists {
		return subjects.ErrNotFound
	}
	return subjects.ErrConflict
}

func (store *Store) DeleteSubject(ctx context.Context, subjectID string) error {
	return persistenceError(store.inTx(ctx, func(txctx context.Context, tx pgx.Tx) error {
		var err error
		_, err = generated.LockSubject(txctx, tx, subjectID)
		if errors.Is(err, pgx.ErrNoRows) {
			return subjects.ErrNotFound
		}
		if err != nil {
			return persistenceError(err)
		}
		if _, err = generated.LockSubjectConnections(txctx, tx, subjectID); err != nil {
			return persistenceError(err)
		}
		if _, err = generated.QueueSubjectRevocations(txctx, tx, subjectID); err != nil {
			return persistenceError(err)
		}
		count, err := generated.DeleteSubject(txctx, tx, subjectID)
		if err != nil {
			return persistenceError(err)
		}
		if count == 0 {
			return subjects.ErrNotFound
		}
		return nil
	}))
}
