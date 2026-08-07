//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	platformpg "github.com/AkaneSakuramori/socialmedia/server/internal/platform/postgres"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests exercise the real PostgreSQL session repository (DATABASE.md
// §4.4) against a live database (the dev compose stack). Run with:
//
//	APP_PG_DSN=$(grep APP_DB_PASSWORD ../infra/docker/.env | ...) go test -tags integration ./internal/auth/infra/postgres/
//	# or simply: make test-integration
//
// APP_PG_DSN is required: the credential is injected at run time and never
// committed (DEVOPS.md §7). They skip when the database is unreachable.

var integSeq int64

// TestMain wipes disposable integration rows from a previously killed run so
// reruns can never collide on the deterministic seed ids. It runs only when
// APP_PG_DSN is set; otherwise the package skips.
func TestMain(m *testing.M) {
	dsn := os.Getenv("APP_PG_DSN")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "APP_PG_DSN not set; skipping auth integration tests")
		os.Exit(0)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if p, err := platformpg.Open(ctx, dsn, 1); err == nil {
		_, _ = p.Exec(ctx, `DELETE FROM user_sessions WHERE id >= 70000`)
		_, _ = p.Exec(ctx, `DELETE FROM user_sessions WHERE user_id >= 9000000`)
		_, _ = p.Exec(ctx, `DELETE FROM auth_tokens WHERE user_id >= 9000000`)
		_, _ = p.Exec(ctx, `DELETE FROM login_history WHERE user_id >= 9000000 OR user_id IS NULL`)
		_, _ = p.Exec(ctx, `DELETE FROM audit_logs WHERE actor_user_id >= 9000000`)
		_, _ = p.Exec(ctx, `DELETE FROM users WHERE id >= 9000000`)
		p.Close()
	}
	os.Exit(m.Run())
}

func integPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("APP_PG_DSN")
	if dsn == "" {
		t.Skip("APP_PG_DSN not set; skipping auth integration tests")
	}
	p, err := platformpg.Open(context.Background(), dsn, 4)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(p.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := platformpg.Ping(ctx, p); err != nil {
		t.Skipf("postgres not reachable, skipping: %v", err)
	}
	return p
}

// seedUser inserts a disposable account and returns its id.
func seedUser(t *testing.T, p *pgxpool.Pool) int64 {
	t.Helper()
	integSeq++
	id := int64(9000000 + integSeq)
	ctx := context.Background()
	// Remove leftovers from a previously killed run (ids are deterministic).
	_, _ = p.Exec(ctx, `DELETE FROM user_sessions WHERE user_id = $1`, id)
	_, _ = p.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	if _, err := p.Exec(ctx,
		`INSERT INTO users (id, display_name) VALUES ($1, 'integration')`, id); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = p.Exec(context.Background(), `DELETE FROM user_sessions WHERE user_id = $1`, id)
		_, _ = p.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id)
	})
	return id
}

func integSession(id, userID int64, deviceID, hash string, now time.Time) *domain.Session {
	return &domain.Session{
		ID:               id,
		UserID:           userID,
		Device:           domain.DeviceInfo{DeviceID: deviceID},
		RefreshTokenHash: hash,
		RefreshExpiresAt: now.Add(30 * 24 * time.Hour),
		LastActiveAt:     now,
		State:            domain.SessionActive,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
}

func commit(t *testing.T, dbtx tx.Tx) {
	t.Helper()
	if err := dbtx.Commit(context.Background()); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// beginTx opens a transaction and registers a rollback cleanup so a tx is
// never left open (pgxpool.Close blocks until every conn is released).
func beginTx(t *testing.T, begin tx.Beginner) tx.Tx {
	t.Helper()
	dbtx, err := begin.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbtx.Rollback(context.Background()) })
	return dbtx
}

