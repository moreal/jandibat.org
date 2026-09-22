package processruntime

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/moreal/jandibat.org/apps/api/internal/config"
	"github.com/moreal/jandibat.org/apps/api/internal/observability"
)

func TestDatabaseURLUsesRoleSpecificProductionValue(t *testing.T) {
	settings := config.Config{
		Environment:            config.EnvironmentProduction,
		DatabaseURL:            "postgresql://jandibat_api@db/jandibat",
		WorkerDatabaseURL:      "postgresql://jandibat_worker@db/jandibat",
		MaintenanceDatabaseURL: "postgresql://jandibat_maintenance@db/jandibat",
	}
	tests := map[string]string{
		config.ProcessAPI:         settings.DatabaseURL,
		config.ProcessWorker:      settings.WorkerDatabaseURL,
		config.ProcessMaintenance: settings.MaintenanceDatabaseURL,
	}
	for process, want := range tests {
		got, err := DatabaseURL(settings, process)
		if err != nil || got != want {
			t.Errorf("DatabaseURL(%q) = %q, %v; want %q", process, got, err, want)
		}
	}
}

func TestDatabasePoolConfigHasOneBoundedTracedCheckoutQueue(t *testing.T) {
	poolConfig, err := databasePoolConfig("postgresql://worker@localhost/jandibat", observability.NewRegistry(observability.Resource{Environment: "test"}))
	if err != nil {
		t.Fatalf("databasePoolConfig() error = %v", err)
	}
	if poolConfig.MaxConns != databaseMaxOpen || poolConfig.MaxConnIdleTime != 5*time.Minute || poolConfig.MaxConnLifetime != 30*time.Minute {
		t.Fatalf("pool bounds = max %d, idle %s, lifetime %s", poolConfig.MaxConns, poolConfig.MaxConnIdleTime, poolConfig.MaxConnLifetime)
	}
	if poolConfig.ConnConfig.Tracer == nil {
		t.Fatal("database tracer does not observe SQL errors")
	}
	if _, ok := poolConfig.ConnConfig.Tracer.(pgxpool.AcquireTracer); !ok {
		t.Fatal("database tracer does not observe pool acquisition")
	}
}

func TestDatabaseURLProductionNeedsOnlyTheCurrentProcessDSN(t *testing.T) {
	tests := []struct {
		process  string
		settings config.Config
		want     string
	}{
		{config.ProcessAPI, config.Config{Environment: config.EnvironmentProduction, DatabaseURL: "postgresql://jandibat_api:secret@db/jandibat"}, "postgresql://jandibat_api:secret@db/jandibat"},
		{config.ProcessWorker, config.Config{Environment: config.EnvironmentProduction, WorkerDatabaseURL: "postgresql://jandibat_worker:secret@db/jandibat"}, "postgresql://jandibat_worker:secret@db/jandibat"},
		{config.ProcessMaintenance, config.Config{Environment: config.EnvironmentProduction, MaintenanceDatabaseURL: "postgresql://jandibat_maintenance:secret@db/jandibat"}, "postgresql://jandibat_maintenance:secret@db/jandibat"},
	}
	for _, test := range tests {
		got, err := DatabaseURL(test.settings, test.process)
		if err != nil || got != test.want {
			t.Errorf("DatabaseURL(%q) = %q, %v; want %q", test.process, got, err, test.want)
		}
	}
}

func TestDatabaseURLProductionFailsClosed(t *testing.T) {
	settings := config.Config{Environment: config.EnvironmentProduction}
	if _, err := DatabaseURL(settings, config.ProcessWorker); !errors.Is(err, ErrDatabaseURLRequired) {
		t.Fatalf("missing worker URL error = %v", err)
	}
	settings.WorkerDatabaseURL = "postgresql://jandibat_api@db/jandibat"
	if _, err := DatabaseURL(settings, config.ProcessWorker); !errors.Is(err, ErrDatabaseRoleMismatch) {
		t.Fatalf("wrong worker role error = %v", err)
	}
	settings.MaintenanceDatabaseURL = "postgresql://jandibat_worker@db/jandibat"
	if _, err := DatabaseURL(settings, config.ProcessMaintenance); !errors.Is(err, ErrDatabaseRoleMismatch) {
		t.Fatalf("wrong maintenance role error = %v", err)
	}
}

func TestDatabaseURLDevelopmentExplicitCommandCanReuseDatabaseURL(t *testing.T) {
	settings := config.Config{Environment: config.EnvironmentDevelopment, DatabaseURL: "postgresql://developer@localhost/jandibat"}
	for _, process := range []string{config.ProcessWorker, config.ProcessMaintenance} {
		got, err := DatabaseURL(settings, process)
		if err != nil || got != settings.DatabaseURL {
			t.Errorf("DatabaseURL(%q) = %q, %v", process, got, err)
		}
	}
}
