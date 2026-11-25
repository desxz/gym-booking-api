package repository

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/muratgun/gym-booking-api/internal/auth"
	"github.com/muratgun/gym-booking-api/internal/domain"
)

var (
	container  *postgres.PostgresContainer
	testPool   *pgxpool.Pool
	startupErr error
	startOnce  sync.Once
	emailSeq   atomic.Int64
	tokenSeq   atomic.Int64
)

func TestMain(m *testing.M) {
	flag.Parse()
	code := m.Run()
	if testPool != nil {
		testPool.Close()
	}
	if container != nil {
		_ = container.Terminate(context.Background())
	}
	os.Exit(code)
}

// testDB lazily starts one Postgres container per test binary and applies the
// real migration, including seed data.
func testDB(t *testing.T) *Postgres {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test requires Docker; run without -short")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)

	startOnce.Do(func() {
		container, testPool, startupErr = startPostgres()
	})
	require.NoError(t, startupErr, "start postgres container")
	return NewPostgres(testPool, 2*time.Second)
}

func startPostgres() (*postgres.PostgresContainer, *pgxpool.Pool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	migration, err := filepath.Abs("../../migrations/001_init.sql")
	if err != nil {
		return nil, nil, fmt.Errorf("resolve migration path: %w", err)
	}

	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("gym_booking"),
		postgres.WithUsername("gym_app"),
		postgres.WithPassword("test_password"),
		postgres.WithInitScripts(migration),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("run container: %w", err)
	}

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return nil, nil, fmt.Errorf("connection string: %w", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, nil, fmt.Errorf("ping: %w", err)
	}
	return container, pool, nil
}

func createClub(t *testing.T, name string) string {
	t.Helper()
	var id string
	err := testPool.QueryRow(context.Background(),
		"INSERT INTO clubs (name) VALUES ($1) RETURNING id", name).Scan(&id)
	require.NoError(t, err, "create club")
	return id
}

func createUser(t *testing.T, repo *Postgres, clubID string, role domain.Role) domain.User {
	t.Helper()
	user, err := repo.CreateUser(context.Background(), domain.User{
		ClubID:       clubID,
		Email:        uniqueEmail(string(role)),
		PasswordHash: "not-used-in-repository-tests",
		Role:         role,
	})
	require.NoError(t, err, "create user")
	return user
}

func createClass(t *testing.T, repo *Postgres, clubID, trainerID string, capacity int) domain.Class {
	t.Helper()
	now := time.Now().UTC()
	class, err := domain.NewClass(clubID, trainerID, "Test Class", now.Add(time.Hour), capacity, now)
	require.NoError(t, err, "build class")
	created, err := repo.CreateClass(context.Background(), class)
	require.NoError(t, err, "create class")
	return created
}

func uniqueEmail(prefix string) string {
	return fmt.Sprintf("%s-%d-%d@test.local", prefix, time.Now().UnixNano(), emailSeq.Add(1))
}

func uniqueRawToken(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, tokenSeq.Add(1))
}

func bookedCount(t *testing.T, classID, clubID string) int {
	t.Helper()
	var count int
	err := testPool.QueryRow(context.Background(),
		"SELECT booked_count FROM classes WHERE id = $1 AND club_id = $2", classID, clubID).Scan(&count)
	require.NoError(t, err, "read booked_count")
	return count
}

// TestCreateBookingConcurrency proves that row-level locking prevents two
// members from claiming the same last seat.
func TestCreateBookingConcurrency(t *testing.T) {
	repo := testDB(t)
	clubID := createClub(t, "Concurrency Club")
	trainer := createUser(t, repo, clubID, domain.RoleTrainer)
	class := createClass(t, repo, clubID, trainer.ID, 1)

	const racers = 10
	userIDs := make([]string, racers)
	for i := range userIDs {
		userIDs[i] = createUser(t, repo, clubID, domain.RoleMember).ID
	}

	now := time.Now().UTC()
	start := make(chan struct{})
	results := make(chan error, racers)
	var waitGroup sync.WaitGroup

	for _, userID := range userIDs {
		waitGroup.Add(1)
		go func(userID string) {
			defer waitGroup.Done()
			<-start
			_, err := repo.CreateBooking(context.Background(), clubID, class.ID, userID, "", now)
			results <- err
		}(userID)
	}

	close(start)
	waitGroup.Wait()
	close(results)

	successes, full := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, domain.ErrClassFull):
			full++
		default:
			require.Failf(t, "unexpected booking error", "%v", err)
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, racers-1, full)
	assert.Equal(t, 1, bookedCount(t, class.ID, clubID))
}

