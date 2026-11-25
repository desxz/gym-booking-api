package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muratgun/gym-booking-api/internal/config"
	"github.com/muratgun/gym-booking-api/internal/domain"
)

const (
	pgErrUniqueViolation     = "23505"
	pgErrForeignKeyViolation = "23503"
	pgErrLockNotAvailable    = "55P03"
	pgErrQueryCanceled       = "57014"

	idempotencyStatusCreated = 201
)

// uniqueConstraintErrors maps database constraints to domain outcomes.
var uniqueConstraintErrors = map[string]error{
	"users_email_key":               domain.ErrEmailTaken,
	"bookings_class_id_user_id_key": domain.ErrAlreadyBooked,
	"idempotency_keys_pkey":         domain.ErrIdempotencyConflict,
}

// Postgres is the only package that knows SQL or pgx types.
type Postgres struct {
	pool            *pgxpool.Pool
	rollbackTimeout time.Duration
}

func NewPostgres(pool *pgxpool.Pool, rollbackTimeout time.Duration) *Postgres {
	return &Postgres{pool: pool, rollbackTimeout: rollbackTimeout}
}

// NewPostgresPool builds the pool programmatically and keeps the password out
// of the DSN so it cannot appear in a parse error.
func NewPostgresPool(ctx context.Context, cfg config.Database) (*pgxpool.Pool, error) {
	dsn := fmt.Sprintf("host=%s port=%d dbname=%s user=%s sslmode=%s",
		cfg.Host, cfg.Port, cfg.Name, cfg.User, cfg.SSLMode)
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse postgres config: %w", err)
	}

	poolCfg.ConnConfig.Password = cfg.Password.Expose()
	poolCfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout
	poolCfg.ConnConfig.RuntimeParams["statement_timeout"] = strconv.FormatInt(cfg.StatementTimeout.Milliseconds(), 10)
	poolCfg.ConnConfig.RuntimeParams["lock_timeout"] = strconv.FormatInt(cfg.LockTimeout.Milliseconds(), 10)
	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MinConns = cfg.MinConns
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, nil
}

func (r *Postgres) CreateUser(ctx context.Context, user domain.User) (domain.User, error) {
	const query = `
		INSERT INTO users (club_id, email, password_hash, role)
		VALUES ($1, $2, $3, $4)
		RETURNING id, club_id, email, password_hash, role, created_at`

	var created domain.User
	err := r.pool.QueryRow(ctx, query, user.ClubID, user.Email, user.PasswordHash, user.Role).
		Scan(&created.ID, &created.ClubID, &created.Email, &created.PasswordHash, &created.Role, &created.CreatedAt)
	if err != nil {
		return domain.User{}, translate(err)
	}
	return created, nil
}

func (r *Postgres) UserByEmail(ctx context.Context, email string) (domain.User, error) {
	const query = `
		SELECT id, club_id, email, password_hash, role, created_at
		FROM users
		WHERE email = $1`

	var user domain.User
	err := r.pool.QueryRow(ctx, query, email).
		Scan(&user.ID, &user.ClubID, &user.Email, &user.PasswordHash, &user.Role, &user.CreatedAt)
	if err != nil {
		return domain.User{}, translate(err)
	}
	return user, nil
}

func (r *Postgres) CreateClass(ctx context.Context, class domain.Class) (domain.Class, error) {
	const query = `
		INSERT INTO classes (club_id, trainer_id, title, starts_at, capacity)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, club_id, trainer_id, title, starts_at, capacity, booked_count, created_at`

	var created domain.Class
	err := r.pool.QueryRow(ctx, query, class.ClubID, class.TrainerID, class.Title, class.StartsAt, class.Capacity).
		Scan(&created.ID, &created.ClubID, &created.TrainerID, &created.Title, &created.StartsAt,
			&created.Capacity, &created.BookedCount, &created.CreatedAt)
	if err != nil {
		return domain.Class{}, translate(err)
	}
	return created, nil
}

