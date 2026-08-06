package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestNewUsername(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr string // expected ValidationError reason
	}{
		{name: "valid", in: "aya.s", want: "aya.s"},
		{name: "upper is lowercased", in: "Aya.S", want: "aya.s"},
		{name: "surrounding space trimmed", in: "  aya.s  ", want: "aya.s"},
		{name: "digits and underscore", in: "aya_2026", want: "aya_2026"},
		{name: "too short", in: "ab", wantErr: "length"},
		{name: "too long", in: strings.Repeat("a", 31), wantErr: "length"},
		{name: "invalid char", in: "aya!", wantErr: "invalid_chars"},
		{name: "leading dot", in: ".aya", wantErr: "invalid_edges"},
		{name: "trailing underscore", in: "aya_", wantErr: "invalid_edges"},
		{name: "reserved", in: "admin", wantErr: "reserved"},
		{name: "reserved case-insensitive", in: "Support", wantErr: "reserved"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewUsername(tt.in)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("NewUsername(%q) expected error, got %q", tt.in, got)
				}
				var ve *ValidationError
				if !errors.As(err, &ve) || ve.Field != "username" || ve.Reason != tt.wantErr {
					t.Fatalf("NewUsername(%q) error = %v, want field=username reason=%q", tt.in, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewUsername(%q) unexpected error: %v", tt.in, err)
			}
			if got.String() != tt.want {
				t.Fatalf("NewUsername(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNewDisplayName(t *testing.T) {
	if _, err := NewDisplayName(""); err == nil {
		t.Error("empty display name should fail")
	}
	if _, err := NewDisplayName("   "); err == nil {
		t.Error("whitespace display name should fail")
	}
	if _, err := NewDisplayName(strings.Repeat("x", 65)); err == nil {
		t.Error("65-char display name should fail")
	}
	got, err := NewDisplayName("  Aya Salim  ")
	if err != nil {
		t.Fatalf("NewDisplayName error: %v", err)
	}
	if got.String() != "Aya Salim" {
		t.Errorf("NewDisplayName trimmed = %q, want %q", got, "Aya Salim")
	}
}
