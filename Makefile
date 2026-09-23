YARN ?= corepack yarn
COCKROACH_DATABASE ?= jandibat

.PHONY: install check ci nix-check ci-nix-gates-test tool-versions sql-generate sql-check sql-check-live sql-live-drift-test sql-generated-drift-check sql-generated-drift-test adapter-sql-allowlist-check graphql-generate graphql-check dev-web build-web test-web test-sdk typecheck typecheck-web typecheck-sdk dev-api dev-worker dev-maintenance test-api test-api-race test-api-integration vet-api lint-api openapi-lint openapi-types openapi-check contract-change-check ci-version-authority-check secret-scan shell-check security-review-check security-review-gate security-review-validator-test audit-verifier-test restore-verifier-test migration-atomicity-test baseline-history-test load-check security-smoke monitoring-check staging-compose-check deploy-staging rollback-rehearsal retention-dry-run retention-execute credential-reencrypt-dry-run credential-reencrypt-execute verify-deletion verify-audit-log db-up db-down db-logs db-shell db-wait db-migrate db-migrate-url db-configure-runtime-roles db-runtime-roles-test db-backup db-backup-schedule db-restore-verify

install:
	$(YARN) install --immutable

check: openapi-check graphql-check ci-version-authority-check ci-nix-gates-test secret-scan shell-check adapter-sql-allowlist-check monitoring-check security-review-validator-test audit-verifier-test restore-verifier-test test-api test-api-race lint-api test-sdk test-web typecheck build-web

ci: nix-check install check

nix-check:
	nix flake check --all-systems --no-build
	nix flake check

ci-nix-gates-test:
	sh scripts/check-ci-nix-gates.test.sh

tool-versions:
	go version
	node --version
	yarn --version
	scythe --version
	staticcheck -version
	exhaustive -V=full
	go-check-sumtype -V=full

sql-generate:
	scythe generate --config scythe.toml

sql-check:
	scythe check --config scythe.toml

sql-check-live:
	@test -n "$$SCYTHE_DATABASE_URL" || (echo "SCYTHE_DATABASE_URL is required" >&2; exit 2)
	@scythe check --config scythe.toml --database-url "$$SCYTHE_DATABASE_URL"

sql-generated-drift-check:
	sh scripts/check-scythe-generated-drift.sh

adapter-sql-allowlist-check:
	sh scripts/test-adapter-sql-allowlist.sh
	sh scripts/check-adapter-sql-allowlist.sh

graphql-generate:
	sh scripts/render-graphql-schema.sh > graphql/schema.graphql
	cd apps/api && go tool gqlgen generate
	$(YARN) relay-compiler

graphql-check:
	sh scripts/test-graphql-contract.sh
	sh scripts/test-graphql-generated-drift.sh
	sh scripts/check-graphql-generated.sh
	$(YARN) relay-compiler --validate
	cd apps/api && go test ./internal/graphql/...

sql-generated-drift-test:
	sh scripts/test-scythe-generated-drift.sh

sql-live-drift-test:
	@test -n "$$SCYTHE_DATABASE_URL" || (echo "SCYTHE_DATABASE_URL is required" >&2; exit 2)
	@sh scripts/test-scythe-live-drift.sh

dev-web:
	$(YARN) dev:web

build-web:
	$(YARN) build:web

test-web:
	$(YARN) test:web

test-sdk:
	$(YARN) test:sdk

typecheck:
	$(YARN) typecheck

typecheck-web:
	$(YARN) typecheck:web

typecheck-sdk:
	$(YARN) typecheck:sdk

dev-api:
	cd apps/api && go run ./cmd/server

dev-worker:
	cd apps/api && go run ./cmd/worker

dev-maintenance:
	cd apps/api && go run ./cmd/maintenance

test-api:
	cd apps/api && go test ./...

test-api-race:
	cd apps/api && go test -race ./...

