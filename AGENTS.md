# AGENTS.md — gym-booking-api

Binding instructions for AI agents. Product behavior is documented in `README.md`; this file is the rule set.

## 1. Project Scope & Dependency Guardrails

- Go 1.25.0 minimum (`go.mod`); CI and Docker build with Go 1.26.
- Module: `github.com/muratgun/gym-booking-api`; packages are `cmd/api` plus `internal/{auth,config,domain,httpapi,repository,service}`.
- Production dependencies are exactly four: `github.com/jackc/pgx/v5`, `golang.org/x/crypto`, `github.com/golang-jwt/jwt/v5`, `gopkg.in/yaml.v3`.
- Test-only dependencies are exactly three: `github.com/testcontainers/testcontainers-go` (including `modules/postgres`), `go.uber.org/mock`, `github.com/stretchr/testify`.
- Never import a test-only dependency from a non-test file. Generated mocks live only in `mock_*_test.go`.
- Forbidden without explicit approval: web frameworks (`gin`, `fiber`, `chi`, `echo`), ORMs/query builders (`gorm`, `sqlx`, `ent`, `squirrel`), `viper`, `zap`, `logrus`, and any new external module.
- Do not add unrequested features: rate limiter, OpenTelemetry SDK, frontend UI, password reset, pagination, refresh-token cleanup jobs, or external migration CLIs.
- No `.cursor/`, Copilot instructions, or other agent-config directories. This file is the single source of truth.
- Do not change public routes, status codes, JSON fields, or the SQL schema unless the task explicitly requires it and tests/docs are updated in the same change.
- Prefer the standard library. If a change appears to require a new module, stop and ask first.

## 2. Package Dependency Boundaries (Inward Architecture)

| Package | Allowed production imports |
| --- | --- |
| `internal/domain` | stdlib only; no HTTP, SQL, JWT, config, or logging infrastructure |
| `internal/config` | stdlib + `gopkg.in/yaml.v3` |
| `internal/auth` | stdlib + `x/crypto` + `jwt/v5` + `internal/config` + `internal/domain` |
| `internal/repository` | stdlib + `pgx/v5` (+ `pgconn`, `pgxpool`) + `internal/config` + `internal/domain`; translate every driver error here |
| `internal/service` | stdlib + `internal/domain` only; declare consumer-side interfaces; no pgx, jwt, bcrypt, or net/http |
| `internal/httpapi` | stdlib + `internal/domain` + `internal/auth`; declare its own service interfaces; never import `repository`, pgx, or concrete service types |
| `cmd/api/main.go` | composition root; the only place concrete auth/repository/service/http implementations are wired together |

Dependencies point inward only. No new package may import a higher layer or create a cycle. `main` wires; lower layers stay testable and transport-agnostic. Tests may import generated mocks only from `_test.go` files.

## 3. Security, Multi-Tenancy & Data Invariants

- Every authenticated tenant-owned SQL query must filter by `club_id`; the club comes from the verified principal, never an untrusted body. Cross-tenant lookups return `domain.ErrNotFound`, not `ErrForbidden` (no existence leak).
- Every SQL statement uses positional parameters (`$1`, `$2`, ...). Never build SQL with string concatenation or `fmt.Sprintf`.
- Booking/cancel and refresh rotation use one Postgres transaction, `SELECT ... FOR UPDATE`, and a rollback defer using `context.WithTimeout(context.WithoutCancel(ctx), database.rollback_timeout)`. Logout's family revoke is a single scoped `UPDATE`; keep it atomic.
- Booking uniqueness, capacity, role, refresh-token hash, and idempotency-key constraints are enforced by `migrations/001_init.sql` as defense in depth. Do not bypass them in application code.
- Access tokens are HS256 JWTs with `jti`, issuer, and required expiry. Refresh tokens are 32 random bytes; store only their SHA-256 hash, never plaintext.
- `JWT_SECRET` must be at least 32 bytes; `bcrypt_cost` is validated in 10–14. Never weaken either.
- `DB_PASSWORD` and `JWT_SECRET` come only from environment variables and are stored as `config.Secret`; every format/log path renders `[REDACTED]`. Never put secrets in YAML, source, tests, or logs. `.env` is gitignored.
- Never log `Authorization` headers, cookies, request/response bodies, raw access/refresh tokens, password hashes, or unmasked email. Reuse events may log `user_id`, `club_id`, and `family_id` only.
- `POST /auth/register` never accepts a `role`; accounts are always `domain.RoleMember`.
- Login must keep dummy-hash verification for unknown users; refresh must rotate tokens and revoke the whole family on reuse; `BookClass` must honor `Idempotency-Key` inside the booking transaction (same key + same class replays; different class is a conflict).
- JSON decoding is strict: unknown fields and trailing objects are rejected; bodies are capped by configured `http.max_body_bytes`.
- HTTP failures exit only through `writeError`; 4xx uses the stable `{code,message,request_id,fields?}` shape; 5xx stays opaque and never leaks SQL, stack traces, hashes, or internal error text.
- Preserve the timeout budget: `lock_timeout < statement_timeout < request_timeout < write_timeout < shutdown_timeout`, validated at startup.
- Preserve shutdown order: `ready=false` → optional drain delay → `srv.Shutdown` → deferred `pool.Close`. Never bind `http.Server.BaseContext` to the OS signal context.
- Generate UUIDs in Postgres; validate inbound UUIDs with `domain.IsUUID`; request IDs come from `crypto/rand`.

