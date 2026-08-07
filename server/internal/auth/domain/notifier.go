package domain

import "context"

// SecurityNotifier surfaces security events to the account holder: password
// change, new-device login, identifier change, account deletion (WhatsApp-style
// account-security alerts, SECURITY_SPEC.md REC-5). Adapters are best-effort and
// asynchronous: a notification outage must never break the operation.
type SecurityNotifier interface {
	// Notify delivers a security event for the user. Implementations must not
	// block the calling use-case; failures are logged and swallowed.
	Notify(ctx context.Context, userID int64, event string, details map[string]string) error
}

// NoopNotifier is the default adapter, used until the delivery/notification
// milestone provides a real sink.
func NoopNotifier() SecurityNotifier { return noopNotifier{} }

type noopNotifier struct{}

func (noopNotifier) Notify(context.Context, int64, string, map[string]string) error { return nil }
