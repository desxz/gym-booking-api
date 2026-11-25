# gym-booking-api

A small, production-minded multi-tenant gym booking API in Go. Clubs are isolated
by `club_id`; members book classes, trainers create them, and every authenticated
tenant-owned query is filtered by the caller's club.

## Request Entry & Response Exit

| Stage | Exact location |
| --- | --- |
| HTTP entry point | `internal/httpapi/router.go` → `NewRouter` |
| Primary route (book a class) | `internal/httpapi/handler.go` → `(*Handler).bookClass` |
| Refresh rotation route | `internal/httpapi/handler.go` → `(*Handler).refresh` |
| Service entry | `internal/service/booking_service.go` → `(*BookingService).BookClass` |
| Auth service entry | `internal/service/auth_service.go` → `(*AuthService).Refresh`, `Login`, `Logout` |
| Transaction boundary | `internal/repository/postgres.go` → `(*Postgres).CreateBooking`, `RotateRefreshToken` |
| Success exit | `internal/httpapi/response.go` → `writeJSON` |
| Failure exit | `internal/httpapi/response.go` → `writeError` |
| No-content exit | `internal/httpapi/response.go` → `writeNoContent` |

Request flow: `SecurityHeaders → RequestID → AccessLog → Recover → RequestTimeout →
RequireAuth → RequireRole → handler → service → repository → PostgreSQL`. `Recover`
sits inside `AccessLog` so the access log records the recovered 500. `/healthz` and
`/readyz` get a short probe timeout instead of the API `RequestTimeout`.

## Interview Walkthrough Map

| # | Topic | Where to look |
| --- | --- | --- |
| 1 | Error handling | `internal/httpapi/response.go` → `writeError`; `internal/domain/errors.go` (sentinels, stable code constants, `ValidationError`) |
| 2 | Error logging & PII | `internal/httpapi/middleware.go` → `AccessLog`, `Recover`; `internal/httpapi/handler.go` → `refresh` (reuse warning without token); `internal/domain/models.go` → `User.LogValue`; `internal/config/secret.go` |
| 3 | Clean code & readability | `internal/httpapi/router.go` → `chain`, `NewRouter`; package-scoped constants; DTOs in `internal/httpapi/handler.go`; config-driven limits |
| 4 | DB credentials & secrets | `internal/config/config.go` → `Load`, `applyEnvironment`, `Validate`; `Secret.Expose`; `internal/repository/postgres.go` → `NewPostgresPool` (password never in DSN) |
| 5 | Login & permissions | `internal/auth/password.go` (bcrypt cost + dummy hash); `internal/auth/jwt.go` (jti, HS256 pinning); `internal/auth/refresh.go` (SHA-256 storage hash); `internal/service/auth_service.go`; `RequireAuth`/`RequireRole` |
| 6 | Modular design | Consumer-side interfaces in `internal/service` and `internal/httpapi`; `NewAuthService(AuthDeps)`; `cmd/api/main.go` as the only composition root |
| 7 | Auth boundaries | `internal/httpapi/middleware.go` → `RequireAuth`, `RequireRole`; `internal/auth/context.go`; club filters and refresh-family scoping in `internal/repository/postgres.go` |
| 8 | Database access | `internal/repository/postgres.go` → `CreateBooking` (row lock + idempotency), `RotateRefreshToken` (reuse revocation), `translate`; constraints/indexes in `migrations/001_init.sql` |
| 9 | General good practices | 100% unit coverage on five packages; Testcontainers integration tests; `Config.Validate`; strict JSON decoding; `.github/workflows/ci.yml`; distroless container |

## Endpoints

| Method | Path | Access |
| --- | --- | --- |
| GET | `/healthz` | Public (liveness) |
| GET | `/readyz` | Public (DB ping + shutdown gate) |
| POST | `/api/v1/auth/register` | Public (always creates a `member`) |
| POST | `/api/v1/auth/login` | Public (returns access + refresh tokens) |
| POST | `/api/v1/auth/refresh` | Public (rotates the refresh token) |
| POST | `/api/v1/auth/logout` | Any authenticated user (revokes the refresh family) |
| GET | `/api/v1/classes` | Any authenticated user, own club only |
| POST | `/api/v1/classes` | `trainer` |
| POST | `/api/v1/classes/{id}/bookings` | `member`; optional `Idempotency-Key` header |
| DELETE | `/api/v1/bookings/{id}` | Owning member or a trainer in the same club |

Errors produced by API handlers always use one shape:

```json
{"code":"class_full","message":"class is full","request_id":"...","fields":{"capacity":"..."}}
```

`fields` is omitted when empty. 5xx responses never include SQL, stack traces, or
internal error text.

## Security & Reliability Mechanics

- **Access tokens:** HS256 JWT with `jti`, `sub`, `club_id`, `role`, `iss`, `iat`,
  `exp`; verification pins HS256, issuer, and a required expiry.
- **Refresh tokens:** 32 random bytes, returned once as base64url; only the hex
  SHA-256 hash is stored. Every refresh rotates the token in one transaction.
