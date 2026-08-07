package domain

import (
	"context"
	"time"
)

// LoginEvent is one recorded authentication attempt (login history). A failed
// attempt against an unknown identifier carries a nil UserID so history is
// complete without revealing account existence. Success/failure is an
// operational necessity, not an enumeration risk: it is only ever readable by
// the account owner or security tooling (SECURITY_SPEC.md MON-5).
type LoginEvent struct {
	ID         int64
	UserID     *int64
	Identifier string
	Method     LoginMethod
	Success    bool
	NewDevice  bool
	DeviceID   string
	IPAddress  *string
	UserAgent  *string
	CreatedAt  time.Time
}

// LoginHistoryRepository owns persistence for login_history. Writes are
// best-effort: a history outage must never break authentication (the same
// guarantee AUD-7 gives the audit log).
type LoginHistoryRepository interface {
	// Record appends a login event.
	Record(ctx context.Context, e LoginEvent) error
	// ListByUser returns the user's login history, newest first, up to limit.
	ListByUser(ctx context.Context, userID int64, limit int) ([]LoginEvent, error)
}
