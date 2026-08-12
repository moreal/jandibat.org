package subjects

import "errors"

var (
	ErrInvalidInput    = errors.New("subjects: invalid input")
	ErrUnauthenticated = errors.New("subjects: unauthenticated")
	ErrForbidden       = errors.New("subjects: forbidden")
	ErrNotFound        = errors.New("subjects: not found")
	ErrConflict        = errors.New("subjects: conflict")
	ErrMissingStore    = errors.New("subjects: repository is required")
)
