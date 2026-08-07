//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	platformpg "github.com/AkaneSakuramori/socialmedia/server/internal/platform/postgres"
	userdomain "github.com/AkaneSakuramori/socialmedia/server/internal/user/domain"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Integration tests for the milestone-5 account-security adapters: user
// lifecycle (SetEmail/SetPhone/MarkDeleted/Restore/PurgeDeleted),
// auth_tokens (single-use, TTL-bounded, uniform errors), login_history, and
// audit_logs. Run with `go test -tags integration ./internal/auth/infra/postgres/`.
// They reuse seedUser/integPool/beginTx/commit/run from
// session_repo_integration_test.go.

// seedPhoneUser inserts a disposable account carrying the given phone.
func seedPhoneUser(t *testing.T, p *pgxpool.Pool, phone string) int64 {
	t.Helper()
	id := seedUser(t, p)
	if _, err := p.Exec(context.Background(),
		`UPDATE users SET phone_number = $2 WHERE id = $1`, id, phone); err != nil {
		t.Fatalf("seed phone: %v", err)
	}
	return id
}

func TestIntegUserSetPhoneAndSetEmail(t *testing.T) {
	p := integPool(t)
	repo := NewUserRepo(p)
	begin := platformpg.NewBeginner(p)
	ctx := context.Background()
	u1 := seedUser(t, p)
	u2 := seedUser(t, p)

	dbtx := beginTx(t, begin)
	if err := repo.SetPhone(ctx, dbtx, u1, "+15550123"); err != nil {
		t.Fatalf("SetPhone: %v", err)
	}
	if err := repo.SetEmail(ctx, dbtx, u1, "u1@example.com"); err != nil {
		t.Fatalf("SetEmail: %v", err)
	}
	commit(t, dbtx)

	found, err := repo.FindByPhone(ctx, "+15550123")
	if err != nil {
		t.Fatalf("FindByPhone: %v", err)
	}
	if found.ID != u1 || found.PhoneNumber == nil || *found.PhoneNumber != "+15550123" {
		t.Errorf("phone roundtrip = %+v", found)
	}
	em, err := repo.FindByEmail(ctx, "u1@example.com")
	if err != nil {
		t.Fatalf("FindByEmail: %v", err)
	}
	if em.ID != u1 {
		t.Errorf("email roundtrip = %+v", em)
	}

	// Unique index arbitrates: the same phone cannot belong to two accounts.
	run(t, begin, func(dbtx tx.Tx) error {
		err := repo.SetPhone(ctx, dbtx, u2, "+15550123")
		if !errors.Is(err, userdomain.ErrIdentifierTaken) {
			return fmt.Errorf("SetPhone duplicate = %v, want ErrIdentifierTaken", err)
		}
		return nil
	})
}

