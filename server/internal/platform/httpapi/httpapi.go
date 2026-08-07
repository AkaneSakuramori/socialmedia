// Package httpapi is the shared delivery/transport layer for REST handlers
// (ENGINEERING.md §8, §9). It owns the pieces every HTTP surface needs so
// domain handlers stay thin: JSON response helpers with the standard list
// envelope (API.md §2.4), request parsing (path/query/body) with bounded
// payloads, the bearer-token gateway middleware (SECURITY_SPEC.md JWT-5), the
// Stripe-style Idempotency-Key middleware (API.md §2.7, ENGINEERING.md §29),
// and the one-time domain-error → RFC 9457 mapping (ENGINEERING.md §14).
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/apierr"
)

// maxBodyBytes bounds request bodies (API.md Appendix B: payload limits). JSON
// payloads above this are rejected with 413 before decoding.
const maxBodyBytes = 64 << 10 // 64 KiB

// Pagination is the list envelope's pagination object (API.md §2.4).
type Pagination struct {
	NextCursor *string `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
	Limit      int     `json:"limit"`
}

// ListEnvelope is the standard list response (API.md §2.4).
type ListEnvelope struct {
	Data       []any      `json:"data"`
	Pagination Pagination `json:"pagination"`
}

// writeJSON serializes v as application/json with the given status. This is the
// only JSON writer handlers use; errors go through apierr.Write instead.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// OK writes a 200 response body.
func OK(w http.ResponseWriter, r *http.Request, v any) {
	writeJSON(w, http.StatusOK, v)
}

// Created writes a 201 response body.
func Created(w http.ResponseWriter, r *http.Request, v any) {
	writeJSON(w, http.StatusCreated, v)
}

// NoContent writes a 204 response.
func NoContent(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

// List writes the standard paginated envelope for a []any page. next is the
// opaque cursor string or nil; nil data renders as an empty array.
func List(w http.ResponseWriter, r *http.Request, data []any, next *string, hasMore bool, limit int) {
	if data == nil {
		data = []any{}
	}
	writeJSON(w, http.StatusOK, ListEnvelope{
		Data:       data,
		Pagination: Pagination{NextCursor: next, HasMore: hasMore, Limit: limit},
	})
}

// pathID parses an int64 path parameter (Go 1.22 routing: r.PathValue). Ids
// serialize as strings on the wire (API.md §2.2) but are 64-bit snowflakes on
// the wire edge, so a non-numeric value is malformed.
func pathID(r *http.Request, name string) (int64, error) {
	raw := r.PathValue(name)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, apierr.Validation("invalid path parameter", apierr.FieldError{Field: name, Reason: "invalid_id"})
	}
	if id <= 0 {
		return 0, apierr.Validation("invalid path parameter", apierr.FieldError{Field: name, Reason: "must_be_positive"})
	}
	return id, nil
}

// PathID parses an int64 path parameter, rejecting malformed or non-positive
// values with a 422.
func PathID(r *http.Request, name string) (int64, error) { return pathID(r, name) }

// queryLimit parses the page-size query parameter with the given default and
// max (API.md §2.6). A malformed value is a 400.
func queryLimit(r *http.Request, def, max int) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, apierr.Validation("invalid query parameter", apierr.FieldError{Field: "limit", Reason: "invalid_limit"})
	}
	if n > max {
		n = max
	}
	return n, nil
}

// QueryLimit parses and clamps the limit query parameter.
func QueryLimit(r *http.Request, def, max int) (int, error) { return queryLimit(r, def, max) }

// ParseIDs converts a wire-format id array (strings, API.md §2.2) to int64
// snowflake ids. A malformed or non-positive entry is a 422 on the caller's
// field.
func ParseIDs(raw []string) ([]int64, error) {
	ids := make([]int64, 0, len(raw))
	for _, s := range raw {
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil || id <= 0 {
			return nil, apierr.Validation("invalid id in list", apierr.FieldError{Field: "ids", Reason: "invalid_id"})
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// ErrNoPrincipal reports an internal routing invariant violation: a handler
// reached without an authenticated principal in its context.
func ErrNoPrincipal() error {
	return apierr.Unauthorized("authentication required")
}

// DecodeJSON decodes a JSON body with a bounded reader. Malformed JSON is a
// 422; an oversized body is a 413. dst must be a pointer.
func DecodeJSON(r *http.Request, dst any) error {
	body := http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	defer body.Close()
	if err := json.NewDecoder(body).Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return apierr.PayloadTooLarge("request body exceeds the 64 KiB limit")
		}
		return apierr.Validation("malformed request body", apierr.FieldError{Field: "body", Reason: "invalid_json"})
	}
	return nil
}

// unauthorized is a convenience for 401 responses without an underlying cause.
func unauthorized(detail string) *apierr.Error {
	return apierr.Unauthorized(detail)
}

// validationErr is a convenience for a single-field 422 response.
func validationErr(field, reason string) *apierr.Error {
	return apierr.Validation("validation failed", apierr.FieldError{Field: field, Reason: reason})
}

// apierrUnauthorized is the idempotency middleware's 401 helper.
func apierrUnauthorized(detail string) *apierr.Error { return apierr.Unauthorized(detail) }

// apierrConflict is the idempotency middleware's 409 helper.
func apierrConflict(detail string) *apierr.Error { return apierr.Conflict(detail) }

// apierrWrap classifies an infrastructure failure (e.g. Redis unavailable) as
// an internal error while preserving the cause chain for logging.
func apierrWrap(err error, detail string) *apierr.Error { return apierr.Wrap(err, detail) }
