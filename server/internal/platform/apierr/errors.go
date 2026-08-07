// Package apierr implements the platform's error contract (ENGINEERING.md §14,
// API.md §2.5, Appendix A). Errors are classified with a stable machine-readable
// code and serialized as RFC 9457 application/problem+json. The code is the only
// field clients switch on; HTTP status and title are derived from it.
package apierr

import (
	"errors"
	"fmt"
	"net/http"
)

// Code is a stable, machine-readable error code from API.md Appendix A.
type Code string

// Core codes used by the foundation, plus the chat/auth codes the messaging
// milestones need (API.md Appendix A). Codes are the only wire field clients
// switch on; HTTP status and title are derived from them.
const (
	CodeValidationError    Code = "VALIDATION_ERROR"
	CodeUnauthorized       Code = "UNAUTHORIZED"
	CodeForbidden          Code = "FORBIDDEN"
	CodeNotFound           Code = "NOT_FOUND"
	CodeConflict           Code = "CONFLICT"
	CodeRateLimited        Code = "RATE_LIMITED"
	CodePayloadTooLarge    Code = "PAYLOAD_TOO_LARGE"
	CodeInternal           Code = "INTERNAL_ERROR"
	CodeServiceUnavailable Code = "SERVICE_UNAVAILABLE"

	// Authentication / session codes (gateway + auth, API.md Appendix A).
	CodeTokenExpired     Code = "TOKEN_EXPIRED"
	CodeTokenRevoked     Code = "TOKEN_REVOKED"
	CodeSessionRevoked   Code = "SESSION_REVOKED"
	CodeAccountSuspended Code = "ACCOUNT_SUSPENDED"
	CodeAccountDeleted   Code = "ACCOUNT_DELETED"

	// Chat/domain codes (API.md Appendix A).
	CodeUserNotFound         Code = "USER_NOT_FOUND"
	CodeConversationNotFound Code = "CONVERSATION_NOT_FOUND"
	CodeBlocked              Code = "BLOCKED"
	CodeNotAMember           Code = "NOT_A_MEMBER"
	CodeInsufficientRole     Code = "INSUFFICIENT_ROLE"
	CodeDirectExists         Code = "DIRECT_EXISTS"
	CodeQuotaExceeded        Code = "QUOTA_EXCEEDED"
)

// codeMeta describes a code's HTTP status and human title.
type codeMeta struct {
	status int
	title  string
}

var metaByCode = map[Code]codeMeta{
	CodeValidationError:    {status: http.StatusUnprocessableEntity, title: "Validation Error"},
	CodeUnauthorized:       {status: http.StatusUnauthorized, title: "Unauthorized"},
	CodeForbidden:          {status: http.StatusForbidden, title: "Forbidden"},
	CodeNotFound:           {status: http.StatusNotFound, title: "Not Found"},
	CodeConflict:           {status: http.StatusConflict, title: "Conflict"},
	CodeRateLimited:        {status: http.StatusTooManyRequests, title: "Rate Limited"},
	CodePayloadTooLarge:    {status: http.StatusRequestEntityTooLarge, title: "Payload Too Large"},
	CodeInternal:           {status: http.StatusInternalServerError, title: "Internal Error"},
	CodeServiceUnavailable: {status: http.StatusServiceUnavailable, title: "Service Unavailable"},

	CodeTokenExpired:     {status: http.StatusUnauthorized, title: "Token Expired"},
	CodeTokenRevoked:     {status: http.StatusUnauthorized, title: "Token Revoked"},
	CodeSessionRevoked:   {status: http.StatusUnauthorized, title: "Session Revoked"},
	CodeAccountSuspended: {status: http.StatusForbidden, title: "Account Suspended"},
	CodeAccountDeleted:   {status: http.StatusForbidden, title: "Account Deleted"},

	CodeUserNotFound:         {status: http.StatusNotFound, title: "User Not Found"},
	CodeConversationNotFound: {status: http.StatusNotFound, title: "Conversation Not Found"},
	CodeBlocked:              {status: http.StatusForbidden, title: "Blocked"},
	CodeNotAMember:           {status: http.StatusForbidden, title: "Not A Member"},
	CodeInsufficientRole:     {status: http.StatusForbidden, title: "Insufficient Role"},
	CodeDirectExists:         {status: http.StatusConflict, title: "Direct Conversation Exists"},
	CodeQuotaExceeded:        {status: http.StatusTooManyRequests, title: "Quota Exceeded"},
}

// FieldError is a field-level validation problem (API.md §2.5 errors[]).
type FieldError struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// Error is a classified API error. It carries the stable code, the derived
// HTTP status, and optionally an underlying cause that is logged but never
// sent to the client.
type Error struct {
	Code      Code
	Detail    string
	Retryable bool
	Fields    []FieldError
	Err       error // underlying cause; logged only.
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Detail, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Detail)
}

// Unwrap exposes the underlying cause so errors.Is / errors.As traverse the chain.
func (e *Error) Unwrap() error { return e.Err }

