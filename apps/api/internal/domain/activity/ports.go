package activity

import "context"

type SaveFactsInput struct {
	Subject SubjectID
	Facts   []Fact
}

type LoadFactsInput struct {
	Subject SubjectID
	From    *Date
	To      *Date
}

type FactWriter interface {
	SaveFacts(ctx context.Context, input SaveFactsInput) error
}

type FactReader interface {
	LoadFacts(ctx context.Context, input LoadFactsInput) ([]Fact, error)
}

type FactStore interface {
	FactWriter
	FactReader
}

type SaveEnvironmentsInput struct {
	Environments []Environment
}

type LoadEnvironmentsInput struct {
	IDs []EnvironmentID
}

type EnvironmentWriter interface {
	SaveEnvironments(ctx context.Context, input SaveEnvironmentsInput) error
}

type EnvironmentReader interface {
	LoadEnvironments(ctx context.Context, input LoadEnvironmentsInput) ([]Environment, error)
}

type EnvironmentStore interface {
	EnvironmentWriter
	EnvironmentReader
}

type ActivityStore interface {
	FactStore
	EnvironmentStore
}