func (r *Postgres) ListClasses(ctx context.Context, clubID string) ([]domain.Class, error) {
	const query = `
		SELECT id, club_id, trainer_id, title, starts_at, capacity, booked_count, created_at
		FROM classes
		WHERE club_id = $1
		ORDER BY starts_at`

	rows, err := r.pool.Query(ctx, query, clubID)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	classes := make([]domain.Class, 0)
	for rows.Next() {
		class, err := scanClass(rows)
		if err != nil {
			return nil, err
		}
		classes = append(classes, class)
	}
	if err := rows.Err(); err != nil {
		return nil, translate(err)
	}
	return classes, nil
}

// CreateBooking locks the class row, checks domain rules, inserts the booking,
// and increments the counter atomically. The row lock is what prevents two
// concurrent members from both claiming the last seat.
func (r *Postgres) CreateBooking(
	ctx context.Context, clubID, classID, userID, idempotencyKey string, now time.Time,
) (domain.Booking, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Booking{}, translate(err)
	}
	defer r.rollback(ctx, tx)

	requestHash := ""
	if idempotencyKey != "" {
		requestHash = sha256Hex(classID)

		// Serialize retries for this user before the check-then-insert. Without
		// this lock two identical concurrent retries could both miss the key.
		const lockUser = `SELECT id FROM users WHERE id = $1 AND club_id = $2 FOR UPDATE`
		var lockedUserID string
		if err := tx.QueryRow(ctx, lockUser, userID, clubID).Scan(&lockedUserID); err != nil {
			return domain.Booking{}, translate(err)
		}

		stored, found, err := findIdempotentBooking(ctx, tx, clubID, userID, idempotencyKey, requestHash)
		if err != nil {
			return domain.Booking{}, err
		}
		if found {
			return stored, translate(tx.Commit(ctx))
		}
	}

	const lockClass = `
		SELECT id, club_id, trainer_id, title, starts_at, capacity, booked_count, created_at
		FROM classes
		WHERE id = $1 AND club_id = $2
		FOR UPDATE`
	class, err := scanClass(tx.QueryRow(ctx, lockClass, classID, clubID))
	if err != nil {
		return domain.Booking{}, err
	}
	if err := class.CanAcceptBooking(now); err != nil {
		return domain.Booking{}, err
	}

	const insertBooking = `
		INSERT INTO bookings (club_id, class_id, user_id)
		VALUES ($1, $2, $3)
		RETURNING id, club_id, class_id, user_id, created_at`
	var booking domain.Booking
	err = tx.QueryRow(ctx, insertBooking, clubID, classID, userID).
		Scan(&booking.ID, &booking.ClubID, &booking.ClassID, &booking.UserID, &booking.CreatedAt)
	if err != nil {
		return domain.Booking{}, translate(err)
	}

	const increment = `UPDATE classes SET booked_count = booked_count + 1 WHERE id = $1 AND club_id = $2`
	if _, err := tx.Exec(ctx, increment, classID, clubID); err != nil {
		return domain.Booking{}, translate(err)
	}

	if idempotencyKey != "" {
		responseBody, err := json.Marshal(newStoredBooking(booking))
		if err != nil {
			return domain.Booking{}, fmt.Errorf("encode idempotent response: %w", err)
		}
		const insertKey = `
			INSERT INTO idempotency_keys (club_id, user_id, key, request_hash, status_code, response_body)
			VALUES ($1, $2, $3, $4, $5, $6::jsonb)`
		if _, err := tx.Exec(ctx, insertKey, clubID, userID, idempotencyKey,
			requestHash, idempotencyStatusCreated, string(responseBody)); err != nil {
			return domain.Booking{}, translate(err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.Booking{}, translate(err)
	}
	return booking, nil
}

// findIdempotentBooking returns a previously stored response when the key was
// already used for the same request, and a conflict when it was used for a
// different one.
func findIdempotentBooking(
	ctx context.Context, tx pgx.Tx, clubID, userID, key, requestHash string,
) (domain.Booking, bool, error) {
	const query = `
		SELECT request_hash, response_body
		FROM idempotency_keys
		WHERE club_id = $1 AND user_id = $2 AND key = $3
		FOR UPDATE`

	var storedHash string
	var body []byte
	err := tx.QueryRow(ctx, query, clubID, userID, key).Scan(&storedHash, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Booking{}, false, nil
	}
	if err != nil {
		return domain.Booking{}, false, translate(err)
	}
	if storedHash != requestHash {
		return domain.Booking{}, false, domain.ErrIdempotencyConflict
	}

	var stored storedBooking
	if err := json.Unmarshal(body, &stored); err != nil {
		return domain.Booking{}, false, fmt.Errorf("decode idempotent response: %w", err)
	}
	return stored.booking(), true, nil
}

func (r *Postgres) CreateRefreshToken(ctx context.Context, token domain.RefreshToken) error {
	const query = `
		INSERT INTO refresh_tokens (family_id, user_id, club_id, token_hash, expires_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4)`

	_, err := r.pool.Exec(ctx, query, token.UserID, token.ClubID, token.TokenHash, token.ExpiresAt)
	return translate(err)
}

// RotateRefreshToken revokes the presented token and issues its replacement in
// one transaction. Replaying an already-revoked token revokes the whole family.
func (r *Postgres) RotateRefreshToken(
	ctx context.Context, presentedHash string, now time.Time, replacementHash string, replacementExpiresAt time.Time,
) (domain.Principal, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Principal{}, translate(err)
	}
	defer r.rollback(ctx, tx)

	const lockToken = `
		SELECT rt.id, rt.family_id, rt.user_id, rt.club_id, rt.expires_at, rt.revoked_at, u.role
		FROM refresh_tokens rt
		JOIN users u ON u.id = rt.user_id
		WHERE rt.token_hash = $1
		FOR UPDATE OF rt`
	var tokenID, familyID, userID, clubID string
	var expiresAt time.Time
	var revokedAt *time.Time
	var role domain.Role
	err = tx.QueryRow(ctx, lockToken, presentedHash).
		Scan(&tokenID, &familyID, &userID, &clubID, &expiresAt, &revokedAt, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Principal{}, domain.ErrUnauthorized
	}
	if err != nil {
		return domain.Principal{}, translate(err)
	}
	principal := domain.Principal{UserID: userID, ClubID: clubID, Role: role}

	if revokedAt != nil {
		const revokeFamily = `UPDATE refresh_tokens SET revoked_at = $1 WHERE family_id = $2 AND revoked_at IS NULL`
		if _, err := tx.Exec(ctx, revokeFamily, now, familyID); err != nil {
			return domain.Principal{}, translate(err)
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.Principal{}, translate(err)
		}
		return domain.Principal{}, &domain.RefreshTokenReuseError{Principal: principal, FamilyID: familyID}
	}
	if !expiresAt.After(now) {
		return domain.Principal{}, domain.ErrUnauthorized
	}

	const insertReplacement = `
		INSERT INTO refresh_tokens (family_id, user_id, club_id, token_hash, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id`
	var replacementID string
	err = tx.QueryRow(ctx, insertReplacement, familyID, userID, clubID, replacementHash, replacementExpiresAt).
		Scan(&replacementID)
	if err != nil {
		return domain.Principal{}, translate(err)
	}

	const markRotated = `UPDATE refresh_tokens SET revoked_at = $1, replaced_by = $2 WHERE id = $3`
	if _, err := tx.Exec(ctx, markRotated, now, replacementID, tokenID); err != nil {
		return domain.Principal{}, translate(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.Principal{}, translate(err)
	}
	return principal, nil
}

// RevokeRefreshFamily revokes every active token in the family that owns the
// presented token, scoped to the actor's user and club. Unknown tokens are a no-op.
func (r *Postgres) RevokeRefreshFamily(
	ctx context.Context, presentedHash string, actor domain.Principal, now time.Time,
) error {
	const query = `
		UPDATE refresh_tokens
		SET revoked_at = $4
		WHERE revoked_at IS NULL
		  AND family_id = (
			SELECT family_id FROM refresh_tokens
			WHERE token_hash = $1 AND user_id = $2 AND club_id = $3
		  )`

	_, err := r.pool.Exec(ctx, query, presentedHash, actor.UserID, actor.ClubID, now)
	return translate(err)
}

// CancelBooking removes the booking and decrements the counter in one locked
// transaction so the seat is immediately available to others.
func (r *Postgres) CancelBooking(ctx context.Context, bookingID string, actor domain.Principal) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return translate(err)
	}
	defer r.rollback(ctx, tx)

	const lockBooking = `
		SELECT id, club_id, class_id, user_id, created_at
		FROM bookings
		WHERE id = $1 AND club_id = $2
		FOR UPDATE`
	var booking domain.Booking
	err = tx.QueryRow(ctx, lockBooking, bookingID, actor.ClubID).
		Scan(&booking.ID, &booking.ClubID, &booking.ClassID, &booking.UserID, &booking.CreatedAt)
	if err != nil {
		return translate(err)
	}
	if !booking.CanBeCancelledBy(actor) {
		return domain.ErrForbidden
	}

	const deleteBooking = `DELETE FROM bookings WHERE id = $1 AND club_id = $2`
	if _, err := tx.Exec(ctx, deleteBooking, bookingID, actor.ClubID); err != nil {
		return translate(err)
	}

	const decrement = `UPDATE classes SET booked_count = booked_count - 1 WHERE id = $1 AND club_id = $2`
	if _, err := tx.Exec(ctx, decrement, booking.ClassID, actor.ClubID); err != nil {
		return translate(err)
	}

	return translate(tx.Commit(ctx))
}