func TestIntegUserMarkDeletedRestorePurge(t *testing.T) {
	p := integPool(t)
	repo := NewUserRepo(p)
	begin := platformpg.NewBeginner(p)
	ctx := context.Background()
	now := time.Now().UTC()

	// u1 is within grace (recently deleted), u2 is past the grace window.
	u1 := seedPhoneUser(t, p, "+15551111")
	u2 := seedPhoneUser(t, p, "+15552222")

	dbtx := beginTx(t, begin)
	if err := repo.MarkDeleted(ctx, dbtx, u1, now.Add(-time.Minute)); err != nil {
		t.Fatalf("MarkDeleted u1: %v", err)
	}
	commit(t, dbtx)
	dbtx = beginTx(t, begin)
	if err := repo.MarkDeleted(ctx, dbtx, u2, now.Add(-31*24*time.Hour)); err != nil {
		t.Fatalf("MarkDeleted u2: %v", err)
	}
	commit(t, dbtx)

	// Deleted accounts are hidden from normal lookups but findable by recovery.
	if _, err := repo.FindByPhone(ctx, "+15551111"); !errors.Is(err, userdomain.ErrUserNotFound) {
		t.Fatalf("FindByPhone(deleted) = %v, want ErrUserNotFound", err)
	}
	del, err := repo.FindDeletedByPhone(ctx, "+15552222")
	if err != nil || del.ID != u2 {
		t.Fatalf("FindDeletedByPhone = %+v, %v; want u2", del, err)
	}

	// Double delete is an explicit error.
	run(t, begin, func(dbtx tx.Tx) error {
		err := repo.MarkDeleted(ctx, dbtx, u1, now)
		if !errors.Is(err, userdomain.ErrAccountAlreadyDeleted) {
			return fmt.Errorf("double MarkDeleted = %v, want ErrAccountAlreadyDeleted", err)
		}
		return nil
	})

	// Restore u1 within grace; restoring u2 (past grace) fails.
	dbtx = beginTx(t, begin)
	if err := repo.Restore(ctx, dbtx, u1, now.Add(-7*24*time.Hour)); err != nil {
		t.Fatalf("Restore u1: %v", err)
	}
	commit(t, dbtx)
	run(t, begin, func(dbtx tx.Tx) error {
		err := repo.Restore(ctx, dbtx, u2, now.Add(-7*24*time.Hour))
		if !errors.Is(err, userdomain.ErrAccountRestoreExpired) {
			return fmt.Errorf("Restore u2 = %v, want ErrAccountRestoreExpired", err)
		}
		return nil
	})
	if _, err := repo.FindByPhone(ctx, "+15551111"); err != nil {
		t.Fatalf("restored u1 not visible: %v", err)
	}

	// Purge removes only accounts past the grace cutoff (u2).
	n, err := repo.PurgeDeleted(ctx, now.Add(-7*24*time.Hour))
	if err != nil {
		t.Fatalf("PurgeDeleted: %v", err)
	}
	if n != 1 {
		t.Errorf("purged %d, want 1 (only u2)", n)
	}
	if _, err := repo.FindDeletedByPhone(ctx, "+15552222"); !errors.Is(err, userdomain.ErrUserNotFound) {
		t.Errorf("purged account still findable: %v", err)
	}
}