## 4. Config & Constants Hygiene

- Tunable operational limits belong in `configs/config.yaml` with fail-fast `Config.Validate()`: timeouts, pool bounds, body limit, drain delay, token TTLs, bcrypt cost, rollback/readiness timeouts.
- `configs/config.yaml` is non-secret and decoded with `KnownFields(true)`; unknown keys must fail startup.
- `Validate()` also enforces positive limits, `max_body_bytes > 0`, `shutdown_drain_delay >= 0`, `refresh_token_ttl > access_token_ttl`, `ready_ping_timeout < request_timeout`, and `min_conns <= max_conns`.
- Keep `http.shutdown_drain_delay: 0s` locally; raise it only for load-balancer/Kubernetes drain. Production code must take timeouts from config; tests must never use sleeps or real-time deadlines for synchronization.
- Environment overrides: `CONFIG_PATH`, `HTTP_ADDR`, `HTTP_SHUTDOWN_DRAIN_DELAY`, `DB_HOST`, `DB_PORT`, `DB_NAME`, `DB_USER`, `DB_SSL_MODE`, plus the two secrets above.
- Non-tunable domain/protocol invariants live in typed package-scoped `const` blocks (domain limits/roles/error codes, auth token/header constants, repository SQLSTATEs, httpapi headers/limits/content type).
- Never create a global `constants.go` dumping ground; never introduce magic strings, numbers, durations, SQLSTATEs, or header names.

## 5. Testing & Flakiness Rules

- Use `go.uber.org/mock`; regenerate via `make generate` from the `//go:generate` directives. Output only `mock_*_test.go` in the package under test. Do not hand-write service/repository fakes.
- Never edit a generated mock by hand; change the consumer-side interface and regenerate.
- Use `testify/require` for fatal preconditions and `testify/assert` for value/payload checks. Never use `testify/mock` or `testify/suite`.
- Maintain 100.0% statement coverage on `internal/domain`, `internal/auth`, `internal/config`, `internal/service`, and `internal/httpapi`. New branches require tests.
- Do not weaken assertions, delete error checks, or add no-op tests to reach the coverage gate.
- Use table-driven tests for multi-variant logic (validation, config, auth/role rejection, error mapping). Keep stateful/concurrency workflows (refresh reuse, idempotency, overbooking) linear.
- Fresh `gomock.NewController(t)` per subtest; explicit `.Times(1)` expectations; unauthorized requests must have no service expectations.
- Zero flakiness: no `time.Sleep`; inject clocks (`now func() time.Time`) and use fixed times; use `t.Setenv`/`t.Cleanup`; never mutate package globals without restoring them; no `t.Parallel` beside env/global mutation.
- Assert unordered results with `assert.ElementsMatch`; rely on slice order only when the query explicitly has `ORDER BY`.
- Testcontainers tests must isolate data with unique IDs/emails/keys and synchronize concurrency with a start barrier and buffered result channel. They skip under `-short` or without Docker.
- Every test must be independently runnable; never depend on execution order or leftover rows.
- All tests must pass with `-shuffle=on -race`.

## 6. Definition of Done (Exact Verification Commands)

Run from the repo root; do not finish until every command passes with zero warnings.

```bash
go mod tidy
make generate
make check
make test-integration
make vuln
```

`make check` includes `generate-check`; run it with a clean working tree. Docker is required for `make test-integration`, and `make vuln` requires `govulncheck` on `PATH` (CI installs it). `go mod tidy` must leave the direct requirements unchanged (four production + three test-only module families).