// rollback uses a detached context: the request context is often already
// cancelled when a timeout fires, but the rollback still must reach Postgres.
func (r *Postgres) rollback(ctx context.Context, tx pgx.Tx) {
	rbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.rollbackTimeout)
	defer cancel()
	_ = tx.Rollback(rbCtx)
}

// storedBooking is the JSONB shape persisted for idempotent replays.
type storedBooking struct {
	ID        string    `json:"id"`
	ClubID    string    `json:"club_id"`
	ClassID   string    `json:"class_id"`
	UserID    string    `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
}

func newStoredBooking(booking domain.Booking) storedBooking {
	return storedBooking{
		ID:        booking.ID,
		ClubID:    booking.ClubID,
		ClassID:   booking.ClassID,
		UserID:    booking.UserID,
		CreatedAt: booking.CreatedAt,
	}
}

func (s storedBooking) booking() domain.Booking {
	return domain.Booking{
		ID:        s.ID,
		ClubID:    s.ClubID,
		ClassID:   s.ClassID,
		UserID:    s.UserID,
		CreatedAt: s.CreatedAt,
	}
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanClass(row rowScanner) (domain.Class, error) {
	var class domain.Class
	err := row.Scan(&class.ID, &class.ClubID, &class.TrainerID, &class.Title, &class.StartsAt,
		&class.Capacity, &class.BookedCount, &class.CreatedAt)
	if err != nil {
		return domain.Class{}, translate(err)
	}
	return class, nil
}

// translate is the boundary where driver errors become domain errors.
func translate(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return err
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return fmt.Errorf("database error: %s", err)
	}

	switch pgErr.Code {
	case pgErrUniqueViolation:
		if mapped, ok := uniqueConstraintErrors[pgErr.ConstraintName]; ok {
			return mapped
		}
		return fmt.Errorf("database error: unexpected unique violation on %s", pgErr.ConstraintName)
	case pgErrForeignKeyViolation:
		return domain.ErrNotFound
	case pgErrLockNotAvailable, pgErrQueryCanceled:
		return fmt.Errorf("database error: %s: %w", pgErr.Message, context.DeadlineExceeded)
	default:
		return fmt.Errorf("database error: %s", pgErr.Message)
	}
}
