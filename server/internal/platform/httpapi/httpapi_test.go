package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/apierr"
)

func TestPathID(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		want  int64
		valid bool
	}{
		{"numeric", "/v1/c/123", 123, true},
		{"non-numeric", "/v1/c/abc", 0, false},
		{"zero", "/v1/c/0", 0, false},
		{"negative", "/v1/c/-5", 0, false},
		{"overflow", "/v1/c/999999999999999999999", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.SetPathValue("conversation_id", strings.TrimPrefix(tt.path, "/v1/c/"))
			got, err := PathID(req, "conversation_id")
			if tt.valid {
				if err != nil || got != tt.want {
					t.Errorf("PathID = %d, %v; want %d, nil", got, err, tt.want)
				}
			} else if err == nil {
				t.Errorf("PathID = %d, nil; want error", got)
			}
		})
	}
}

func TestQueryLimit(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  int
		err   bool
	}{
		{"default", "", 50, false},
		{"explicit", "limit=20", 20, false},
		{"clamped", "limit=500", 100, false},
		{"malformed", "limit=abc", 0, true},
		{"zero", "limit=0", 0, true},
		{"negative", "limit=-3", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/c?"+tt.query, nil)
			got, err := QueryLimit(req, 50, 100)
			if tt.err {
				if err == nil {
					t.Errorf("QueryLimit = %d, nil; want error", got)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("QueryLimit = %d, %v; want %d, nil", got, err, tt.want)
			}
		})
	}
}

func TestParseIDs(t *testing.T) {
	got, err := ParseIDs([]string{"1", "2", "3"})
	if err != nil || len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Errorf("ParseIDs = %v, %v; want [1 2 3], nil", got, err)
	}

	for _, bad := range [][]string{{"abc"}, {"0"}, {"-1"}, {"1", "2", "x"}} {
		if _, err := ParseIDs(bad); err == nil {
			t.Errorf("ParseIDs(%v) = nil error; want 422", bad)
		}
	}
}

func TestDecodeJSON(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"a":1}`))
		var dst struct {
			A int `json:"a"`
		}
		if err := DecodeJSON(req, &dst); err != nil {
			t.Fatalf("DecodeJSON: %v", err)
		}
		if dst.A != 1 {
			t.Errorf("dst.A = %d, want 1", dst.A)
		}
	})

	t.Run("malformed is 422", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"a":`))
		err := DecodeJSON(req, &struct{}{})
		ae := apierr.AsError(err)
		if ae == nil || ae.Code != "VALIDATION_ERROR" {
			t.Errorf("malformed body err = %v, want 422", err)
		}
	})

	t.Run("oversized is 413", func(t *testing.T) {
		// Valid JSON that exceeds the 64 KiB cap, so the decoder hits the
		// MaxBytesReader limit instead of a syntax error.
		body := strings.NewReader(`{"data":"` + strings.Repeat("x", maxBodyBytes) + `"}`)
		req := httptest.NewRequest(http.MethodPost, "/", body)
		err := DecodeJSON(req, &struct{}{})
		ae := apierr.AsError(err)
		if ae == nil || ae.Code != "PAYLOAD_TOO_LARGE" {
			t.Errorf("oversized body err = %v, want 413", err)
		}
	})
}

func TestListEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/c", nil)
	next := "abc"
	List(rec, req, nil, &next, true, 50)

	var env ListEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if env.Data == nil {
		t.Error("nil data must render as an empty array")
	}
	if env.Pagination.NextCursor == nil || *env.Pagination.NextCursor != "abc" {
		t.Errorf("next_cursor = %v, want abc", env.Pagination.NextCursor)
	}
	if !env.Pagination.HasMore || env.Pagination.Limit != 50 {
		t.Errorf("pagination = %+v, want has_more=true limit=50", env.Pagination)
	}
}

func TestErrNoPrincipal(t *testing.T) {
	ae := apierr.AsError(ErrNoPrincipal())
	if ae == nil || ae.Code != "UNAUTHORIZED" {
		t.Errorf("ErrNoPrincipal = %v, want UNAUTHORIZED", ae)
	}
}