# Requires a migrated CockroachDB. The explicit URL prevents the opt-in tests
# from silently skipping in CI while leaving ordinary unit-test runs portable.
export JANDIBAT_TEST_DATABASE_URL JANDIBAT_TEST_API_DATABASE_URL JANDIBAT_TEST_WORKER_DATABASE_URL JANDIBAT_TEST_MAINTENANCE_DATABASE_URL
test-api-integration:
	@test -n "$(JANDIBAT_TEST_DATABASE_URL)" || (echo "JANDIBAT_TEST_DATABASE_URL is required" >&2; exit 2)
	@test -n "$(JANDIBAT_TEST_API_DATABASE_URL)" || (echo "JANDIBAT_TEST_API_DATABASE_URL is required" >&2; exit 2)
	@test -n "$(JANDIBAT_TEST_WORKER_DATABASE_URL)" || (echo "JANDIBAT_TEST_WORKER_DATABASE_URL is required" >&2; exit 2)
	@test -n "$(JANDIBAT_TEST_MAINTENANCE_DATABASE_URL)" || (echo "JANDIBAT_TEST_MAINTENANCE_DATABASE_URL is required" >&2; exit 2)
	@cd apps/api && go test -tags=integration -p=1 -count=1 ./...

vet-api:
	cd apps/api && go vet ./...

lint-api:
	cd apps/api && go vet ./...
	cd apps/api && staticcheck ./...
	cd apps/api && exhaustive -check=switch,map ./...
	cd apps/api && go-check-sumtype ./...

openapi-lint:
	$(YARN) openapi:lint

openapi-types:
	$(YARN) openapi:types

openapi-check:
	$(YARN) openapi:check

contract-change-check:
	sh scripts/test-contract-change.sh
	sh scripts/check-contract-change.sh

ci-version-authority-check:
	sh scripts/check-ci-version-authority.test.sh
	sh scripts/check-ci-version-authority.sh

secret-scan:
	sh scripts/check-secrets.sh

