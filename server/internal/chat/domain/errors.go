package domain

import "errors"

// Sentinel errors for the chat module (API.md Appendix A). The delivery layer
// maps these to wire codes exactly once at the boundary (ENGINEERING.md §14);
// the application never formats responses.

var (
	// ErrInvalidConversationType means the wire type is not direct|group.
	ErrInvalidConversationType = errors.New("chat: invalid conversation type")
	// ErrInvalidRole means the wire role is not owner|admin|member.
	ErrInvalidRole = errors.New("chat: invalid role")
	// ErrInvalidHistoryVisible means history_visible is not all|from_join.
	ErrInvalidHistoryVisible = errors.New("chat: invalid history_visible setting")
	// ErrGroupTitleRequired means a group was created without a title
	// (DATABASE.md §5.1 CHECK (type='direct' OR title IS NOT NULL)).
	ErrGroupTitleRequired = errors.New("chat: group title required")
	// ErrConversationNotFound means no conversation matches the query
	// (API.md Appendix A CONVERSATION_NOT_FOUND, 404).
	ErrConversationNotFound = errors.New("chat: conversation not found")
	// ErrMembershipNotFound means the user has no row in the conversation
	// (active or not).
	ErrMembershipNotFound = errors.New("chat: membership not found")
	// ErrNotMember means the caller is not an active member and cannot read or
	// mutate the conversation (API.md Appendix A NOT_A_MEMBER, 403).
	ErrNotMember = errors.New("chat: caller is not a member")
	// ErrInsufficientRole means the caller's role is below the required level
	// (API.md Appendix A INSUFFICIENT_ROLE, 403).
	ErrInsufficientRole = errors.New("chat: insufficient role")
	// ErrDirectExists means a direct conversation with the same counterpart
	// already exists and the caller opted into a conflict (API.md §7.2 → 409
	// DIRECT_EXISTS). The create flow returns the existing conversation instead.
	ErrDirectExists = errors.New("chat: direct conversation exists")
	// ErrUnknownParticipant means a participant id has no live account
	// (API.md §7.2 → 422 participant_ids).
	ErrUnknownParticipant = errors.New("chat: participant does not exist")
	// ErrConversationFull means a group add would exceed 500 members
	// (API.md §7.2/§7.6).
	ErrConversationFull = errors.New("chat: conversation is full")
	// ErrCannotRemoveOwner means an owner cannot be removed (API.md §7.7:
	// "owner cannot be removed").
	ErrCannotRemoveOwner = errors.New("chat: owner cannot be removed")
	// ErrCannotDemoteOwner means an owner cannot be demoted without transfer
	// (API.md §7.8: only the owner may grant/revoke owner).
	ErrCannotDemoteOwner = errors.New("chat: owner cannot be demoted")
	// ErrOnlyOwnerMayChangeOwner means a non-owner tried to grant/revoke the
	// owner role (API.md §7.8).
	ErrOnlyOwnerMayChangeOwner = errors.New("chat: only the owner may change the owner role")
	// ErrInvalidCursor means a pagination cursor failed decoding or validation
	// (API.md §2.6; delivered as a field-level 422).
	ErrInvalidCursor = errors.New("chat: invalid pagination cursor")
	// ErrInvalidMessageType means the wire type is not one of API.md §8.1.
	ErrInvalidMessageType = errors.New("chat: invalid message type")
	// ErrMessageContentRequired means a text message has no content and a media
	// message has no attachment envelope (API.md §8.2 "exactly one of").
	ErrMessageContentRequired = errors.New("chat: message requires content or media")
	// ErrMessageMediaLimit means a media message exceeded maxMediaPerMessage.
	ErrMessageMediaLimit = errors.New("chat: too many media attachments")
	// ErrMessageNotFound means no message matches the query (API.md Appendix A
	// MESSAGE_NOT_FOUND, 404).
	ErrMessageNotFound = errors.New("chat: message not found")
	// ErrNotSender means the caller is not the message's sender for an edit or
	// delete (API.md §8.4/§8.5, 403).
	ErrNotSender = errors.New("chat: caller is not the message sender")
	// ErrEditWindowExpired means the edit window has passed (API.md §8.4, 403).
	ErrEditWindowExpired = errors.New("chat: edit window expired")
	// ErrMessageDeleted means the target message is a tombstone and cannot be
	// edited or reacted to (API.md §8.4/§8.6).
	ErrMessageDeleted = errors.New("chat: message is deleted")
	// ErrMessageNotEditable means an edit was attempted on a non-text field
	// (type/media cannot change, API.md §8.4).
	ErrMessageNotEditable = errors.New("chat: message is not editable")
	// ErrReplyNotFound means reply_to_seq references a message that does not
	// exist in the conversation (API.md §8.2).
	ErrReplyNotFound = errors.New("chat: reply target not found")
	// ErrMentionNotMember means a mention targets a non-member (API.md §8.2).
	ErrMentionNotMember = errors.New("chat: mention is not a conversation member")
	// ErrInvalidEmoji means the reaction emoji is missing or malformed
	// (API.md §8.6).
	ErrInvalidEmoji = errors.New("chat: invalid reaction emoji")
	// ErrReactionLimit means a message already has 20 distinct emoji
	// (API.md §8.6: "max 20 distinct emoji per message").
	ErrReactionLimit = errors.New("chat: reaction limit reached")
	// ErrSequenceConflict means a sequence collision was detected against the
	// composite PK (DATABASE.md §11 final guard); the caller retries with the
	// next sequence.
	ErrSequenceConflict = errors.New("chat: sequence conflict, retry")
	// ErrClientMsgIDConflict means the client_msg_id dedupe fired but the
	// original row could not be resolved — a corruption-level inconsistency.
	ErrClientMsgIDConflict = errors.New("chat: duplicate client_msg_id without original")
)

// ValidationError is a field-level validation failure (API.md §2.5 errors[]).
// The delivery layer maps it to 422 VALIDATION_ERROR with the field/reason
// pair preserved.
type ValidationError struct {
	Field  string
	Reason string
}

// Error implements the error interface.
func (e *ValidationError) Error() string {
	return "chat: validation failed on " + e.Field + ": " + e.Reason
}