func TestDuplicateBooking(t *testing.T) {
	repo := testDB(t)
	clubID := createClub(t, "Duplicate Club")
	trainer := createUser(t, repo, clubID, domain.RoleTrainer)
	member := createUser(t, repo, clubID, domain.RoleMember)
	class := createClass(t, repo, clubID, trainer.ID, 5)

	now := time.Now().UTC()
	_, err := repo.CreateBooking(context.Background(), clubID, class.ID, member.ID, "", now)
	require.NoError(t, err, "first booking")
	_, err = repo.CreateBooking(context.Background(), clubID, class.ID, member.ID, "", now)
	require.ErrorIs(t, err, domain.ErrAlreadyBooked)
	assert.Equal(t, 1, bookedCount(t, class.ID, clubID))
}

func TestDuplicateEmail(t *testing.T) {
	repo := testDB(t)
	clubID := createClub(t, "Email Club")
	user := createUser(t, repo, clubID, domain.RoleMember)

	_, err := repo.CreateUser(context.Background(), domain.User{
		ClubID:       clubID,
		Email:        user.Email,
		PasswordHash: "not-used",
		Role:         domain.RoleMember,
	})
	require.ErrorIs(t, err, domain.ErrEmailTaken)
}

func TestMultiTenantIsolation(t *testing.T) {
	repo := testDB(t)
	clubAID := createClub(t, "Tenant A")
	clubBID := createClub(t, "Tenant B")
	trainerA := createUser(t, repo, clubAID, domain.RoleTrainer)
	memberA := createUser(t, repo, clubAID, domain.RoleMember)
	memberB := createUser(t, repo, clubBID, domain.RoleMember)
	classA := createClass(t, repo, clubAID, trainerA.ID, 5)

	_, err := repo.CreateBooking(context.Background(), clubBID, classA.ID, memberB.ID, "", time.Now().UTC())
	require.ErrorIs(t, err, domain.ErrNotFound)

	booking, err := repo.CreateBooking(context.Background(), clubAID, classA.ID, memberA.ID, "", time.Now().UTC())
	require.NoError(t, err)
	err = repo.CancelBooking(context.Background(), booking.ID, domain.Principal{
		UserID: memberB.ID,
		ClubID: clubBID,
		Role:   domain.RoleMember,
	})
	require.ErrorIs(t, err, domain.ErrNotFound)

	classesB, err := repo.ListClasses(context.Background(), clubBID)
	require.NoError(t, err)
	assert.Empty(t, classesB)

	classesA, err := repo.ListClasses(context.Background(), clubAID)
	require.NoError(t, err)
	require.Len(t, classesA, 1)
	assert.Equal(t, classA.ID, classesA[0].ID)
}

func TestSQLInjectionPayloadsAreLiteral(t *testing.T) {
	repo := testDB(t)
	clubID := createClub(t, "Injection Club")
	trainer := createUser(t, repo, clubID, domain.RoleTrainer)
	payload := fmt.Sprintf("inject-%d ' OR 1=1; DROP TABLE bookings; --", emailSeq.Add(1))

	user, err := repo.CreateUser(context.Background(), domain.User{
		ClubID:       clubID,
		Email:        payload,
		PasswordHash: "not-used",
		Role:         domain.RoleMember,
	})
	require.NoError(t, err)
	found, err := repo.UserByEmail(context.Background(), payload)
	require.NoError(t, err)
	assert.Equal(t, user.ID, found.ID)
	assert.Equal(t, payload, found.Email)

	class, err := repo.CreateClass(context.Background(), domain.Class{
		ClubID:    clubID,
		TrainerID: trainer.ID,
		Title:     payload,
		StartsAt:  time.Now().UTC().Add(time.Hour),
		Capacity:  5,
	})
	require.NoError(t, err)
	classes, err := repo.ListClasses(context.Background(), clubID)
	require.NoError(t, err)
	require.Len(t, classes, 1)
	assert.Equal(t, class.ID, classes[0].ID)
	assert.Equal(t, payload, classes[0].Title)

	integrityQueries := []struct {
		table string
		query string
	}{
		{"users", "SELECT count(*) FROM users"},
		{"classes", "SELECT count(*) FROM classes"},
		{"bookings", "SELECT count(*) FROM bookings"},
	}
	for _, check := range integrityQueries {
		var count int
		require.NoError(t, testPool.QueryRow(context.Background(), check.query).Scan(&count), "table %s", check.table)
	}
}

