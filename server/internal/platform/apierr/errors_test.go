package apierr

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/observability"
)

func TestConstructorsClassify(t *testing.T) {
	tests := []struct {
		name string
		err  *Error
		want Code
		http int
		rtry bool
	}{
		{"validation", Validation("bad fields", FieldError{Field: "content", Reason: "must_not_be_blank"}), CodeValidationError, 422, false},
		{"unauthorized", Unauthorized("token required"), CodeUnauthorized, 401, false},
		{"forbidden", Forbidden("nope"), CodeForbidden, 403, false},
		{"not found", NotFound("gone"), CodeNotFound, 404, false},
		{"conflict", Conflict("taken"), CodeConflict, 409, false},
		{"rate limited", RateLimited("slow down"), CodeRateLimited, 429, true},
		{"too large", PayloadTooLarge("too big"), CodePayloadTooLarge, 413, false},
		{"internal", Internal("boom"), CodeInternal, 500, true},
		{"unavailable", ServiceUnavailable("draining"), CodeServiceUnavailable, 503, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.Code != tt.want || tt.err.Status() != tt.http || tt.err.Retryable != tt.rtry {
				t.Errorf("misclassified: got code=%s status=%d retryable=%v", tt.err.Code, tt.err.Status(), tt.err.Retryable)
			}
			if tt.err.Error() == "" {
				t.Error("Error() must not be empty")
			}
		})
	}
}

func TestErrorsIsMatchesCode(t *testing.T) {
	if !errors.Is(NotFound("a"), NotFound("b")) {
		t.Error("errors.Is should match two NotFound errors by code")
	}
	if errors.Is(NotFound("a"), Forbidden("b")) {
		t.Error("errors.Is should not match different codes")
	}
}

func TestAsErrorPassesThroughAndWraps(t *testing.T) {
	if got := AsError(NotFound("x")); got.Code != CodeNotFound {
		t.Errorf("AsError(classified) = %v, want passthrough", got.Code)
	}
	got := AsError(errors.New("db exploded"))
	if got.Code != CodeInternal || got.Err == nil {
		t.Errorf("AsError(unexpected) = %v, want wrapped internal", got)
	}
	if got := AsError(nil); got != nil {
		t.Errorf("AsError(nil) = %v, want nil", got)
	}
}

func TestWriteEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/test", nil)
	req = req.WithContext(observability.WithRequestID(req.Context(), "req_abc123"))

	Write(rec, req, Validation("one or more fields failed", FieldError{Field: "email", Reason: "must_be_valid"}))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("content-type = %q, want application/problem+json", ct)
	}

	var env envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.Code != CodeValidationError {
		t.Errorf("code = %q, want VALIDATION_ERROR", env.Code)
	}
	if env.Status != 422 || env.Retryable {
		t.Errorf("status/retryable wrong: %d/%v", env.Status, env.Retryable)
	}
	if env.RequestID != "req_abc123" {
		t.Errorf("request_id = %q, want req_abc123", env.RequestID)
	}
	if env.Instance != "/v1/test" {
		t.Errorf("instance = %q, want /v1/test", env.Instance)
	}
	if len(env.Errors) != 1 || env.Errors[0].Field != "email" {
		t.Errorf("errors[] wrong: %+v", env.Errors)
	}
}

func TestWriteRetryAfterOn429(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	Write(rec, req, RateLimited("back off"))
	if rec.Header().Get("Retry-After") == "" {
		t.Error("expected Retry-After on 429")
	}
}

func TestWriteSanitizesUnexpectedErrors(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	Write(rec, req, errors.New("secret connection string leaked"))
	if strings.Contains(rec.Body.String(), "secret connection") {
		t.Error("unexpected error detail leaked to client")
	}
}
