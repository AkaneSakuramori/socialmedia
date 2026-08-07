package httpapi

import (
	"errors"
	"net/http"
	"testing"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	chatdomain "github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/apierr"
	userdomain "github.com/AkaneSakuramori/socialmedia/server/internal/user/domain"
)

func TestMapErrorChatDomain(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code apierr.Code
		http int
	}{
		{"conversation not found", chatdomain.ErrConversationNotFound, apierr.CodeConversationNotFound, http.StatusNotFound},
		{"not a member", chatdomain.ErrNotMember, apierr.CodeNotAMember, http.StatusForbidden},
		{"insufficient role", chatdomain.ErrInsufficientRole, apierr.CodeInsufficientRole, http.StatusForbidden},
		{"only owner may change owner", chatdomain.ErrOnlyOwnerMayChangeOwner, apierr.CodeInsufficientRole, http.StatusForbidden},
		{"direct exists", chatdomain.ErrDirectExists, apierr.CodeDirectExists, http.StatusConflict},
		{"cannot remove owner", chatdomain.ErrCannotRemoveOwner, apierr.CodeForbidden, http.StatusForbidden},
		{"cannot demote owner", chatdomain.ErrCannotDemoteOwner, apierr.CodeForbidden, http.StatusForbidden},
		{"conversation full", chatdomain.ErrConversationFull, apierr.CodeValidationError, http.StatusUnprocessableEntity},
		{"unknown participant", chatdomain.ErrUnknownParticipant, apierr.CodeValidationError, http.StatusUnprocessableEntity},
		{"group title required", chatdomain.ErrGroupTitleRequired, apierr.CodeValidationError, http.StatusUnprocessableEntity},
		{"invalid conversation type", chatdomain.ErrInvalidConversationType, apierr.CodeValidationError, http.StatusUnprocessableEntity},
		{"invalid role", chatdomain.ErrInvalidRole, apierr.CodeValidationError, http.StatusUnprocessableEntity},
		{"invalid history visible", chatdomain.ErrInvalidHistoryVisible, apierr.CodeValidationError, http.StatusUnprocessableEntity},
		{"invalid cursor", chatdomain.ErrInvalidCursor, apierr.CodeValidationError, http.StatusUnprocessableEntity},
		{"membership not found", chatdomain.ErrMembershipNotFound, apierr.CodeValidationError, http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapped := mapError(tt.err)
			ae := apierr.AsError(mapped)
			if ae == nil {
				t.Fatal("mapError returned a non-apierr error")
			}
			if ae.Code != tt.code || ae.Status() != tt.http {
				t.Errorf("got code=%s status=%d, want code=%s status=%d", ae.Code, ae.Status(), tt.code, tt.http)
			}
		})
	}
}

func TestMapErrorChatValidationError(t *testing.T) {
	err := &chatdomain.ValidationError{Field: "title", Reason: "too_long"}
	ae := apierr.AsError(mapError(err))
	if ae == nil || ae.Code != apierr.CodeValidationError || ae.Status() != http.StatusUnprocessableEntity {
		t.Fatalf("validation error not mapped to 422: %+v", ae)
	}
	if len(ae.Fields) != 1 || ae.Fields[0].Field != "title" {
		t.Errorf("field not preserved: %+v", ae.Fields)
	}
}

func TestMapErrorUserAndAuthDomain(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code apierr.Code
		http int
	}{
		{"user not found", userdomain.ErrUserNotFound, apierr.CodeUserNotFound, http.StatusNotFound},
		{"token expired", domain.ErrTokenExpired, apierr.CodeTokenExpired, http.StatusUnauthorized},
		{"token revoked", domain.ErrTokenRevoked, apierr.CodeTokenRevoked, http.StatusUnauthorized},
		{"session revoked", domain.ErrSessionRevoked, apierr.CodeSessionRevoked, http.StatusUnauthorized},
		{"account suspended", domain.ErrAccountSuspended, apierr.CodeAccountSuspended, http.StatusForbidden},
		{"account deleted", domain.ErrAccountDeleted, apierr.CodeAccountDeleted, http.StatusForbidden},
		{"token invalid", domain.ErrTokenInvalid, apierr.CodeUnauthorized, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae := apierr.AsError(mapError(tt.err))
			if ae == nil || ae.Code != tt.code || ae.Status() != tt.http {
				t.Errorf("got code=%s status=%d, want code=%s status=%d", ae.Code, ae.Status(), tt.code, tt.http)
			}
		})
	}
}

func TestMapErrorPassesThroughClassifiedErrors(t *testing.T) {
	orig := apierr.RateLimited("slow down")
	ae := apierr.AsError(mapError(orig))
	if ae == nil || ae.Code != apierr.CodeRateLimited {
		t.Errorf("classified error not passed through: %+v", ae)
	}
}

func TestMapErrorWrapsUnknownErrorsAsInternal(t *testing.T) {
	ae := apierr.AsError(mapError(errors.New("boom")))
	if ae == nil || ae.Code != apierr.CodeInternal || ae.Status() != http.StatusInternalServerError {
		t.Errorf("unknown error not wrapped as internal: %+v", ae)
	}
}