func TestCancelAndRebook(t *testing.T) {
	repo := testDB(t)
	clubID := createClub(t, "Cancel Club")
	trainer := createUser(t, repo, clubID, domain.RoleTrainer)
	memberOne := createUser(t, repo, clubID, domain.RoleMember)
	memberTwo := createUser(t, repo, clubID, domain.RoleMember)
	class := createClass(t, repo, clubID, trainer.ID, 1)

	now := time.Now().UTC()
	booking, err := repo.CreateBooking(context.Background(), clubID, class.ID, memberOne.ID, "", now)
	require.NoError(t, err, "first booking")
	_, err = repo.CreateBooking(context.Background(), clubID, class.ID, memberTwo.ID, "", now)
	require.ErrorIs(t, err, domain.ErrClassFull)

	actor := domain.Principal{UserID: memberOne.ID, ClubID: clubID, Role: domain.RoleMember}
	require.NoError(t, repo.CancelBooking(context.Background(), booking.ID, actor))
	assert.Equal(t, 0, bookedCount(t, class.ID, clubID))

	rebooked, err := repo.CreateBooking(context.Background(), clubID, class.ID, memberTwo.ID, "", now)
	require.NoError(t, err, "re-book after cancel")
	assert.Equal(t, 1, bookedCount(t, class.ID, clubID))

	// A member in the same club still cannot cancel someone else's booking.
	err = repo.CancelBooking(context.Background(), rebooked.ID, actor)
	require.ErrorIs(t, err, domain.ErrForbidden)
}

func TestRefreshTokenRotationAndReuseDetection(t *testing.T) {
	repo := testDB(t)
	clubID := createClub(t, "Refresh Club")
	member := createUser(t, repo, clubID, domain.RoleMember)
	now := time.Now().UTC()

	t1 := auth.HashRefreshToken(uniqueRawToken("refresh-one"))
	require.NoError(t, repo.CreateRefreshToken(context.Background(), domain.RefreshToken{
		UserID: member.ID, ClubID: clubID, TokenHash: t1, ExpiresAt: now.Add(time.Hour),
	}))

	var familyID string
	require.NoError(t, testPool.QueryRow(context.Background(),
		"SELECT family_id FROM refresh_tokens WHERE token_hash = $1", t1).Scan(&familyID))

	t2 := auth.HashRefreshToken(uniqueRawToken("refresh-two"))
	principal, err := repo.RotateRefreshToken(context.Background(), t1, now, t2, now.Add(time.Hour))
	require.NoError(t, err, "first rotation")
	wantPrincipal := domain.Principal{UserID: member.ID, ClubID: clubID, Role: domain.RoleMember}
	assert.Equal(t, wantPrincipal, principal)

	// T2 works: it can rotate to T3.
	t3 := auth.HashRefreshToken(uniqueRawToken("refresh-three"))
	_, err = repo.RotateRefreshToken(context.Background(), t2, now, t3, now.Add(time.Hour))
	require.NoError(t, err, "second rotation")

	// Replaying T1 is reuse: the whole family must be revoked.
	t4 := auth.HashRefreshToken(uniqueRawToken("refresh-four"))
	_, err = repo.RotateRefreshToken(context.Background(), t1, now, t4, now.Add(time.Hour))
	var reuse *domain.RefreshTokenReuseError
	require.ErrorAs(t, err, &reuse)
	require.ErrorIs(t, err, domain.ErrUnauthorized)
	assert.Equal(t, familyID, reuse.FamilyID)
	assert.Equal(t, wantPrincipal, reuse.Principal)

	var active int
	require.NoError(t, testPool.QueryRow(context.Background(),
		"SELECT count(*) FROM refresh_tokens WHERE family_id = $1 AND revoked_at IS NULL", familyID).Scan(&active))
	assert.Zero(t, active)

	// T3 was active before the replay and is now revoked too.
	_, err = repo.RotateRefreshToken(context.Background(), t3, now, t4, now.Add(time.Hour))
	require.ErrorAs(t, err, &reuse)
}

