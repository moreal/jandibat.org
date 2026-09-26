// Package backup holds locally verified recovery evidence and serves its metrics.
// The checker is supplied by the caller; this package does not claim to inspect
// CockroachDB backups or remote objects by itself.
package backup

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	checkInterval   = 5 * time.Minute
	checkStaleAfter = 15 * time.Minute
	maxRecordSize   = 4096
)

// CheckResult is evidence supplied by a checker that has actually run
// SHOW BACKUP ... WITH check_files. FileChecked must never be inferred from
// schedule completion or from a backup's presence in a catalog.
type CheckResult struct {
	ChainID           string
	CollectionID      string
	RecoveryTimestamp time.Time
	FileChecked       bool
}

type Checker func(context.Context) (CheckResult, error)

type verificationRecord struct {
	ChainID           string    `json:"chain_id,omitempty"`
	CollectionID      string    `json:"collection_id,omitempty"`
	RecoveryTimestamp time.Time `json:"recovery_timestamp,omitempty"`
	LastCheckAt       time.Time `json:"last_check_at,omitempty"`
	CheckHealthy      bool      `json:"check_healthy"`
}

// VerificationState stores one Pod-local record. A new emptyDir starts unknown.
type VerificationState struct {
	checkMu sync.Mutex
	mu      sync.RWMutex
	path    string
	now     func() time.Time
	record  verificationRecord
}

func NewVerificationState(path string, now func() time.Time) (*VerificationState, error) {
	if path == "" || now == nil {
		return nil, errors.New("invalid verification configuration")
	}
	s := &VerificationState{path: path, now: now}
	data, err := readBoundedFile(path, maxRecordSize)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, errors.New("cannot read verification record")
	}
	if err := json.Unmarshal(data, &s.record); err != nil || !validRecord(s.record, now().UTC()) {
		return nil, errors.New("invalid verification record")
	}
	return s, nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("bounded file read failed")
	}
	return data, nil
}

func (s *VerificationState) LastVerified() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.record.RecoveryTimestamp
}

func (s *VerificationState) Healthy() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return healthy(s.record, s.now().UTC())
}

func healthy(r verificationRecord, now time.Time) bool {
	age := now.Sub(r.LastCheckAt)
	return r.CheckHealthy && !r.RecoveryTimestamp.IsZero() && age >= 0 && age <= checkStaleAfter
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, b := range []byte(id) {
		if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '-' || b == '.' {
			continue
		}
		return false
	}
	return true
}

func validRecord(r verificationRecord, now time.Time) bool {
	if !r.LastCheckAt.IsZero() && r.LastCheckAt.After(now) {
		return false
	}
	if r.RecoveryTimestamp.IsZero() {
		return !r.CheckHealthy && r.ChainID == "" && r.CollectionID == ""
	}
	return validID(r.ChainID) && validID(r.CollectionID) && !r.RecoveryTimestamp.After(now) && !r.LastCheckAt.IsZero()
}

// Check records each attempt. Failed, missing-file, malformed, or older
// results leave the previously verified recovery timestamp unchanged.
// Errors are deliberately generic because checker errors may include URLs,
// object names, SQL text, or credentials.
func (s *VerificationState) Check(ctx context.Context, checker Checker) error {
	s.checkMu.Lock()
	defer s.checkMu.Unlock()

	var result CheckResult
	var checkErr error
	if checker == nil {
		checkErr = errors.New("missing checker")
	} else {
		result, checkErr = checker(ctx)
	}
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.record
	next.LastCheckAt = now
	next.CheckHealthy = false
	if checkErr == nil && result.FileChecked && validID(result.ChainID) && validID(result.CollectionID) &&
		!result.RecoveryTimestamp.IsZero() && !result.RecoveryTimestamp.After(now) &&
		!result.RecoveryTimestamp.Before(next.RecoveryTimestamp) {
		next.ChainID = result.ChainID
		next.CollectionID = result.CollectionID
		next.RecoveryTimestamp = result.RecoveryTimestamp.UTC()
		next.CheckHealthy = true
	} else {
		checkErr = errors.New("verification check failed")
	}
	if err := replaceRecord(s.path, next); err != nil {
		// A failed write cannot establish fresh durable evidence.
		s.record.CheckHealthy = false
		return errors.New("cannot store verification record")
	}
	s.record = next
	if checkErr != nil {
		return errors.New("verification check failed")
	}
	return nil
}

// RunChecks makes an immediate attempt, then repeats every five minutes.
// It can run alongside a metrics listener; a slow checker does not block scrapes.
func (s *VerificationState) RunChecks(ctx context.Context, checker Checker) {
	_ = s.Check(ctx, checker)
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.Check(ctx, checker)
		}
	}
}

func replaceRecord(path string, record verificationRecord) error {
	data, err := json.Marshal(record)
	if err != nil || len(data) > maxRecordSize {
		return errors.New("invalid verification record")
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".verified-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	parent, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}

// NewVerificationMetricsHandler reads a dedicated scrape token file once.
// The handler answers while verification is unknown or unhealthy.
func NewVerificationMetricsHandler(state *VerificationState, tokenFile string) (http.Handler, error) {
	if state == nil || tokenFile == "" {
		return nil, errors.New("invalid verification metrics configuration")
	}
	token, err := readBoundedFile(tokenFile, 4096)
	if err != nil || len(token) < 32 {
		return nil, errors.New("invalid verification metrics token")
	}
	for _, b := range token {
		if b <= ' ' || b >= 0x7f {
			return nil, errors.New("invalid verification metrics token")
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.EscapedPath() != "/metrics" || r.URL.RawQuery != "" || r.URL.ForceQuery {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		auth := r.Header.Values("Authorization")
		if len(auth) != 1 || len(auth[0]) != len("Bearer ")+len(token) || !strings.HasPrefix(auth[0], "Bearer ") ||
			subtle.ConstantTimeCompare([]byte(auth[0][len("Bearer "):]), token) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		state.mu.RLock()
		record := state.record
		state.mu.RUnlock()
		healthValue := 0
		if healthy(record, state.now().UTC()) {
			healthValue = 1
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = fmt.Fprintf(w, "# TYPE jandibat_backup_verified_recovery_timestamp_seconds gauge\n"+
			"jandibat_backup_verified_recovery_timestamp_seconds %d\n"+
			"# TYPE jandibat_backup_check_healthy gauge\n"+
			"jandibat_backup_check_healthy %d\n"+
			"# TYPE jandibat_backup_last_check_timestamp_seconds gauge\n"+
			"jandibat_backup_last_check_timestamp_seconds %d\n",
			record.RecoveryTimestamp.Unix()*boolInt(!record.RecoveryTimestamp.IsZero()),
			healthValue, record.LastCheckAt.Unix()*boolInt(!record.LastCheckAt.IsZero()))
	}), nil
}

func boolInt(value bool) int64 {
	if value {
		return 1
	}
	return 0
}