- **Reuse detection:** replaying an already-revoked refresh token revokes the entire
  family, logs `event="refresh_token_reuse_detected"` with user/club/family IDs, and
  returns `401`. Logs never contain the raw token.
- **Timing-safe login:** unknown emails still run bcrypt against
  `auth.DummyPasswordHash`, so the unknown-user and wrong-password paths have
  comparable latency.
- **Idempotent bookings:** an optional `Idempotency-Key` (≤128 printable ASCII) is
  checked and recorded inside the booking transaction. Same key + same class returns
  the stored booking; same key + different class returns `409 idempotency_conflict`.
- **Overbooking:** booking locks the class row with `SELECT ... FOR UPDATE`;
  cancellation decrements the counter in the same transaction.

## Configuration

`configs/config.yaml` holds only non-secret operational settings:

- HTTP: timeouts, `max_body_bytes`, `shutdown_drain_delay`
- Database: pool bounds, connect/statement/lock timeouts, `max_conn_lifetime`,
  `max_conn_idle_time`, `rollback_timeout`, `ready_ping_timeout`
- Auth: `jwt_issuer`, `access_token_ttl`, `refresh_token_ttl`, `bcrypt_cost`

Secrets come only from the environment: `DB_PASSWORD`, `JWT_SECRET` (minimum 32
bytes). Optional overrides: `HTTP_ADDR`, `HTTP_SHUTDOWN_DRAIN_DELAY`, `DB_HOST`,
`DB_PORT`, `DB_NAME`, `DB_USER`, `DB_SSL_MODE`. `CONFIG_PATH` selects another YAML
file.

Strict YAML decoding (`KnownFields(true)`) fails fast on typos. `Validate` enforces
`lock_timeout < statement_timeout < request_timeout < write_timeout <
shutdown_timeout`, positive timeouts/limits, refresh TTL > access TTL, and a bcrypt
cost between 10 and 14.

## Quickstart & End-to-End Demo

Requires Go 1.26+ to build from source (`go.mod`); CI and the Docker image use
Go 1.26. Docker and `make` are enough for the compose demo.

```bash
cp .env.example .env
make up
```

The API listens on `http://localhost:8080`; PostgreSQL is seeded by
`migrations/001_init.sql`. Every demo account's password is `Password123!`.
Stop with `make down`; reset all data with `make down-v`.

```bash
curl -sS localhost:8080/healthz   # {"status":"ok"}
curl -sS localhost:8080/readyz    # {"status":"ready"}
```

### 1. Log in and keep both tokens

```bash
LOGIN=$(curl -sS -X POST http://localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"member@club-a.test","password":"Password123!"}')

ACCESS=$(echo "$LOGIN" | jq -r .access_token)
REFRESH=$(echo "$LOGIN" | jq -r .refresh_token)
```

The response also includes `token_type: "Bearer"` and `expires_at`.

### 2. Rotate the refresh token

```bash
ROTATED=$(curl -sS -X POST http://localhost:8080/api/v1/auth/refresh \
  -H 'Content-Type: application/json' \
  -d "{\"refresh_token\":\"$REFRESH\"}")

ACCESS=$(echo "$ROTATED" | jq -r .access_token)
REFRESH=$(echo "$ROTATED" | jq -r .refresh_token)
```

Replaying the old token now returns `401` and revokes the whole family.

### 3. Book with an Idempotency-Key

Seeded class `33333333-3333-3333-3333-333333333301` belongs to Club A, capacity 2.

```bash
BOOKING=$(curl -sS -X POST \
  http://localhost:8080/api/v1/classes/33333333-3333-3333-3333-333333333301/bookings \
  -H "Authorization: Bearer $ACCESS" \
  -H 'Idempotency-Key: demo-booking-1')
echo "$BOOKING" | jq -r .id
```

### 4. Replay the same request → same booking, no double increment

```bash
curl -sS -X POST \
  http://localhost:8080/api/v1/classes/33333333-3333-3333-3333-333333333301/bookings \
  -H "Authorization: Bearer $ACCESS" \
  -H 'Idempotency-Key: demo-booking-1'
# identical booking id; GET /api/v1/classes still shows booked_count == 1
```

Reusing that key for a different class returns `409 idempotency_conflict`:

```bash
curl -sS -X POST \
  http://localhost:8080/api/v1/classes/33333333-3333-3333-3333-333333333302/bookings \
  -H "Authorization: Bearer $ACCESS" \
  -H 'Idempotency-Key: demo-booking-1'
```

### 5. Log out

```bash
curl -sS -o /dev/null -w '%{http_code}\n' -X POST \
  http://localhost:8080/api/v1/auth/logout \
  -H "Authorization: Bearer $ACCESS" \
  -H 'Content-Type: application/json' \
  -d "{\"refresh_token\":\"$REFRESH\"}"
# 204; the refresh family is revoked, so refreshing it now returns 401
```

### Register a new member

```bash
curl -sS -X POST http://localhost:8080/api/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"club_id":"11111111-1111-1111-1111-111111111111","email":"demo@club-a.test","password":"Password123!"}'
# 201; re-running returns 409 email_taken
```

## Running Tests