// run executes fn inside a transaction that is rolled back on return, so a
// one-shot read/write releases its connection immediately (the pool is small).
func run(t *testing.T, begin tx.Beginner, fn func(dbtx tx.Tx) error) {
	t.Helper()
	dbtx, err := begin.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer dbtx.Rollback(context.Background()) // no-op after a successful Commit
	if err := fn(dbtx); err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestIntegSessionCreateAndFindByDevice(t *testing.T) {
	p := integPool(t)
	repo := NewSessionRepo(p)
	begin := platformpg.NewBeginner(p)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	userID := seedUser(t, p)
	s := integSession(71001, userID, "d-1", "current-hash-1", now)

	dbtx := beginTx(t, begin)
	if err := repo.Create(ctx, dbtx, s); err != nil {
		t.Fatalf("Create: %v", err)
	}
	commit(t, dbtx)

	found, err := repo.FindByDeviceID(ctx, userID, "d-1")
	if err != nil {
		t.Fatalf("FindByDeviceID: %v", err)
	}
	if found.ID != s.ID || found.RefreshTokenHash != s.RefreshTokenHash {
		t.Errorf("roundtrip mismatch: %+v", found)
	}
	if found.Device.DeviceID != "d-1" || found.State != domain.SessionActive {
		t.Errorf("session = %+v", found)
	}
	if !found.RefreshExpiresAt.Equal(s.RefreshExpiresAt) {
		t.Errorf("expiry = %v, want %v", found.RefreshExpiresAt, s.RefreshExpiresAt)
	}
}

func TestIntegRotateIsAtomicCAS(t *testing.T) {
	p := integPool(t)
	repo := NewSessionRepo(p)
	begin := platformpg.NewBeginner(p)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	userID := seedUser(t, p)
	s := integSession(71002, userID, "d-2", "hash-A", now)

	dbtx := beginTx(t, begin)
	if err := repo.Create(ctx, dbtx, s); err != nil {
		t.Fatal(err)
	}
	commit(t, dbtx)

	// Rotate A -> B.
	dbtx = beginTx(t, begin)
	cur, err := repo.FindByHash(ctx, dbtx, "hash-A")
	if err != nil {
		t.Fatalf("FindByHash: %v", err)
	}
	next := integSession(cur.ID, cur.UserID, cur.Device.DeviceID, "hash-B", now)
	next.RefreshTokenPreviousHash = cur.RefreshTokenHash
	next.RefreshTokenFamily = cur.RefreshTokenFamily + 1
	next.LastActiveAt = now
	if err := repo.Rotate(ctx, dbtx, next, "hash-A"); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	commit(t, dbtx)

	check := func(tx tx.Tx) {
		t.Helper()
		if got, err := repo.FindByHash(ctx, tx, "hash-B"); err != nil {
			t.Errorf("current hash should now be B: %v", err)
		} else if got.RefreshTokenFamily != 1 || got.RefreshTokenPreviousHash != "hash-A" {
			t.Errorf("rotated session = %+v", got)
		}
		if _, err := repo.FindByPreviousHash(ctx, tx, "hash-A"); err != nil {
			t.Errorf("FindByPreviousHash(A): %v", err)
		}
		if _, err := repo.FindByHash(ctx, tx, "hash-A"); !errors.Is(err, domain.ErrSessionNotFound) {
			t.Errorf("FindByHash(A) = %v, want ErrSessionNotFound", err)
		}
	}
	dbtx = beginTx(t, begin)
	check(dbtx)
	if err := dbtx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	// CAS: rotating from a stale hash must fail.
	dbtx = beginTx(t, begin)
	stale := integSession(cur.ID, cur.UserID, cur.Device.DeviceID, "hash-C", now)
	err = repo.Rotate(ctx, dbtx, stale, "hash-A")
	if !errors.Is(err, domain.ErrSessionNotFound) {
		t.Fatalf("stale Rotate = %v, want ErrSessionNotFound", err)
	}
}

func TestIntegRevokeAllByUserID(t *testing.T) {
	p := integPool(t)
	repo := NewSessionRepo(p)
	begin := platformpg.NewBeginner(p)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	userID := seedUser(t, p)

	for i, device := range []string{"d-a", "d-b"} {
		s := integSession(71010+int64(i), userID, device, fmt.Sprintf("hash-%d", i), now)
		dbtx, err := begin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.Create(ctx, dbtx, s); err != nil {
			t.Fatal(err)
		}
		commit(t, dbtx)
	}

	dbtx := beginTx(t, begin)
	if err := repo.RevokeAllByUserID(ctx, dbtx, userID); err != nil {
		t.Fatalf("RevokeAllByUserID: %v", err)
	}
	commit(t, dbtx)

	for _, device := range []string{"d-a", "d-b"} {
		s, err := repo.FindByDeviceID(ctx, userID, device)
		if err != nil {
			t.Fatalf("FindByDeviceID(%s): %v", device, err)
		}
		if s.State != domain.SessionRevoked {
			t.Errorf("session %s state = %s, want revoked", device, s.State)
		}
	}
}

func TestIntegConcurrentRotationSingleWinner(t *testing.T) {
	p := integPool(t)
	repo := NewSessionRepo(p)
	begin := platformpg.NewBeginner(p)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	userID := seedUser(t, p)
	s := integSession(71020, userID, "d-race", "hash-race", now)

	dbtx := beginTx(t, begin)
	if err := repo.Create(ctx, dbtx, s); err != nil {
		t.Fatal(err)
	}
	commit(t, dbtx)

	const n = 6
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			tx, err := begin.Begin(ctx)
			if err != nil {
				results <- err
				return
			}
			defer tx.Rollback(ctx) // no-op after commit
			cur, err := repo.FindByHash(ctx, tx, "hash-race")
			if err != nil {
				results <- err // FOR UPDATE re-read: this goroutine lost
				return
			}
			next := integSession(cur.ID, cur.UserID, cur.Device.DeviceID,
				fmt.Sprintf("hash-winner-%d", i), now)
			if err := repo.Rotate(ctx, tx, next, "hash-race"); err != nil {
				results <- err // CAS: lost the race
				return
			}
			results <- tx.Commit(ctx)
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	wins, losses := 0, 0
	for err := range results {
		if err == nil {
			wins++
		} else if errors.Is(err, domain.ErrSessionNotFound) {
			losses++
		} else {
			t.Errorf("unexpected goroutine error: %v", err)
		}
	}
	if wins != 1 || losses != n-1 {
		t.Errorf("wins=%d losses=%d, want exactly one winner (%d losers)", wins, losses, n-1)
	}
}

func TestIntegListByUserAndRename(t *testing.T) {
	p := integPool(t)
	repo := NewSessionRepo(p)
	begin := platformpg.NewBeginner(p)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	userID := seedUser(t, p)

	// Two active sessions (one older), one revoked.
	for _, c := range []struct {
		id     int64
		dev    string
		active time.Time
	}{
		{72001, "d-new", now},
		{72002, "d-old", now.Add(-2 * time.Hour)},
		{72003, "d-revoked", now},
	} {
		dbtx := beginTx(t, begin)
		s := integSession(c.id, userID, c.dev, "hash-"+c.dev, c.active)
		if err := repo.Create(ctx, dbtx, s); err != nil {
			t.Fatalf("Create(%s): %v", c.dev, err)
		}
		commit(t, dbtx)
	}
	dbtx := beginTx(t, begin)
	if err := repo.RevokeByID(ctx, dbtx, userID, 72003); err != nil {
		t.Fatalf("revoke seed: %v", err)
	}
	commit(t, dbtx)

	list, err := repo.ListByUser(ctx, userID)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("listed %d sessions, want 2 (revoked excluded)", len(list))
	}
	if list[0].ID != 72001 || list[1].ID != 72002 {
		t.Errorf("order = [%d %d], want newest-first [72001 72002]", list[0].ID, list[1].ID)
	}

	// Rename an active session.
	renameTx := beginTx(t, begin)
	if err := repo.Rename(ctx, renameTx, userID, 72001, "Work Laptop"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	commit(t, renameTx)
	var got *domain.Session
	run(t, begin, func(dbtx tx.Tx) error {
		var err error
		got, err = repo.FindByID(ctx, dbtx, 72001)
		return err
	})
	if got.Device.DeviceName == nil || *got.Device.DeviceName != "Work Laptop" {
		t.Errorf("device_name = %v, want Work Laptop", got.Device.DeviceName)
	}

	// Rename of a revoked or foreign session fails (no update).
	run(t, begin, func(dbtx tx.Tx) error {
		if err := repo.Rename(ctx, dbtx, userID, 72003, "x"); !errors.Is(err, domain.ErrSessionNotFound) {
			return fmt.Errorf("Rename revoked = %v, want ErrSessionNotFound", err)
		}
		return nil
	})
}

func TestIntegRevokeByIDAndOwnership(t *testing.T) {
	p := integPool(t)
	repo := NewSessionRepo(p)
	begin := platformpg.NewBeginner(p)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	owner := seedUser(t, p)
	foreign := seedUser(t, p)

	for _, c := range []struct {
		id     int64
		userID int64
	}{
		{72101, owner},
		{72102, foreign},
	} {
		dbtx := beginTx(t, begin)
		if err := repo.Create(ctx, dbtx, integSession(c.id, c.userID, "d", "h", now)); err != nil {
			t.Fatalf("Create: %v", err)
		}
		commit(t, dbtx)
	}

	// Owner revokes its own session (committed so the next checks observe it).
	dbtx := beginTx(t, begin)
	if err := repo.RevokeByID(ctx, dbtx, owner, 72101); err != nil {
		t.Fatalf("RevokeByID own: %v", err)
	}
	commit(t, dbtx)
	// Revoking the same session again is a no-op result (SESS-3 idempotency).
	run(t, begin, func(dbtx tx.Tx) error {
		if err := repo.RevokeByID(ctx, dbtx, owner, 72101); !errors.Is(err, domain.ErrSessionNotFound) {
			return fmt.Errorf("double revoke = %v, want ErrSessionNotFound", err)
		}
		return nil
	})
	// A caller can never revoke a session it does not own — the row is not
	// touched and no ownership information leaks (SESS-3).
	run(t, begin, func(dbtx tx.Tx) error {
		if err := repo.RevokeByID(ctx, dbtx, owner, 72102); !errors.Is(err, domain.ErrSessionNotFound) {
			return fmt.Errorf("foreign revoke = %v, want ErrSessionNotFound", err)
		}
		return nil
	})
	var s *domain.Session
	run(t, begin, func(dbtx tx.Tx) error {
		var err error
		s, err = repo.FindByID(ctx, dbtx, 72102)
		return err
	})
	if s.State != domain.SessionActive {
		t.Errorf("foreign session state = %s, must stay active", s.State)
	}
}

func TestIntegRevokeOthersKeepsCurrent(t *testing.T) {
	p := integPool(t)
	repo := NewSessionRepo(p)
	begin := platformpg.NewBeginner(p)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	userID := seedUser(t, p)

	for i, id := range []int64{72201, 72202, 72203} {
		dbtx := beginTx(t, begin)
		if err := repo.Create(ctx, dbtx, integSession(id, userID, fmt.Sprintf("d-%d", i), "h", now)); err != nil {
			t.Fatalf("Create: %v", err)
		}
		commit(t, dbtx)
	}

	dbtx := beginTx(t, begin)
	if err := repo.RevokeOthersByUserID(ctx, dbtx, userID, 72202); err != nil {
		t.Fatalf("RevokeOthersByUserID: %v", err)
	}
	commit(t, dbtx)

	for _, id := range []int64{72201, 72203} {
		var s *domain.Session
		run(t, begin, func(dbtx tx.Tx) error {
			var err error
			s, err = repo.FindByID(ctx, dbtx, id)
			return err
		})
		if s.State != domain.SessionRevoked {
			t.Errorf("session %d state = %s, want revoked", id, s.State)
		}
	}
	var keep *domain.Session
	run(t, begin, func(dbtx tx.Tx) error {
		var err error
		keep, err = repo.FindByID(ctx, dbtx, 72202)
		return err
	})
	if keep.State != domain.SessionActive {
		t.Errorf("kept session state = %s, want active", keep.State)
	}
}

func TestIntegExpireIdleAndPurge(t *testing.T) {
	p := integPool(t)
	repo := NewSessionRepo(p)
	begin := platformpg.NewBeginner(p)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	userID := seedUser(t, p)

	// Two active sessions; force one idle well beyond the sweep window.
	for _, c := range []struct {
		id   int64
		dev  string
		idle time.Time
	}{
		{72301, "d-idle", now.Add(-40 * 24 * time.Hour)},
		{72302, "d-fresh", now},
	} {
		dbtx := beginTx(t, begin)
		s := integSession(c.id, userID, c.dev, "h", c.idle)
		s.RefreshExpiresAt = now.Add(30 * 24 * time.Hour) // only the idle rule applies
		if err := repo.Create(ctx, dbtx, s); err != nil {
			t.Fatalf("Create: %v", err)
		}
		commit(t, dbtx)
	}

	n, err := repo.ExpireIdle(ctx, now, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("ExpireIdle: %v", err)
	}
	if n != 1 {
		t.Fatalf("expired %d sessions, want 1 (SESS-9)", n)
	}
	// Lock-free state reads: FindByID takes FOR UPDATE, which would deadlock
	// with the Purge below.
	stateOf := func(id int64) domain.SessionState {
		t.Helper()
		var s domain.SessionState
		if err := p.QueryRow(ctx, `SELECT state FROM user_sessions WHERE id = $1`, id).Scan(&s); err != nil {
			t.Fatalf("state(%d): %v", id, err)
		}
		return s
	}
	if s := stateOf(72301); s != domain.SessionExpired {
		t.Errorf("idle session state = %s, want expired", s)
	}
	if s := stateOf(72302); s != domain.SessionActive {
		t.Errorf("fresh session state = %s, want active", s)
	}

	// Purge only removes revoked/expired rows last changed before the cutoff
	// (DATABASE.md §4.4 retention). A cutoff before the rows' updated_at keeps
	// everything; one after ExpireIdle's DB timestamp removes the expired row.
	if n, err := repo.Purge(ctx, now.Add(-90*24*time.Hour)); err != nil {
		t.Fatalf("Purge: %v", err)
	} else if n != 0 {
		t.Errorf("purged %d with a fresh cutoff, want 0", n)
	}
	fullCutoff := time.Now().UTC().Add(24 * time.Hour)
	if n, err := repo.Purge(ctx, fullCutoff); err != nil {
		t.Fatalf("Purge: %v", err)
	} else if n != 1 {
		t.Errorf("purged %d with a full cutoff, want 1 (DATABASE.md §4.4)", n)
	}
	if _, err := repo.FindByID(ctx, beginTx(t, begin), 72301); !errors.Is(err, domain.ErrSessionNotFound) {
		t.Errorf("purged session still present: %v", err)
	}
}

// TestIntegTokenVersionBumpAtomicity proves the SESS-6 pattern behind
// LogoutAll against the live schema: the bump is transactional — a rolled-back
// bump does not survive, a committed one does.
func TestIntegTokenVersionBumpAtomicity(t *testing.T) {
	p := integPool(t)
	ctx := context.Background()
	userID := seedUser(t, p)

	scan := func() int64 {
		t.Helper()
		var v int64
		if err := p.QueryRow(ctx, `SELECT token_version FROM users WHERE id = $1`, userID).Scan(&v); err != nil {
			t.Fatalf("read token_version: %v", err)
		}
		return v
	}
	if v := scan(); v != 0 {
		t.Fatalf("initial token_version = %d, want 0", v)
	}

	// Rolled back bump leaves the version untouched.
	conn, err := p.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	dbtx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := dbtx.Exec(ctx, `UPDATE users SET token_version = token_version + 1 WHERE id = $1`, userID); err != nil {
		t.Fatalf("bump: %v", err)
	}
	if err := dbtx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	conn.Release()
	if v := scan(); v != 0 {
		t.Errorf("after rollback token_version = %d, want 0", v)
	}

	// Committed bump is durable.
	conn, err = p.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	dbtx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	var newVersion int64
	if err := dbtx.QueryRow(ctx, `UPDATE users SET token_version = token_version + 1 WHERE id = $1 RETURNING token_version`, userID).Scan(&newVersion); err != nil {
		t.Fatalf("bump returning: %v", err)
	}
	if err := dbtx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	conn.Release()
	if newVersion != 1 || scan() != 1 {
		t.Errorf("committed bump = %d/%d, want 1/1", newVersion, scan())
	}
}
