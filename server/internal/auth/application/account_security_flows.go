package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	userdomain "github.com/AkaneSakuramori/socialmedia/server/internal/user/domain"
)

// RequestPasswordReset starts the forgot-password flow (API.md §4.2
// purpose=password_reset). The response is uniform whether or not the account
// exists — no user enumeration (OWASP A07:2021). The plaintext token is
// returned to the delivery layer for out-of-band delivery; it is never echoed
// in an API response.
func (s *service) RequestPasswordReset(ctx context.Context, cmd RequestPasswordResetCommand) (*RequestPasswordResetResult, error) {
	ident, err := domain.NewIdentifier(cmd.IdentifierType, cmd.Identifier)
	if err != nil {
		return nil, err
	}
	now := s.now()

	user, err := s.findUser(ctx, ident)
	if err != nil && !errors.Is(err, userdomain.ErrUserNotFound) {
		return nil, fmt.Errorf("auth: find user: %w", err)
	}

	// Uniform response for unknown/deleted/suspended accounts — no enumeration.
	if errors.Is(err, userdomain.ErrUserNotFound) || user.AccountState == userdomain.AccountDeleted {
		_, _ = domain.GenerateOpaqueToken() // equalize timing with the real path
		s.audit(ctx, nil, "auth.password_reset_requested", nil, cmd.IPAddress,
			map[string]string{"identifier": ident.Value})
		return &RequestPasswordResetResult{ExpiresIn: int64(s.deps.PasswordResetTokenTTL.Seconds())}, nil
	}
	if user.AccountState == userdomain.AccountSuspended {
		s.audit(ctx, &user.ID, "auth.password_reset_requested", &user.ID, cmd.IPAddress,
			map[string]string{"blocked": "suspended"})
		return &RequestPasswordResetResult{ExpiresIn: int64(s.deps.PasswordResetTokenTTL.Seconds())}, nil
	}

	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("auth: begin tx: %w", err)
	}
	defer dbtx.Rollback(ctx)

	raw, _, err := s.issueAuthToken(ctx, dbtx, user.ID, domain.PurposePasswordReset, nil, s.deps.PasswordResetTokenTTL, now)
	if err != nil {
		return nil, err
	}
	if err := dbtx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("auth: commit reset request: %w", err)
	}
	s.audit(ctx, &user.ID, "auth.password_reset_requested", &user.ID, cmd.IPAddress, nil)
	s.notify(ctx, user.ID, "password_reset", nil)
	return &RequestPasswordResetResult{Token: raw, ExpiresIn: int64(s.deps.PasswordResetTokenTTL.Seconds())}, nil
}