// Status returns the HTTP status derived from the code.
func (e *Error) Status() int {
	if m, ok := metaByCode[e.Code]; ok {
		return m.status
	}
	return http.StatusInternalServerError
}

// Title returns the human-readable title derived from the code.
func (e *Error) Title() string {
	if m, ok := metaByCode[e.Code]; ok {
		return m.title
	}
	return "Error"
}

// Is lets errors.Is treat distinct instances of the same Code as equal, which
// lets callers branch on a code without relying on sentinel singletons.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	if !ok {
		return false
	}
	return e.Code == t.Code
}

// Constructors.

// New builds a classified error with an explicit retryable flag.
func New(code Code, detail string, retryable bool, err error) *Error {
	return &Error{Code: code, Detail: detail, Retryable: retryable, Err: err}
}

// Validation reports one or more field-level validation failures (422).
func Validation(detail string, fields ...FieldError) *Error {
	return &Error{Code: CodeValidationError, Detail: detail, Fields: fields}
}

// Unauthorized reports a missing or invalid credential (401).
func Unauthorized(detail string) *Error {
	return &Error{Code: CodeUnauthorized, Detail: detail}
}

// Forbidden reports an authorization failure (403).
func Forbidden(detail string) *Error {
	return &Error{Code: CodeForbidden, Detail: detail}
}

// NotFound reports a missing resource (404).
func NotFound(detail string) *Error {
	return &Error{Code: CodeNotFound, Detail: detail}
}

// Conflict reports a resource conflict (409).
func Conflict(detail string) *Error {
	return &Error{Code: CodeConflict, Detail: detail}
}

// TokenExpired reports an expired access token (401, retryable via refresh).
func TokenExpired(detail string) *Error {
	return &Error{Code: CodeTokenExpired, Detail: detail, Retryable: true}
}

// TokenRevoked reports a token whose token-version claim is stale (401).
func TokenRevoked(detail string) *Error {
	return &Error{Code: CodeTokenRevoked, Detail: detail}
}

// SessionRevoked reports that the token's session was revoked (401).
func SessionRevoked(detail string) *Error {
	return &Error{Code: CodeSessionRevoked, Detail: detail}
}

// AccountSuspended reports a suspended account (403).
func AccountSuspended(detail string) *Error {
	return &Error{Code: CodeAccountSuspended, Detail: detail}
}

// AccountDeleted reports a deleted account (403).
func AccountDeleted(detail string) *Error {
	return &Error{Code: CodeAccountDeleted, Detail: detail}
}

// UserNotFound reports a missing user (404).
func UserNotFound(detail string) *Error {
	return &Error{Code: CodeUserNotFound, Detail: detail}
}

// ConversationNotFound reports a missing conversation (404).
func ConversationNotFound(detail string) *Error {
	return &Error{Code: CodeConversationNotFound, Detail: detail}
}

// Blocked reports that the target user blocked the caller (403).
func Blocked(detail string) *Error {
	return &Error{Code: CodeBlocked, Detail: detail}
}

// NotAMember reports that the caller is not in the conversation (403).
func NotAMember(detail string) *Error {
	return &Error{Code: CodeNotAMember, Detail: detail}
}

// InsufficientRole reports that the caller's role is too low (403).
func InsufficientRole(detail string) *Error {
	return &Error{Code: CodeInsufficientRole, Detail: detail}
}

// DirectExists reports that a direct conversation already exists (409).
func DirectExists(detail string) *Error {
	return &Error{Code: CodeDirectExists, Detail: detail}
}

// QuotaExceeded reports that the caller hit a per-user quota (429).
func QuotaExceeded(detail string) *Error {
	return &Error{Code: CodeQuotaExceeded, Detail: detail}
}

// RateLimited reports that the caller exceeded a rate limit (429, retryable).
func RateLimited(detail string) *Error {
	return &Error{Code: CodeRateLimited, Detail: detail, Retryable: true}
}

// PayloadTooLarge reports an oversized body (413).
func PayloadTooLarge(detail string) *Error {
	return &Error{Code: CodePayloadTooLarge, Detail: detail}
}

// Internal reports an unexpected server failure (500, retryable). The cause is
// logged; only the generic message reaches the client.
func Internal(detail string) *Error {
	return &Error{Code: CodeInternal, Detail: detail, Retryable: true}
}

// ServiceUnavailable reports that the service is not ready (503, retryable).
func ServiceUnavailable(detail string) *Error {
	return &Error{Code: CodeServiceUnavailable, Detail: detail, Retryable: true}
}

// Wrap classifies an unexpected error as an internal error while preserving the
// cause chain for logging. Callers use Wrap(err, "sending message") at the
// boundary (ENGINEERING.md §14.3).
func Wrap(err error, detail string) *Error {
	return &Error{Code: CodeInternal, Detail: detail, Retryable: true, Err: err}
}

// AsError converts any error into a classified *Error. Already-classified
// errors pass through; unknown errors are wrapped as INTERNAL_ERROR.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var ae *Error
	if errors.As(err, &ae) {
		return ae
	}
	return Wrap(err, "an internal error occurred")
}