func TestIntegAuthTokenCreateConsume(t *testing.T) {
	p := integPool(t)
	repo := NewAuthTokenRepo(p)
	begin := platformpg.NewBeginner(p)
	ctx := context.Background()
	now := time.Now().UTC()
	userID := seedUser(t, p)

	dbtx := beginTx(t, begin)
	if err := repo.Create(ctx, dbtx, &domain.AuthToken{
		UserID:    userID,
		Purpose:   domain.PurposePasswordReset,
		TokenHash: "tok-hash-1",
		Data:      []byte(`{"email":"new@example.com"}`),
		ExpiresAt: now.Add(30 * time.Minute),
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	commit(t, dbtx)

	// First consume succeeds and returns the token.
	dbtx = beginTx(t, begin)
	got, err := repo.Consume(ctx, dbtx, "tok-hash-1")
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if got.Purpose != domain.PurposePasswordReset {
		t.Errorf("purpose = %s, want password_reset", got.Purpose)
	}
	var data map[string]string
	if err := json.Unmarshal(got.Data, &data); err != nil || data["email"] != "new@example.com" {
		t.Errorf("data = %s, want {\"email\":\"new@example.com\"}", got.Data)
	}
	commit(t, dbtx)

	// Second consume fails identically to unknown/expired (REC-6: no state
	// enumeration) — and a fresh consume of an unknown hash is identical.
	run(t, begin, func(dbtx tx.Tx) error {
		if _, err := repo.Consume(ctx, dbtx, "tok-hash-1"); !errors.Is(err, domain.ErrRecoveryTokenInvalid) {
			return fmt.Errorf("second Consume = %v, want ErrRecoveryTokenInvalid", err)
		}
		return nil
	})
	run(t, begin, func(dbtx tx.Tx) error {
		if _, err := repo.Consume(ctx, dbtx, "tok-unknown"); !errors.Is(err, domain.ErrRecoveryTokenInvalid) {
			return fmt.Errorf("unknown Consume = %v, want ErrRecoveryTokenInvalid", err)
		}
		return nil
	})
}

func TestIntegAuthTokenExpiredConsumeFails(t *testing.T) {
	p := integPool(t)
	repo := NewAuthTokenRepo(p)
	begin := platformpg.NewBeginner(p)
	ctx := context.Background()
	userID := seedUser(t, p)

	dbtx := beginTx(t, begin)
	if err := repo.Create(ctx, dbtx, &domain.AuthToken{
		UserID:    userID,
		Purpose:   domain.PurposePhoneChange,
		TokenHash: "tok-expired",
		ExpiresAt: time.Now().UTC().Add(-time.Minute),
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	commit(t, dbtx)

	run(t, begin, func(dbtx tx.Tx) error {
		if _, err := repo.Consume(ctx, dbtx, "tok-expired"); !errors.Is(err, domain.ErrRecoveryTokenInvalid) {
			return fmt.Errorf("expired Consume = %v, want ErrRecoveryTokenInvalid", err)
		}
		return nil
	})
}

func TestIntegLoginHistoryRecordAndList(t *testing.T) {
	p := integPool(t)
	repo := NewLoginHistoryRepo(p)
	ctx := context.Background()
	userID := seedUser(t, p)
	ip := "203.0.113.9"
	ua := "integration-test/1.0"

	// Two events for the user, one for an unknown identifier (nil user).
	if err := repo.Record(ctx, domain.LoginEvent{
		UserID:     &userID,
		Identifier: "+15550123",
		Method:     domain.LoginMethodPassword,
		Success:    true,
		NewDevice:  true,
		DeviceID:   "d-1",
		IPAddress:  &ip,
		UserAgent:  &ua,
		CreatedAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Record success: %v", err)
	}
	if err := repo.Record(ctx, domain.LoginEvent{
		UserID:     &userID,
		Identifier: "+15550123",
		Method:     domain.LoginMethodOTP,
		Success:    false,
		CreatedAt:  time.Now().UTC().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("Record failure: %v", err)
	}
	if err := repo.Record(ctx, domain.LoginEvent{
		UserID:     nil,
		Identifier: "ghost@example.com",
		Method:     domain.LoginMethodPassword,
		Success:    false,
		CreatedAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Record unknown: %v", err)
	}

	events, err := repo.ListByUser(ctx, userID, 10)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("listed %d events, want 2 (unknown excluded)", len(events))
	}
	if !events[0].Success || events[1].Success {
		t.Errorf("order = %+v, want newest-first success then failure", events)
	}
	if events[0].DeviceID != "d-1" || events[0].IPAddress == nil || *events[0].IPAddress != ip {
		t.Errorf("event 0 = %+v, want device d-1 and ip", events[0])
	}
	if events[1].Method != domain.LoginMethodOTP {
		t.Errorf("event 1 method = %s, want otp", events[1].Method)
	}

	one, err := repo.ListByUser(ctx, userID, 1)
	if err != nil || len(one) != 1 {
		t.Errorf("limit 1 = %d events, %v; want 1", len(one), err)
	}
}

func TestIntegAuditLogPersists(t *testing.T) {
	p := integPool(t)
	log := NewAuditLog(p, discardLogger())
	ctx := context.Background()
	userID := seedUser(t, p)
	ip := "198.51.100.7"

	if err := log.Log(ctx, domain.AuditEvent{
		ActorUserID:  &userID,
		Action:       "auth.password_reset_requested",
		ResourceType: "user",
		ResourceID:   &userID,
		IPAddress:    &ip,
		Details:      map[string]string{"identifier": "+15550123"},
	}); err != nil {
		t.Fatalf("Log: %v", err)
	}

	var action, rtype string
	var rid, actor *int64
	var det map[string]string
	if err := p.QueryRow(ctx,
		`SELECT action, resource_type, resource_id, actor_user_id, details
		 FROM audit_logs WHERE action = 'auth.password_reset_requested' LIMIT 1`,
	).Scan(&action, &rtype, &rid, &actor, &det); err != nil {
		t.Fatalf("read audit row: %v", err)
	}
	if action != "auth.password_reset_requested" || rtype != "user" {
		t.Errorf("row = %s/%s", action, rtype)
	}
	if rid == nil || *rid != userID {
		t.Errorf("resource_id = %v, want %d", rid, userID)
	}
	if det["identifier"] != "+15550123" {
		t.Errorf("details = %v, want identifier +15550123", det)
	}
}

// discardLogger drops all log output (audit writes are best-effort).
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