// ResetPassword completes the forgot-password flow: consume the single-use
// token, set the new password, suspend all sessions and bump the token version
// (PASS-4, SESS-4, REC-4: recovery revokes existing sessions/devices).
func (s *service) ResetPassword(ctx context.Context, cmd ResetPasswordCommand) error {
	if !domain.IsOpaqueTokenShape(cmd.Token) {
		s.audit(ctx, nil, "auth.password_reset_failed", nil, cmd.IPAddress,
			map[string]string{"reason": "malformed"})
		return domain.ErrRecoveryTokenInvalid
	}
	if err := domain.ValidatePassword(cmd.NewPassword, ""); err != nil {
		return err
	}
	hash := domain.HashOpaqueToken(cmd.Token)

	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("auth: begin tx: %w", err)
	}
	defer dbtx.Rollback(ctx)

	tok, err := s.deps.AuthTokens.Consume(ctx, dbtx, hash)
	if err != nil {
		if errors.Is(err, domain.ErrRecoveryTokenInvalid) {
			s.audit(ctx, nil, "auth.password_reset_failed", nil, cmd.IPAddress,
				map[string]string{"reason": "token_invalid"})
			return domain.ErrRecoveryTokenInvalid
		}
		return fmt.Errorf("auth: consume reset token: %w", err)
	}
	if tok.Purpose != domain.PurposePasswordReset {
		s.audit(ctx, nil, "auth.password_reset_failed", nil, cmd.IPAddress,
			map[string]string{"reason": "wrong_purpose"})
		return domain.ErrRecoveryTokenInvalid
	}

	user, err := s.deps.Users.FindByID(ctx, tok.UserID)
	if err != nil {
		if errors.Is(err, userdomain.ErrUserNotFound) {
			return domain.ErrRecoveryTokenInvalid
		}
		return fmt.Errorf("auth: find user: %w", err)
	}
	switch user.AccountState {
	case userdomain.AccountDeleted:
		s.audit(ctx, nil, "auth.password_reset_failed", nil, cmd.IPAddress,
			map[string]string{"reason": "account_deleted"})
		return domain.ErrRecoveryTokenInvalid
	case userdomain.AccountSuspended:
		return domain.ErrAccountSuspended
	}

	if err := s.setPassword(ctx, dbtx, user.ID, cmd.NewPassword); err != nil {
		return err
	}
	if _, err := s.deps.Users.BumpTokenVersion(ctx, dbtx, user.ID); err != nil {
		return fmt.Errorf("auth: bump token version: %w", err)
	}
	if err := s.deps.Sessions.SuspendAllByUserID(ctx, dbtx, user.ID); err != nil {
		return fmt.Errorf("auth: suspend sessions: %w", err)
	}
	if err := dbtx.Commit(ctx); err != nil {
		return fmt.Errorf("auth: commit reset: %w", err)
	}
	s.audit(ctx, &user.ID, "auth.password_reset", &user.ID, cmd.IPAddress, nil)
	s.notify(ctx, user.ID, "password_reset_completed", nil)
	return nil
}

// ChangePassword is the authenticated password-change flow (AUTH-9 step-up).
// The current session survives; all other sessions are suspended (PASS-4,
// ARCHITECTURE.md §11.2) and the global token version bumped (SESS-6).
func (s *service) ChangePassword(ctx context.Context, cmd ChangePasswordCommand) error {
	if err := domain.ValidatePassword(cmd.NewPassword, ""); err != nil {
		return err
	}
	user, err := s.deps.Users.FindByID(ctx, cmd.UserID)
	if err != nil {
		return fmt.Errorf("auth: find user: %w", err)
	}
	if user.AccountState != userdomain.AccountActive {
		return domain.ErrAccountSuspended
	}
	if err := s.reauth(ctx, user, cmd.Reauth); err != nil {
		return err
	}

	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("auth: begin tx: %w", err)
	}
	defer dbtx.Rollback(ctx)

	if err := s.setPassword(ctx, dbtx, user.ID, cmd.NewPassword); err != nil {
		return err
	}
	if _, err := s.deps.Users.BumpTokenVersion(ctx, dbtx, user.ID); err != nil {
		return fmt.Errorf("auth: bump token version: %w", err)
	}
	// PASS-4: suspend all OTHER sessions, keep the current one.
	if err := s.deps.Sessions.SuspendOthersByUserID(ctx, dbtx, user.ID, cmd.SessionID); err != nil {
		return fmt.Errorf("auth: suspend other sessions: %w", err)
	}
	if err := dbtx.Commit(ctx); err != nil {
		return fmt.Errorf("auth: commit password change: %w", err)
	}
	s.audit(ctx, &user.ID, "auth.password_changed", &user.ID, cmd.IPAddress, nil)
	s.notify(ctx, user.ID, "password_changed", nil)
	return nil
}