Mocks are generated with `go.uber.org/mock` (`mockgen`) and live only in
`mock_*_test.go` files. Regenerate them with `make generate`; `make generate-check`
fails if generated mocks drift. `make check` includes that check, so run it with a
clean working tree.

```bash
make generate          # Regenerate mocks after interface changes
make test              # Fast unit + HTTP tests; no Docker
make cover             # 100% coverage gate on the five core packages
make test-flake        # Shuffled repeated core tests
make test-integration  # Full suite including Testcontainers
make check             # fmt + vet + prod purity + coverage + flake
make help              # List every available target
```

The repository suite skips gracefully under `-short` or when Docker is unavailable.
It proves, against a real `postgres:16-alpine`:

- 10 concurrent members, capacity 1 → exactly one success, nine `ErrClassFull`,
  `booked_count == 1`.
- Duplicate booking/email and SQL-injection payloads remain literal and contained.
- Cross-tenant booking/cancellation → `ErrNotFound`; cancellation frees the seat.
- Refresh T1 → T2 → T3; replaying T1 revokes T1, T2, and T3; reuse is detected.
- Logout revokes the family only for the correct user/club.
- Idempotency replay returns the identical booking with `booked_count == 1`;
  reusing the key for another class returns `ErrIdempotencyConflict`.

Testcontainers is the only dependency that needs Docker. `go.uber.org/mock` and
`github.com/stretchr/testify` are used strictly in `_test.go` files and generated
`mock_*_test.go` mocks; no test dependency is compiled into the production binary
(verified by `make check-prod-deps`).

## Known Weak Spots & Trade-offs (What I Would Improve Next)

- **Access-token revocation.** Logout revokes the refresh family, but an already
  issued access token stays valid until its 15-minute expiry. Next: a `jti` denylist
  in Redis (or very short access TTLs) for immediate revocation.
- **Rate limiting.** Login/refresh/booking endpoints are unbounded. Next: token-bucket
  limits and abuse detection at the API gateway or in Redis.
- **Retention/cleanup jobs.** Expired refresh tokens and old idempotency records grow
  forever. Next: a scheduled delete job with a retention window (e.g. 30 days).
- **Versioned migrations.** A single `migrations/001_init.sql` is applied by the
  Postgres initdb hook; there is no down migration or version ledger. Next: `goose`
  or `golang-migrate` in a separate migration job before deploy.
- **No OpenTelemetry.** There are structured logs and request IDs but no traces or
  metrics. Next: `otelhttp` + `otelpgx`, excluding bodies and Authorization headers.
- **Email enumeration.** `register` reveals an existing email with `409`. Next:
  always return the same response and send verification email asynchronously.
  Login timing is already equalized with the dummy bcrypt hash.
- **Idempotent replay after cancellation.** Replaying a previously used
  `Idempotency-Key` returns the originally stored `201` booking response even if
  that booking was later cancelled. This is standard stored-response idempotency
  semantics.
- **Club ID enumeration on `register`.** `POST /api/v1/auth/register` returns `404`
  for an unknown `club_id` (foreign-key violation). Club IDs are semi-public by
  design for club onboarding links; this is accepted alongside the email
  enumeration trade-off above.
- **Out of scope by design:** frontend, password reset, pagination, rate limiter,
  OTel SDK, and club/trainer administration endpoints.

## Code Review & Deployment Strategy

Review checklist:

- Every tenant-owned query on an authenticated route has a `club_id` predicate; the
  club comes from the verified token, never from the request body. Registration is
  the only public endpoint that selects a club.
- All SQL is parameterized. Booking/cancellation and refresh rotation use one
  transaction with row locks and a rollback defer that survives a cancelled request.
- Refresh tokens are stored only as SHA-256 hashes; reuse revokes the family; logout
  is scoped to the actor's user and club.
- Idempotency keys are scoped to `(club_id, user_id, key)`, hashed by class, checked
  and written inside the booking transaction, and never increment twice.
- Repository errors are translated at the boundary; no `pgx` types reach services or
  handlers; 5xx bodies stay opaque.
- Secrets come only from the environment, `Secret` redacts in every formatting path,
  and config invariants fail startup instead of production traffic.
- JWT verification pins HS256, issuer, and expiry; bcrypt cost is configurable and
  login is timing-equalized for unknown users.
- Tests cover concurrency, tenant isolation, injection, refresh reuse, idempotency,
  401/403/400/409/413/500, panic recovery, and every core package at 100% statements.

Zero-downtime deployment flow:

1. Build and scan the image (CI runs the same Makefile verification targets,
   including coverage, integration tests, and `make vuln`).
2. Apply expand-contract migrations first: add nullable columns/tables, deploy code
   that writes both shapes, backfill, switch reads, then drop old shapes later.
3. Roll out behind `/readyz`; a pod that cannot ping PostgreSQL never receives traffic.
4. Set `shutdown_drain_delay` (or a Kubernetes `preStop` sleep) so load balancers
   stop routing before shutdown. On SIGTERM, readiness flips false, in-flight requests
   drain within `shutdown_timeout`, then the pool closes.
5. Roll back the application independently of the database; destructive schema
   changes wait for a later release.
