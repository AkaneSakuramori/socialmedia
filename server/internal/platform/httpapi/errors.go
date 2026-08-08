package httpapi

import (
	"errors"
	"net/http"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	chatdomain "github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/apierr"
	userdomain "github.com/AkaneSakuramori/socialmedia/server/internal/user/domain"
)

// WriteError maps a domain/application error to its wire code exactly once and
// writes the RFC 9457 body (ENGINEERING.md §14.3). Handlers never format error
// bodies themselves. Already-classified *apierr.Error values pass through.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	apierr.Write(w, r, mapError(err))
}

// mapError translates every sentinel the delivery layer can observe into a
// stable API code (API.md Appendix A). The switch order matters: the most
// specific errors are checked first.
func mapError(err error) error {
	var ae *apierr.Error
	if errors.As(err, &ae) {
		return ae
	}

	// Chat domain (M1).
	var ve *chatdomain.ValidationError
	if errors.As(err, &ve) {
		return apierr.Validation("one or more fields failed validation",
			apierr.FieldError{Field: ve.Field, Reason: ve.Reason})
	}
	switch {
	case errors.Is(err, chatdomain.ErrConversationNotFound):
		return apierr.ConversationNotFound("conversation not found")
	case errors.Is(err, chatdomain.ErrNotMember):
		return apierr.NotAMember("caller is not a member of this conversation")
	case errors.Is(err, chatdomain.ErrInsufficientRole),
		errors.Is(err, chatdomain.ErrOnlyOwnerMayChangeOwner):
		return apierr.InsufficientRole("insufficient role for this operation")
	case errors.Is(err, chatdomain.ErrDirectExists):
		return apierr.DirectExists("a direct conversation already exists")
	case errors.Is(err, chatdomain.ErrCannotRemoveOwner),
		errors.Is(err, chatdomain.ErrCannotDemoteOwner):
		return apierr.Forbidden("the conversation owner cannot be removed or demoted")
	case errors.Is(err, chatdomain.ErrConversationFull):
		return apierr.Validation("conversation is full",
			apierr.FieldError{Field: "user_ids", Reason: "conversation_full"})
	case errors.Is(err, chatdomain.ErrUnknownParticipant):
		return apierr.Validation("unknown participant",
			apierr.FieldError{Field: "participant_ids", Reason: "unknown_user"})
	case errors.Is(err, chatdomain.ErrGroupTitleRequired):
		return apierr.Validation("group title is required",
			apierr.FieldError{Field: "title", Reason: "required"})
	case errors.Is(err, chatdomain.ErrInvalidConversationType):
		return apierr.Validation("invalid conversation type",
			apierr.FieldError{Field: "type", Reason: "invalid_type"})
	case errors.Is(err, chatdomain.ErrInvalidRole):
		return apierr.Validation("invalid role",
			apierr.FieldError{Field: "role", Reason: "invalid_role"})
	case errors.Is(err, chatdomain.ErrInvalidHistoryVisible):
		return apierr.Validation("invalid history_visible setting",
			apierr.FieldError{Field: "settings.history_visible", Reason: "invalid_value"})
	case errors.Is(err, chatdomain.ErrInvalidCursor):
		return apierr.Validation("invalid pagination cursor",
			apierr.FieldError{Field: "cursor", Reason: "invalid_cursor"})
	case errors.Is(err, chatdomain.ErrMembershipNotFound):
		return apierr.Validation("membership not found",
			apierr.FieldError{Field: "user_id", Reason: "not_a_member"})
	case errors.Is(err, chatdomain.ErrMessageNotFound):
		return apierr.MessageNotFound("message not found")
	case errors.Is(err, chatdomain.ErrNotSender),
		errors.Is(err, chatdomain.ErrEditWindowExpired),
		errors.Is(err, chatdomain.ErrMessageDeleted),
		errors.Is(err, chatdomain.ErrMessageNotEditable):
		return apierr.Forbidden("this message cannot be modified")
	case errors.Is(err, chatdomain.ErrReactionLimit):
		return apierr.Validation("reaction limit reached",
			apierr.FieldError{Field: "emoji", Reason: "reaction_limit"})
	case errors.Is(err, chatdomain.ErrInvalidEmoji):
		return apierr.Validation("invalid reaction emoji",
			apierr.FieldError{Field: "emoji", Reason: "invalid_emoji"})
	case errors.Is(err, chatdomain.ErrMentionNotMember):
		return apierr.Validation("mention is not a conversation member",
			apierr.FieldError{Field: "mentions", Reason: "not_a_member"})
	case errors.Is(err, chatdomain.ErrReplyNotFound):
		return apierr.Validation("reply target not found",
			apierr.FieldError{Field: "reply_to_seq", Reason: "invalid_reply_target"})
	}

	// User domain.
	switch {
	case errors.Is(err, userdomain.ErrUserNotFound):
		return apierr.UserNotFound("user not found")
	}

	// Auth domain (gateway boundary).
	switch {
	case errors.Is(err, domain.ErrTokenExpired):
		return apierr.TokenExpired("access token expired")
	case errors.Is(err, domain.ErrTokenRevoked):
		return apierr.TokenRevoked("access token revoked")
	case errors.Is(err, domain.ErrSessionRevoked):
		return apierr.SessionRevoked("session revoked")
	case errors.Is(err, domain.ErrAccountSuspended):
		return apierr.AccountSuspended("account suspended")
	case errors.Is(err, domain.ErrAccountDeleted):
		return apierr.AccountDeleted("account deleted")
	case errors.Is(err, domain.ErrTokenInvalid):
		return apierr.Unauthorized("invalid access token")
	}

	return apierr.Wrap(err, "an internal error occurred")
}