// RequestEmailChange starts an email change: step-up re-auth, uniqueness check,
// then issue a verification token for the new email (delivered out-of-band).
func (s *service) RequestEmailChange(ctx context.Context, cmd RequestEmailChangeCommand) (*RequestEmailChangeResult, error) {
	ident, err := domain.NewIdentifier(domain.IdentifierEmail, cmd.NewEmail)
	if err != nil {
		return nil, err
	}
	user, err := s.deps.Users.FindByID(ctx, cmd.UserID)
	if err != nil {
		return nil, fmt.Errorf("auth: find user: %w", err)
	}
	if user.AccountState != userdomain.AccountActive {
		return nil, domain.ErrAccountSuspended
	}
	if err := s.reauth(ctx, user, cmd.Reauth); err != nil {
		return nil, err
	}
	if user.Email != nil && *user.Email == ident.Value {
		return nil, &domain.ValidationError{Field: "email", Reason: "unchanged"}
	}
	taken, err := s.deps.Users.EmailTaken(ctx, ident.Value)
	if err != nil {
		return nil, fmt.Errorf("auth: check email taken: %w", err)
	}
	if taken {
		return nil, userdomain.ErrIdentifierTaken
	}
	data, err := json.Marshal(map[string]string{"email": ident.Value})
	if err != nil {
		return nil, fmt.Errorf("auth: marshal change data: %w", err)
	}
	now := s.now()

	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("auth: begin tx: %w", err)
	}
	defer dbtx.Rollback(ctx)

	raw, _, err := s.issueAuthToken(ctx, dbtx, user.ID, domain.PurposeEmailChange, data, s.deps.ChangeVerificationTokenTTL, now)
	if err != nil {
		return nil, err
	}
	if err := dbtx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("auth: commit email change request: %w", err)
	}
	s.audit(ctx, &user.ID, "auth.email_change_requested", &user.ID, cmd.IPAddress,
		map[string]string{"email": ident.Value})
	return &RequestEmailChangeResult{Token: raw, ExpiresIn: int64(s.deps.ChangeVerificationTokenTTL.Seconds())}, nil
}

// ConfirmEmailChange completes an email change by consuming the verification
// token and updating the account's email. The unique index is the final
// race-free arbiter (a concurrent claim surfaces as ErrIdentifierTaken).
func (s *service) ConfirmEmailChange(ctx context.Context, cmd ConfirmEmailChangeCommand) error {
	return s.confirmIdentifierChange(ctx, cmd.Token, cmd.IPAddress, domain.PurposeEmailChange)
}

// RequestPhoneChange starts a phone change (mirror of RequestEmailChange).
func (s *service) RequestPhoneChange(ctx context.Context, cmd RequestPhoneChangeCommand) (*RequestPhoneChangeResult, error) {
	ident, err := domain.NewIdentifier(domain.IdentifierPhone, cmd.NewPhone)
	if err != nil {
		return nil, err
	}
	user, err := s.deps.Users.FindByID(ctx, cmd.UserID)
	if err != nil {
		return nil, fmt.Errorf("auth: find user: %w", err)
	}
	if user.AccountState != userdomain.AccountActive {
		return nil, domain.ErrAccountSuspended
	}
	if err := s.reauth(ctx, user, cmd.Reauth); err != nil {
		return nil, err
	}
	if user.PhoneNumber != nil && *user.PhoneNumber == ident.Value {
		return nil, &domain.ValidationError{Field: "phone", Reason: "unchanged"}
	}
	taken, err := s.deps.Users.PhoneTaken(ctx, ident.Value)
	if err != nil {
		return nil, fmt.Errorf("auth: check phone taken: %w", err)
	}
	if taken {
		return nil, userdomain.ErrIdentifierTaken
	}
	data, err := json.Marshal(map[string]string{"phone": ident.Value})
	if err != nil {
		return nil, fmt.Errorf("auth: marshal change data: %w", err)
	}
	now := s.now()

	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("auth: begin tx: %w", err)
	}
	defer dbtx.Rollback(ctx)

	raw, _, err := s.issueAuthToken(ctx, dbtx, user.ID, domain.PurposePhoneChange, data, s.deps.ChangeVerificationTokenTTL, now)
	if err != nil {
		return nil, err
	}
	if err := dbtx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("auth: commit phone change request: %w", err)
	}
	s.audit(ctx, &user.ID, "auth.phone_change_requested", &user.ID, cmd.IPAddress,
		map[string]string{"phone": ident.Value})
	return &RequestPhoneChangeResult{Token: raw, ExpiresIn: int64(s.deps.ChangeVerificationTokenTTL.Seconds())}, nil
}

// ConfirmPhoneChange completes a phone change by consuming the verification
// token and updating the account's phone number.
func (s *service) ConfirmPhoneChange(ctx context.Context, cmd ConfirmPhoneChangeCommand) error {
	return s.confirmIdentifierChange(ctx, cmd.Token, cmd.IPAddress, domain.PurposePhoneChange)
}

