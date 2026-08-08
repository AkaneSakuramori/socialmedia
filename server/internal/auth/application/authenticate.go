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
	user, _, err := s.authenticateClaims(ctx, token, deviceID)
	return user, err
}

// AuthenticateClaims is Authenticate plus the validated claim set. The WS
// gateway needs the session id to bind a socket to (user_id, session_id) and to
// enforce per-session revocation (API.md §16.1). The checks are identical to
// Authenticate; only the return is richer.
func (s *service) AuthenticateClaims(ctx context.Context, token, deviceID string) (*userdomain.User, *domain.AccessClaims, error) {
	return s.authenticateClaims(ctx, token, deviceID)
}

func (s *service) authenticateClaims(ctx context.Context, token, deviceID string) (*userdomain.User, *domain.AccessClaims, error) {
	claims, err := s.deps.Verifier.VerifyAccess(token)
	if err != nil {
		if errors.Is(err, domain.ErrTokenExpired) || errors.Is(err, domain.ErrTokenInvalid) ||
			errors.Is(err, domain.ErrTokenRevoked) {
			return nil, nil, err
		}
		return nil, nil, domain.ErrTokenInvalid
	}

	user, err := s.deps.Users.FindByID(ctx, claims.UserID)
	if err != nil {
		if errors.Is(err, userdomain.ErrUserNotFound) {
			// A token for a missing account is indistinguishable from a bogus
			// token (no account enumeration through auth).
			return nil, nil, domain.ErrTokenRevoked
		}
		return nil, nil, err
	}
	switch user.AccountState {
	case userdomain.AccountSuspended:
		return nil, nil, domain.ErrAccountSuspended
	case userdomain.AccountDeleted:
		return nil, nil, domain.ErrAccountDeleted
	}

	if claims.TokenVersion != user.TokenVersion {
		return nil, nil, domain.ErrTokenRevoked
	}

	// The session must be the token's own, bound to the presenting device, and
	// active. A revoked/expired/suspended session rejects the token even when
	// the JWT itself is unexpired (SESS-3, API.md §4.5).
	sess, err := s.deps.Sessions.FindByDeviceID(ctx, user.ID, deviceID)
	if err != nil {
		if errors.Is(err, domain.ErrSessionNotFound) {
			return nil, nil, domain.ErrSessionRevoked
		}
		return nil, nil, err
	}
	if sess.ID != claims.SessionID {
		return nil, nil, domain.ErrSessionRevoked
	}
	if sess.State != domain.SessionActive {
		return nil, nil, domain.ErrSessionRevoked
	}

	return user, claims, nil
}
