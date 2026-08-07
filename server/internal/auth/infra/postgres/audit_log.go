package postgres

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AuditLog is a persistent domain.AuditLogger writing to audit_logs
// (DATABASE.md §8.5, SECURITY_SPEC.md AUD-1/AUD-6). It is best-effort by
// design (AUTH-7): an audit outage must never break the business operation, so
// failures are logged and swallowed rather than propagated.
type AuditLog struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

// NewAuditLog builds the persistent audit logger over the shared pool.
func NewAuditLog(pool *pgxpool.Pool, logger *slog.Logger) *AuditLog {
	if logger == nil {
		logger = slog.Default()
	}
	return &AuditLog{pool: pool, log: logger}
}

// Log appends one audit event. Details are serialized to JSONB; failures are
// logged and not propagated (best-effort semantics).
func (a *AuditLog) Log(ctx context.Context, e domain.AuditEvent) error {
	details, err := json.Marshal(e.Details)
	if err != nil {
		a.log.Error("audit: marshal details", "error", err)
		return nil
	}
	resourceType := e.ResourceType
	if resourceType == "" {
		resourceType = "user"
	}
	_, err = a.pool.Exec(ctx, `
		INSERT INTO audit_logs (actor_user_id, action, resource_type, resource_id, ip_address, details)
		VALUES ($1,$2,$3,$4,$5::inet,$6)`,
		e.ActorUserID, e.Action, resourceType, e.ResourceID, nullIfEmpty(e.IPAddress), details)
	if err != nil {
		a.log.Error("audit: write failed (best-effort)", "action", e.Action, "error", err)
	}
	return nil
}

var _ domain.AuditLogger = (*AuditLog)(nil)