// confirmIdentifierChange is the shared completion path for email/phone change.
// It consumes the single-use verification token and applies the pending
// identifier. The unique index on users is the race-free arbiter; a concurrent
// claim surfaces as ErrIdentifierTaken.
func (s *service) confirmIdentifierChange(ctx context.Context, token string, ip *string, purpose domain.TokenPurpose) error {
	if !domain.IsOpaqueTokenShape(token) {
		s.audit(ctx, nil, "auth.identifier_change_failed", nil, ip,
			map[string]string{"reason": "malformed"})
		return domain.ErrRecoveryTokenInvalid
	}
	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("auth: begin tx: %w", err)
	}
	defer dbtx.Rollback(ctx)

	tok, err := s.deps.AuthTokens.Consume(ctx, dbtx, domain.HashOpaqueToken(token))
	if err != nil {
		if errors.Is(err, domain.ErrRecoveryTokenInvalid) {
			s.audit(ctx, nil, "auth.identifier_change_failed", nil, ip,
				map[string]string{"reason": "token_invalid"})
			return domain.ErrRecoveryTokenInvalid
		}
		return fmt.Errorf("auth: consume change token: %w", err)
	}
	if tok.Purpose != purpose {
		s.audit(ctx, nil, "auth.identifier_change_failed", nil, ip,
			map[string]string{"reason": "wrong_purpose"})
		return domain.ErrRecoveryTokenInvalid
	}

	user, err := s.deps.Users.FindByID(ctx, tok.UserID)
	if err != nil {
		if errors.Is(err, userdomain.ErrUserNotFound) {
			return domain.ErrRecoveryTokenInvalid
		}
		return fmt.Errorf("auth: find user: %w", err)
	}
	if user.AccountState != userdomain.AccountActive {
		return domain.ErrAccountSuspended
	}

	switch purpose {
	case domain.PurposeEmailChange:
		email, err := tok.PendingEmail()
		if err != nil {
			return domain.ErrRecoveryTokenInvalid
		}
		if err := s.deps.Users.SetEmail(ctx, dbtx, user.ID, email); err != nil {
			return err
		}
		s.audit(ctx, &user.ID, "auth.email_changed", &user.ID, ip, nil)
	case domain.PurposePhoneChange:
		phone, err := tok.PendingPhone()
		if err != nil {
			return domain.ErrRecoveryTokenInvalid
		}
		if err := s.deps.Users.SetPhone(ctx, dbtx, user.ID, phone); err != nil {
			return err
		}
		s.audit(ctx, &user.ID, "auth.phone_changed", &user.ID, ip, nil)
	}

	if err := dbtx.Commit(ctx); err != nil {
		return fmt.Errorf("auth: commit identifier change: %w", err)
	}
	s.notify(ctx, user.ID, "identifier_changed", nil)
	return nil
}

// DeleteAccount soft-deletes the authenticated account (API.md §5.5): step-up
// re-auth, mark deleted, revoke all sessions, bump the token version. A grace
// period precedes the hard purge (PurgeDeletedAccounts).
func (s *service) DeleteAccount(ctx context.Context, cmd DeleteAccountCommand) error {
	user, err := s.deps.Users.FindByID(ctx, cmd.UserID)
	if err != nil {
		return fmt.Errorf("auth: find user: %w", err)
	}
	switch user.AccountState {
	case userdomain.AccountDeleted:
		return domain.ErrAccountAlreadyDeleted
	case userdomain.AccountSuspended:
		return domain.ErrAccountSuspended
	}
	if err := s.reauth(ctx, user, cmd.Reauth); err != nil {
		return err
	}
	now := s.now()

	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("auth: begin tx: %w", err)
	}
	defer dbtx.Rollback(ctx)

	if err := s.deps.Users.MarkDeleted(ctx, dbtx, user.ID, now); err != nil {
		return err
	}
	if err := s.deps.Sessions.RevokeAllByUserID(ctx, dbtx, user.ID); err != nil {
		return fmt.Errorf("auth: revoke sessions: %w", err)
	}
	if _, err := s.deps.Users.BumpTokenVersion(ctx, dbtx, user.ID); err != nil {
		return fmt.Errorf("auth: bump token version: %w", err)
	}
	if err := dbtx.Commit(ctx); err != nil {
		return fmt.Errorf("auth: commit deletion: %w", err)
	}
	s.audit(ctx, &user.ID, "account.deleted", &user.ID, cmd.IPAddress, nil)
	s.notify(ctx, user.ID, "account_deleted", nil)
	return nil
}

