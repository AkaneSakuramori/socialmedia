package application

import (
	"context"
	"errors"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	userdomain "github.com/AkaneSakuramori/socialmedia/server/internal/user/domain"
)

// Authenticate validates an access token at the gateway and returns the account
// behind it (SECURITY_SPEC.md JWT-5). Order of checks, cheapest first:
//
//  1. JWT signature/expiry/issuer/audience (via the TokenVerifier port).
//  2. Account exists and is not suspended/deleted.
//  3. Token-version freshness — the ver claim must equal users.token_version
//     (SESS-6: sign-out-everywhere bumps it, failing every old token).
//  4. The token's session is still active.
//
// Any failure returns a domain sentinel the delivery layer maps to a stable
// wire code; the raw JWT is never exposed.
func (s *service) Authenticate(ctx context.Context, token, deviceID string) (*userdomain.User, error) {
	claims, err := s.deps.Verifier.VerifyAccess(token)
	if err != nil {
		if errors.Is(err, domain.ErrTokenExpired) || errors.Is(err, domain.ErrTokenInvalid) ||
			errors.Is(err, domain.ErrTokenRevoked) {
			return nil, err
		}
		return nil, domain.ErrTokenInvalid
	}

	user, err := s.deps.Users.FindByID(ctx, claims.UserID)
	if err != nil {
		if errors.Is(err, userdomain.ErrUserNotFound) {
			// A token for a missing account is indistinguishable from a bogus
			// token (no account enumeration through auth).
			return nil, domain.ErrTokenRevoked
		}
		return nil, err
	}
	switch user.AccountState {
	case userdomain.AccountSuspended:
		return nil, domain.ErrAccountSuspended
	case userdomain.AccountDeleted:
		return nil, domain.ErrAccountDeleted
	}

	if claims.TokenVersion != user.TokenVersion {
		return nil, domain.ErrTokenRevoked
	}

	// The session must be the token's own, bound to the presenting device, and
	// active. A revoked/expired/suspended session rejects the token even when
	// the JWT itself is unexpired (SESS-3, API.md §4.5).
	sess, err := s.deps.Sessions.FindByDeviceID(ctx, user.ID, deviceID)
	if err != nil {
		if errors.Is(err, domain.ErrSessionNotFound) {
			return nil, domain.ErrSessionRevoked
		}
		return nil, err
	}
	if sess.ID != claims.SessionID {
		return nil, domain.ErrSessionRevoked
	}
	if sess.State != domain.SessionActive {
		return nil, domain.ErrSessionRevoked
	}

	return user, nil
}