shell-check:
	@for script in scripts/*.sh; do sh -n "$$script"; done
	sh scripts/test-api-integration-target.sh

security-review-check:
	@test -n "$(SECURITY_REVIEW)" || (echo "SECURITY_REVIEW is required" >&2; exit 2)
	node scripts/validate-security-review.mjs '$(SECURITY_REVIEW)' '$(SECURITY_REVIEW_SHA)'

security-review-gate:
	SECURITY_REVIEW_BASE='$(SECURITY_REVIEW_BASE)' SECURITY_REVIEW='$(SECURITY_REVIEW)' sh scripts/check-security-review-change.sh

security-review-validator-test:
	node --test scripts/security-review-validator.test.mjs

audit-verifier-test:
	sh scripts/verify-audit-log.test.sh

restore-verifier-test:
	sh scripts/db-restore-verify.test.sh

migration-atomicity-test:
	sh scripts/test-migration-atomicity.sh

monitoring-check:
	@ruby -e 'require "yaml"; YAML.load_file(ARGV.fetch(0)); puts "monitoring rules YAML parsed"' deploy/monitoring/prometheus-rules.yaml
	@node -e 'JSON.parse(require("fs").readFileSync(process.argv[1], "utf8")); console.log("Grafana dashboard JSON parsed")' deploy/monitoring/grafana-dashboard.json

staging-compose-check:
	API_IMAGE=example.invalid/jandibat/api:test WEB_IMAGE=example.invalid/jandibat/web:test RESTORE_TOOLS_IMAGE=example.invalid/jandibat/restore-tools:test BUILD_SHA=0000000000000000000000000000000000000000 REGION=local docker compose --env-file deploy/staging/.env.staging.example -f deploy/staging/compose.yaml config --quiet

deploy-staging:
	sh scripts/deploy-staging.sh

rollback-rehearsal:
	sh scripts/rehearse-staging-rollback.sh

retention-dry-run:
	sh scripts/purge-expired-data.sh --as-of '$(AS_OF)' --dry-run --scope '$(SCOPE)'

retention-execute:
	sh scripts/purge-expired-data.sh --as-of '$(AS_OF)' --execute --scope '$(SCOPE)' $(if $(RESUME),--resume,)

credential-reencrypt-dry-run:
	sh scripts/rotate-credentials.sh --dry-run --scope '$(SCOPE)'

credential-reencrypt-execute:
	sh scripts/rotate-credentials.sh --execute --scope '$(SCOPE)' $(if $(RESUME),--resume,)

verify-deletion:
	sh scripts/verify-deletion.sh --request-id '$(REQUEST_ID)'

verify-audit-log:
	sh scripts/verify-audit-log.sh --window '$(or $(WINDOW),24h)' --orphan-age '$(or $(ORPHAN_AGE),5m)' $(if $(OUTPUT),--output '$(OUTPUT)',)

security-smoke:
	node scripts/security-smoke.mjs

load-check:
	node scripts/load-check.mjs

db-up:
	docker compose up -d --remove-orphans cockroach

db-down:
	docker compose down

db-logs:
	docker compose logs -f cockroach

db-shell:
	docker compose exec cockroach cockroach sql --insecure --host=127.0.0.1:26258

db-wait:
	docker compose exec -T cockroach /bin/sh -ec 'for i in $$(seq 1 60); do cockroach sql --insecure --host=127.0.0.1:26258 -e "SELECT 1" >/dev/null 2>&1 && exit 0; sleep 1; done; exit 1'

db-migrate: db-wait
	sh scripts/db-migrate.sh

db-migrate-url:
	sh scripts/db-migrate-url.sh

baseline-history-test:
	sh scripts/test-baseline-schema.sh
	sh scripts/test-baseline-history-rejection.sh
	sh scripts/test-baseline-followup-history.sh
	sh scripts/test-ingest-reservation-migration.sh

db-configure-runtime-roles:
	sh scripts/db-configure-runtime-roles.sh

db-runtime-roles-test:
	@case '$(COCKROACH_DATABASE)' in *[!A-Za-z0-9_]*|'') echo 'COCKROACH_DATABASE must contain only letters, digits, and underscores' >&2; exit 2;; esac
	docker compose exec -T cockroach cockroach sql --insecure --host=127.0.0.1:26258 --database='$(COCKROACH_DATABASE)' --set=errexit=true --execute='CREATE USER IF NOT EXISTS jandibat_migrator; CREATE USER IF NOT EXISTS jandibat_api; CREATE USER IF NOT EXISTS jandibat_worker; CREATE USER IF NOT EXISTS jandibat_maintenance'
	docker compose exec -T -e MIGRATION_DATABASE_URL='postgresql://root@127.0.0.1:26258/$(COCKROACH_DATABASE)?sslmode=disable' -e COCKROACH_DATABASE='$(COCKROACH_DATABASE)' cockroach sh /workspace/scripts/db-configure-runtime-roles.sh
	docker compose exec -T -e MIGRATION_DATABASE_URL='postgresql://root@127.0.0.1:26258/$(COCKROACH_DATABASE)?sslmode=disable' -e API_DATABASE_URL='postgresql://jandibat_api@127.0.0.1:26258/$(COCKROACH_DATABASE)?sslmode=disable' -e WORKER_DATABASE_URL='postgresql://jandibat_worker@127.0.0.1:26258/$(COCKROACH_DATABASE)?sslmode=disable' -e MAINTENANCE_DATABASE_URL='postgresql://jandibat_maintenance@127.0.0.1:26258/$(COCKROACH_DATABASE)?sslmode=disable' cockroach sh /workspace/scripts/db-verify-runtime-roles.sh

db-backup:
	sh scripts/db-backup.sh

db-backup-schedule:
	sh scripts/db-configure-backup-schedule.sh

db-restore-verify:
	sh scripts/db-restore-verify.sh
