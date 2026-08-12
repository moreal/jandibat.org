package activity

import "time"

// Date is a canonical calendar date string in YYYY-MM-DD format.
type Date string

type SubjectID string

type EnvironmentID string

type EnvironmentScope string

const (
	EnvironmentScopeGlobal  EnvironmentScope = "global"
	EnvironmentScopeSubject EnvironmentScope = "subject"
)

type Environment struct {
	ID           EnvironmentID
	Key          string
	Name         string
	Scope        EnvironmentScope
	OwnerSubject *SubjectID
	Metadata     map[string]string
}

type ActionID string

const (
	ActionCommit ActionID = "commit"
	ActionIssue  ActionID = "issue"
	ActionPr     ActionID = "pull_request"
	ActionCustom ActionID = "custom"
)

type MetricName string

const (
	MetricCount MetricName = "count"
)

type Metric struct {
	Name  MetricName
	Value int
}

type Level uint8

const (
	LevelNone     Level = 0
	LevelLow      Level = 1
	LevelMedium   Level = 2
	LevelHigh     Level = 3
	LevelVeryHigh Level = 4
)

type Fact struct {
	Subject       SubjectID
	Date          Date
	EnvironmentID EnvironmentID
	Action        ActionID
	Metric        Metric
	Metadata      map[string]string
}

type DayEntry struct {
	EnvironmentID EnvironmentID
	Action        ActionID
	Metric        Metric
	Metadata      map[string]string
}

type Day struct {
	Date    Date
	Count   int
	Level   Level
	Entries []DayEntry
}

type Timeline struct {
	Subject      SubjectID
	Timezone     string
	Environments []Environment
	Days         []Day
}

type FetchFailurePolicy string

const (
	FetchFailureKeepStale FetchFailurePolicy = "keep_stale"
	FetchFailurePurge     FetchFailurePolicy = "purge"
)

type CachePolicy struct {
	HotTTL  time.Duration
	ColdTTL time.Duration
}
