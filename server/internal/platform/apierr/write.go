package apierr

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/observability"
)

// baseErrorTypeURL is the prefix for the RFC 9457 "type" member. Per API.md
// §2.5, clients should treat it as documentation, not logic.
const baseErrorTypeURL = "https://api.socialmedia.example/errors/"

// envelope is the wire representation of an API error (API.md §2.5).
type envelope struct {
	Type      string       `json:"type"`
	Title     string       `json:"title"`
	Status    int          `json:"status"`
	Detail    string       `json:"detail"`
	Code      Code         `json:"code"`
	Instance  string       `json:"instance"`
	RequestID string       `json:"request_id"`
	Retryable bool         `json:"retryable"`
	Errors    []FieldError `json:"errors,omitempty"`
}

// Write serializes err as application/problem+json. Handlers return errors and
// this single mapper does the translation (ENGINEERING.md §14.3): handlers
// never format error bodies themselves.
func Write(w http.ResponseWriter, r *http.Request, err error) {
	ae := AsError(err)
	if ae == nil {
		return
	}

	env := envelope{
		Type:      baseErrorTypeURL + typeSlug(ae.Code),
		Title:     ae.Title(),
		Status:    ae.Status(),
		Detail:    ae.Detail,
		Code:      ae.Code,
		Instance:  r.URL.Path,
		RequestID: observability.RequestIDFrom(r.Context()),
		Retryable: ae.Retryable,
		Errors:    ae.Fields,
	}

	// Sanitize: a classified error may still carry an empty or internal detail.
	if env.Detail == "" {
		env.Detail = "an error occurred"
	}

	w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
	if ae.Retryable && (ae.Status() == http.StatusTooManyRequests || ae.Status() == http.StatusServiceUnavailable) {
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(env.Status)
	_ = json.NewEncoder(w).Encode(env)
}

// typeSlug converts a code to its lowercase snake_case type slug:
// VALIDATION_ERROR -> validation_error.
func typeSlug(code Code) string {
	return strings.ToLower(strings.ReplaceAll(string(code), "_", "-"))
}