func TestLogoutRevokesRefreshFamily(t *testing.T) {
	repo := testDB(t)
	clubID := createClub(t, "Logout Club")
	member := createUser(t, repo, clubID, domain.RoleMember)
	otherMember := createUser(t, repo, clubID, domain.RoleMember)
	now := time.Now().UTC()
	actor := domain.Principal{UserID: member.ID, ClubID: clubID, Role: domain.RoleMember}

	t1 := auth.HashRefreshToken(uniqueRawToken("logout-one"))
	require.NoError(t, repo.CreateRefreshToken(context.Background(), domain.RefreshToken{
		UserID: member.ID, ClubID: clubID, TokenHash: t1, ExpiresAt: now.Add(time.Hour),
	}))
	t2 := auth.HashRefreshToken(uniqueRawToken("logout-two"))
	_, err := repo.RotateRefreshToken(context.Background(), t1, now, t2, now.Add(time.Hour))
	require.NoError(t, err, "rotate")

	// Wrong user cannot revoke the family.
	otherActor := domain.Principal{UserID: otherMember.ID, ClubID: clubID, Role: domain.RoleMember}
	require.NoError(t, repo.RevokeRefreshFamily(context.Background(), t2, otherActor, now))
	t3 := auth.HashRefreshToken(uniqueRawToken("logout-three"))
	_, err = repo.RotateRefreshToken(context.Background(), t2, now, t3, now.Add(time.Hour))
	require.NoError(t, err, "family was revoked by wrong user")

	require.NoError(t, repo.RevokeRefreshFamily(context.Background(), t3, actor, now))
	var reuse *domain.RefreshTokenReuseError
	_, err = repo.RotateRefreshToken(context.Background(), t3, now,
		auth.HashRefreshToken(uniqueRawToken("logout-four")), now.Add(time.Hour))
	require.ErrorAs(t, err, &reuse)

	require.NoError(t, repo.RevokeRefreshFamily(context.Background(),
		auth.HashRefreshToken("unknown"), actor, now))
}

func TestIdempotentBookingReplayAndConflict(t *testing.T) {
	repo := testDB(t)
	clubID := createClub(t, "Idempotency Club")
	trainer := createUser(t, repo, clubID, domain.RoleTrainer)
	member := createUser(t, repo, clubID, domain.RoleMember)
	classOne := createClass(t, repo, clubID, trainer.ID, 5)
	classTwo := createClass(t, repo, clubID, trainer.ID, 5)
	now := time.Now().UTC()
	key := uniqueRawToken("idem-key")

	first, err := repo.CreateBooking(context.Background(), clubID, classOne.ID, member.ID, key, now)
	require.NoError(t, err, "first booking")
	second, err := repo.CreateBooking(context.Background(), clubID, classOne.ID, member.ID, key, now)
	require.NoError(t, err, "replay")
	assert.Equal(t, first.ID, second.ID)
	assert.True(t, first.CreatedAt.Equal(second.CreatedAt))
	assert.Equal(t, 1, bookedCount(t, classOne.ID, clubID))

	var bookings int
	require.NoError(t, testPool.QueryRow(context.Background(),
		"SELECT count(*) FROM bookings WHERE class_id = $1 AND user_id = $2", classOne.ID, member.ID).Scan(&bookings))
	assert.Equal(t, 1, bookings)

	_, err = repo.CreateBooking(context.Background(), clubID, classTwo.ID, member.ID, key, now)
	require.ErrorIs(t, err, domain.ErrIdempotencyConflict)
	assert.Equal(t, 0, bookedCount(t, classTwo.ID, clubID))
}

func TestCreateBookingConcurrentIdempotency(t *testing.T) {
	repo := testDB(t)
	clubID := createClub(t, "Concurrent Idempotency Club")
	trainer := createUser(t, repo, clubID, domain.RoleTrainer)
	member := createUser(t, repo, clubID, domain.RoleMember)
	class := createClass(t, repo, clubID, trainer.ID, 1)
	now := time.Now().UTC()
	key := uniqueRawToken("concurrent-idem")

	const workers = 4
	type result struct {
		booking domain.Booking
		err     error
	}

	start := make(chan struct{})
	results := make(chan result, workers)
	var waitGroup sync.WaitGroup

	for i := 0; i < workers; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			booking, err := repo.CreateBooking(context.Background(), clubID, class.ID, member.ID, key, now)
			results <- result{booking: booking, err: err}
		}()
	}

	close(start)
	waitGroup.Wait()
	close(results)

	var firstID string
	completed := 0
	for res := range results {
		require.NoError(t, res.err)
		require.NotEmpty(t, res.booking.ID)
		if firstID == "" {
			firstID = res.booking.ID
		} else {
			assert.Equal(t, firstID, res.booking.ID)
		}
		completed++
	}
	assert.Equal(t, workers, completed)
	assert.Equal(t, 1, bookedCount(t, class.ID, clubID))

	var rows int
	require.NoError(t, testPool.QueryRow(context.Background(),
		"SELECT count(*) FROM bookings WHERE class_id = $1 AND user_id = $2", class.ID, member.ID).Scan(&rows))
	assert.Equal(t, 1, rows)
}