// RestoreAccount reactivates a soft-deleted account within the grace period
// (DATABASE.md §4.1), gated by identifier verification via OTP (REC-1
// hierarchy). Existing sessions were revoked at deletion; we revoke again in
// case of stale rows (REC-4).
func (s *service) RestoreAccount(ctx context.Context, cmd RestoreAccountCommand) error {
	ident, err := domain.NewIdentifier(cmd.IdentifierType, cmd.Identifier)
	if err != nil {
		return err
	}
	if err := s.deps.OTP.Verify(ctx, ident, cmd.OTPCode); err != nil {
		return domain.ErrInvalidCredentials
	}

	var user *userdomain.User
	switch ident.Type {
	case domain.IdentifierPhone:
		user, err = s.deps.Users.FindDeletedByPhone(ctx, ident.Value)
	case domain.IdentifierEmail:
		user, err = s.deps.Users.FindDeletedByEmail(ctx, ident.Value)
	default:
		return &domain.ValidationError{Field: "identifier_type", Reason: "unsupported"}
	}
	if err != nil {
		if errors.Is(err, userdomain.ErrUserNotFound) {
			return domain.ErrInvalidCredentials
		}
		return fmt.Errorf("auth: find deleted user: %w", err)
	}

	now := s.now()
	cutoff := now.Add(-s.deps.DeletionGracePeriod)

	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("auth: begin tx: %w", err)
	}
	defer dbtx.Rollback(ctx)

	if err := s.deps.Users.Restore(ctx, dbtx, user.ID, cutoff); err != nil {
		return err
	}
	if err := s.deps.Sessions.RevokeAllByUserID(ctx, dbtx, user.ID); err != nil {
		return fmt.Errorf("auth: revoke sessions: %w", err)
	}
	if err := dbtx.Commit(ctx); err != nil {
		return fmt.Errorf("auth: commit restore: %w", err)
	}
	s.audit(ctx, &user.ID, "account.restored", &user.ID, cmd.IPAddress, nil)
	s.notify(ctx, user.ID, "account_restored", nil)
	return nil
}

// PurgeDeletedAccounts hard-deletes accounts soft-deleted past the grace period
// (DATABASE.md §4.1 retention → hard-purge worker). Child rows (credentials,
// sessions, recovery tokens) are removed in the same transaction; login history
// and audit entries have their user_id set to NULL (retained for forensics).
// Returns the number of accounts purged.
func (s *service) PurgeDeletedAccounts(ctx context.Context) (int64, error) {
	cutoff := s.now().Add(-s.deps.DeletionGracePeriod)
	return s.deps.Users.PurgeDeleted(ctx, cutoff)
}

// ListLoginHistory returns the caller's own login history (security-review
// screen), newest first. Only the caller's own events are returned (SESS-3
// identity scoping).
func (s *service) ListLoginHistory(ctx context.Context, cmd ListLoginHistoryCommand) ([]LoginEventInfo, error) {
	limit := cmd.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	events, err := s.deps.LoginHistory.ListByUser(ctx, cmd.UserID, limit)
	if err != nil {
		return nil, fmt.Errorf("auth: list login history: %w", err)
	}
	out := make([]LoginEventInfo, len(events))
	for i, e := range events {
		out[i] = LoginEventInfo{
			ID:         e.ID,
			Method:     string(e.Method),
			Success:    e.Success,
			NewDevice:  e.NewDevice,
			DeviceID:   e.DeviceID,
			IPAddress:  e.IPAddress,
			UserAgent:  e.UserAgent,
			Identifier: e.Identifier,
			CreatedAt:  e.CreatedAt,
		}
	}
	return out, nil
}
