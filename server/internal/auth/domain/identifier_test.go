package domain

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"
)

func TestNewIdentifierPhone(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "plain e164", raw: "+15550123", want: "+15550123"},
		{name: "with spaces", raw: "+1 555 0123", want: "+15550123"},
		{name: "with dashes and parens", raw: "+1-(555)-0123", want: "+15550123"},
		{name: "with dots", raw: "+1.555.0123", want: "+15550123"},
		{name: "india number", raw: "+91 98765 43210", want: "+919876543210"},
		{name: "00 prefix converted", raw: "0091 98765 43210", want: "+919876543210"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewIdentifier(IdentifierPhone, tt.raw)
			if err != nil {
				t.Fatalf("NewIdentifier(phone, %q) error: %v", tt.raw, err)
			}
			if got.Type != IdentifierPhone || got.Value != tt.want {
				t.Fatalf("NewIdentifier(phone, %q) = %+v, want %s", tt.raw, got, tt.want)
			}
		})
	}
}

func TestNewIdentifierPhoneRejectsInvalid(t *testing.T) {
	for _, raw := range []string{"", "15550123", "+1a555", "+", "+123", "+12345678901234567"} {
		if _, err := NewIdentifier(IdentifierPhone, raw); err == nil {
			t.Errorf("NewIdentifier(phone, %q) expected error", raw)
		}
	}
}

func TestNewIdentifierEmail(t *testing.T) {
	got, err := NewIdentifier(IdentifierEmail, "  Aya@Example.COM ")
	if err != nil {
		t.Fatalf("NewIdentifier(email) error: %v", err)
	}
	if got.Value != "aya@example.com" {
		t.Fatalf("NewIdentifier(email) = %q, want lowercased+trimmed", got.Value)
	}
}

func TestNewIdentifierEmailRejectsInvalid(t *testing.T) {
	for _, raw := range []string{"", "not-an-email", "a@", "@x.com", "a@b", "a b@c.com"} {
		if _, err := NewIdentifier(IdentifierEmail, raw); err == nil {
			t.Errorf("NewIdentifier(email, %q) expected error", raw)
		}
	}
}

func TestNewIdentifierRejectsUnknownType(t *testing.T) {
	if _, err := NewIdentifier("username", "aya"); err == nil {
		t.Fatal("NewIdentifier(username, ...) expected error")
	}
}

func TestValidatePassword(t *testing.T) {
	tests := []struct {
		name       string
		password   string
		identifier string
		wantErr    string
	}{
		{name: "valid", password: "correct horse 42", identifier: "+15550123"},
		{name: "too short", password: "short1", identifier: "+15550123", wantErr: "too_short"},
		{name: "too long", password: string(make([]byte, 1025)), identifier: "+15550123", wantErr: "too_long"},
		{name: "contains identifier", password: "pwd+15550123xyz", identifier: "+15550123", wantErr: "contains_identifier"},
		{name: "common value", password: "password", identifier: "+15550123", wantErr: "too_common"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePassword(tt.password, tt.identifier)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidatePassword() unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidatePassword() expected error %q", tt.wantErr)
			}
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Field != "password" || ve.Reason != tt.wantErr {
				t.Fatalf("ValidatePassword() error = %v, want field=password reason=%q", err, tt.wantErr)
			}
		})
	}
}

func TestHashOpaqueToken(t *testing.T) {
	h := HashOpaqueToken("rt_secret")
	if len(h) != 64 {
		t.Fatalf("HashOpaqueToken() length = %d, want 64 hex chars", len(h))
	}
	if HashOpaqueToken("rt_secret") != h {
		t.Fatal("HashOpaqueToken() not deterministic")
	}
}

func TestIsOpaqueTokenShape(t *testing.T) {
	valid := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xAB}, 32))
	if !IsOpaqueTokenShape(valid) {
		t.Error("valid opaque token shape rejected")
	}
	for _, bad := range []string{"", "short", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa==", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa!"} {
		if IsOpaqueTokenShape(bad) {
			t.Errorf("invalid opaque token %q accepted", bad)
		}
	}
}
