package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

var errMaintenanceCommandUsage = errors.New("maintenance: invalid operator command")

func isMaintenanceCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "retention", "reencrypt", "delete", "verify-deletion":
		return true
	default:
		return false
	}
}

func runMaintenanceCommand(ctx context.Context, app *maintenanceApplication, args []string, output io.Writer) error {
	if app == nil || app.store == nil || app.clock == nil || len(args) == 0 {
		return errMaintenanceCommandUsage
	}
	switch args[0] {
	case "retention":
		return runRetentionCommand(ctx, app, args[1:], output)
	case "reencrypt":
		return runReencryptionCommand(ctx, app, args[1:], output)
	case "delete":
		return runDeletionCommand(ctx, app, args[1:], output)
	case "verify-deletion":
		return runVerifyDeletionCommand(ctx, app, args[1:], output)
	default:
		return fmt.Errorf("%w: unknown command %q", errMaintenanceCommandUsage, args[0])
	}
}

func runRetentionCommand(ctx context.Context, app *maintenanceApplication, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("retention", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asOfText := flags.String("as-of", "", "fixed RFC3339 cutoff evaluation time")
	dryRun := flags.Bool("dry-run", false, "count eligible rows without mutation")
	execute := flags.Bool("execute", false, "delete eligible rows")
	resume := flags.Bool("resume", false, "resume the durable scope checkpoint")
	scope := flags.String("scope", "", "stable operator run scope")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || (*dryRun == *execute) {
		return fmt.Errorf("%w: retention requires exactly one of --dry-run or --execute", errMaintenanceCommandUsage)
	}
	asOf, err := time.Parse(time.RFC3339, strings.TrimSpace(*asOfText))
	if err != nil {
		return fmt.Errorf("%w: --as-of must be RFC3339", errMaintenanceCommandUsage)
	}
	if *execute && strings.TrimSpace(*scope) == "" {
		return fmt.Errorf("%w: retention --execute requires --scope", errMaintenanceCommandUsage)
	}
	result, runErr := app.retention.RunOperator(ctx, app.store, operations.RetentionOperatorOptions{
		AsOf: asOf, DryRun: *dryRun, Resume: *resume, Scope: *scope,
	})
	if err := writeOperatorJSON(output, map[string]any{
		"operation": "retention", "dry_run": result.DryRun, "as_of": asOf.UTC(),
		"matched": retentionCounts(result.Matched), "deleted": retentionCounts(result.Deleted),
		"failed_datasets": maintenanceDatasetNames(result.Failed), "truncated_datasets": maintenanceDatasetNames(result.Truncated),
	}); err != nil {
		return errors.Join(runErr, err)
	}
	return runErr
}

func runReencryptionCommand(ctx context.Context, app *maintenanceApplication, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("reencrypt", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dryRun := flags.Bool("dry-run", false, "inventory rows without mutation")
	execute := flags.Bool("execute", false, "perform CAS re-encryption")
	resume := flags.Bool("resume", false, "resume the durable scope checkpoint")
	scope := flags.String("scope", "", "stable rotation scope")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || (*dryRun == *execute) {
		return fmt.Errorf("%w: reencrypt requires exactly one of --dry-run or --execute", errMaintenanceCommandUsage)
	}
	if *execute && strings.TrimSpace(*scope) == "" {
		return fmt.Errorf("%w: reencrypt --execute requires --scope", errMaintenanceCommandUsage)
	}
	result, runErr := app.reencrypt.RunOperator(ctx, app.store, operations.ReencryptionOperatorOptions{
		DryRun: *dryRun, Resume: *resume, Scope: *scope,
	})
	if err := writeOperatorJSON(output, map[string]any{
		"operation": "credential_reencryption", "dry_run": result.DryRun, "resumed": result.Resumed,
		"scanned": result.Scanned, "rotated": result.Rotated, "pending": result.Pending,
		"skipped": result.Skipped, "failed": result.Failed, "by_key": result.ByKey,
	}); err != nil {
		return errors.Join(runErr, err)
	}
	return runErr
}

func runDeletionCommand(ctx context.Context, app *maintenanceApplication, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("delete", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	requestID := flags.String("request-id", "", "stable deletion request correlation ID")
	targetType := flags.String("target-type", "", "account or subject")
	targetID := flags.String("target-id", "", "internal target identifier")
	requestOnly := flags.Bool("request-only", false, "persist without running the workflow")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || strings.TrimSpace(*requestID) == "" || strings.TrimSpace(*targetID) == "" {
		return fmt.Errorf("%w: delete requires --request-id, --target-type, and --target-id", errMaintenanceCommandUsage)
	}
	workflow, err := operations.NewDeletionWorkflow(app.store, app.clock, app.audit, app.deletionPseudonymKey)
	if err != nil {
		return err
	}
	request, err := workflow.Request(ctx, *requestID, operations.DeletionTargetType(*targetType), *targetID)
	if err == nil && !*requestOnly {
		now := app.clock.Now().UTC()
		request, err = app.store.ClaimDeletion(ctx, request.RequestID, now, now.Add(16*time.Minute))
		if err == nil {
			request, err = workflow.RunClaimed(ctx, request)
		}
	}
	summary := deletionSummary(request)
	if writeErr := writeOperatorJSON(output, summary); writeErr != nil {
		return errors.Join(err, writeErr)
	}
	return err
}

func runVerifyDeletionCommand(ctx context.Context, app *maintenanceApplication, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("verify-deletion", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	requestID := flags.String("request-id", "", "deletion request correlation ID")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || strings.TrimSpace(*requestID) == "" {
		return fmt.Errorf("%w: verify-deletion requires --request-id", errMaintenanceCommandUsage)
	}
	request, err := app.store.LoadDeletion(ctx, *requestID)
	if err != nil {
		return err
	}
	residuals, err := app.store.VerifyDeletion(ctx, request)
	if writeErr := writeOperatorJSON(output, map[string]any{
		"operation": "verify_deletion", "request_id": request.RequestID,
		"status": request.Status, "residual_total": residuals.Total(), "residuals": residuals,
	}); writeErr != nil {
		return errors.Join(err, writeErr)
	}
	if err == nil && residuals.Total() != 0 {
		return fmt.Errorf("%w: %d rows remain", operations.ErrDeletionResiduals, residuals.Total())
	}
	return err
}

func retentionCounts(counts map[operations.RetentionDataset]int64) map[string]int64 {
	result := make(map[string]int64, len(counts))
	for dataset, count := range counts {
		result[string(dataset)] = count
	}
	return result
}

func deletionSummary(request operations.DeletionRequest) map[string]any {
	return map[string]any{
		"operation": "deletion", "request_id": request.RequestID, "target_type": request.TargetType,
		"status": request.Status, "last_completed_stage": request.LastCompletedStage,
		"error_code": request.ErrorCode, "subject_count": len(request.SubjectIDs),
		"completed_at": request.CompletedAt, "backup_expiry_at": request.BackupExpiryAt,
	}
}

func writeOperatorJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("encode operator result: %w", err)
	}
	return nil
}
