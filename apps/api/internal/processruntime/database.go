package processruntime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/moreal/jandibat.org/apps/api/internal/config"
	"github.com/moreal/jandibat.org/apps/api/internal/observability"
)

var (
	ErrDatabaseURLRequired  = errors.New("runtime: process database URL is required")
	ErrDatabaseRoleMismatch = errors.New("runtime: database URL uses the wrong process role")
)

const (
	databaseMaxOpen = 20
)

// RuntimeDatabase exposes pgxpool as the primary runtime boundary and keeps a
// database/sql view during the adapter migration. Call Close to release both.
type RuntimeDatabase struct {
	DB *sql.DB

	Pool     *pgxpool.Pool
	close    sync.Once
	closeErr error
}

// Close releases both layers of the database runtime exactly once.
func (database *RuntimeDatabase) Close() error {
	if database == nil {
		return nil
	}
	database.close.Do(func() {
		if database.DB != nil {
			database.closeErr = database.DB.Close()
		}
		if database.Pool != nil {
			database.Pool.Close()
		}
	})
	return database.closeErr
}

// DatabaseURL selects the least-privilege database role for a process. Each
// production executable receives only its own DSN and verifies the embedded
// username against its fixed role; it never needs another role's secret.
func DatabaseURL(settings config.Config, process string) (string, error) {
	var value string
	switch process {
	case config.ProcessAPI:
		value = settings.DatabaseURL
	case config.ProcessWorker:
		value = settings.WorkerDatabaseURL
	case config.ProcessMaintenance:
		value = settings.MaintenanceDatabaseURL
	default:
		return "", fmt.Errorf("runtime: unsupported process %q", process)
	}
	if settings.Environment != config.EnvironmentProduction && strings.TrimSpace(value) == "" && process != config.ProcessAPI {
		value = settings.DatabaseURL
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%w: %s", ErrDatabaseURLRequired, databaseEnvironmentName(process))
	}
	if settings.Environment == config.EnvironmentProduction {
		expected := databaseRoleName(process)
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User == nil || parsed.User.Username() != expected {
			return "", fmt.Errorf("%w: %s must authenticate as %s", ErrDatabaseRoleMismatch, databaseEnvironmentName(process), expected)
		}
	}
	return value, nil
}

func databaseRoleName(process string) string {
	switch process {
	case config.ProcessAPI:
		return "jandibat_api"
	case config.ProcessWorker:
		return "jandibat_worker"
	case config.ProcessMaintenance:
		return "jandibat_maintenance"
	default:
		return ""
	}
}

func OpenDatabase(ctx context.Context, databaseURL string) (*RuntimeDatabase, error) {
	poolConfig, err := databasePoolConfig(databaseURL, observability.Default())
	if err != nil {
		return nil, fmt.Errorf("runtime: open database: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("runtime: open database pool: %w", err)
	}
	database := &RuntimeDatabase{DB: stdlib.OpenDBFromPool(pool), Pool: pool}
	// OpenDBFromPool sets MaxIdleConns(0). Leaving MaxOpenConns unlimited is
	// intentional: pgxpool is the sole bounded checkout queue, so its acquire
	// tracer measures the complete wait rather than missing a database/sql queue.
	if err := database.DB.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("runtime: ping database: %w", err)
	}
	return database, nil
}

func databasePoolConfig(databaseURL string, registry *observability.Registry) (*pgxpool.Config, error) {
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	poolConfig.ConnConfig.Tracer = observability.NewCockroachQueryTracer(registry)
	poolConfig.MaxConns = databaseMaxOpen
	poolConfig.MaxConnIdleTime = 5 * time.Minute
	poolConfig.MaxConnLifetime = 30 * time.Minute
	return poolConfig, nil
}

func databaseEnvironmentName(process string) string {
	switch process {
	case config.ProcessAPI:
		return "DATABASE_URL"
	case config.ProcessWorker:
		return "WORKER_DATABASE_URL"
	case config.ProcessMaintenance:
		return "MAINTENANCE_DATABASE_URL"
	default:
		return "database URL"
	}
}
