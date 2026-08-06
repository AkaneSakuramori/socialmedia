// Package auth owns the authentication and session bounded context
// (ARCHITECTURE.md §9.1, §10; DATABASE.md §4.3–§4.4). Pure domain: credentials,
// sessions, identifiers, and the ports application/infra depend on.
package domain

import (
	"errors"
	"regexp"
	"strings"
)

// IdentifierType is the kind of primary identifier (API.md §4.1).
type IdentifierType string

const (
	IdentifierPhone IdentifierType = "phone"
	IdentifierEmail IdentifierType = "email"
)

// ValidationError is a field-level validation failure (shared vocabulary with
// user/domain, kept local to avoid cross-module coupling).
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Reason }

var (
	// ErrInvalidIdentifier means the value is not a valid E.164 phone or email.
	ErrInvalidIdentifier = errors.New("auth: invalid identifier")
	// ErrWeakPassword means a password failed the strength policy (PASS-2).
	ErrWeakPassword = errors.New("auth: weak password")
	// ErrOTPInvalid means the presented OTP is wrong (OTP-1).
	ErrOTPInvalid = errors.New("auth: otp invalid")
	// ErrOTPExpired means the OTP's 300 s TTL passed (OTP-1).
	ErrOTPExpired = errors.New("auth: otp expired")
)

// phoneRe matches ITU-T E.164: leading '+' then 7–15 digits (country code
// included), no spaces or punctuation.
var phoneRe = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)

// emailRe is a pragmatic RFC 5322 subset: local@domain.tld with a real dot
// domain; uppercase is normalized away by callers via NormalizeEmail.
var emailRe = regexp.MustCompile(`^[a-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

// Identifier is a normalized primary identifier (API.md §2.2, §4.1).
type Identifier struct {
	Type  IdentifierType
	Value string // E.164 for phone, lowercased for email
}

// NewIdentifier normalizes and validates an identifier of the given type.
func NewIdentifier(typ IdentifierType, raw string) (Identifier, error) {
	switch typ {
	case IdentifierPhone:
		v := NormalizePhone(raw)
		if !phoneRe.MatchString(v) {
			return Identifier{}, &ValidationError{Field: "identifier", Reason: "invalid_phone"}
		}
		return Identifier{Type: typ, Value: v}, nil
	case IdentifierEmail:
		v := NormalizeEmail(raw)
		if !emailRe.MatchString(v) {
			return Identifier{},
				&ValidationError{Field: "identifier", Reason: "invalid_email"}
		}
		return Identifier{Type: typ, Value: v}, nil
	default:
		return Identifier{}, &ValidationError{Field: "identifier_type", Reason: "unsupported"}
	}
}

// NormalizePhone strips visual separators from a phone and returns it in E.164
// shape with a leading '+'. Inputs without a leading '+' are returned unchanged
// (and will fail validation).
func NormalizePhone(raw string) string {
	s := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '(', ')', '.':
			return -1
		}
		return r
	}, strings.TrimSpace(raw))
	if strings.HasPrefix(s, "00") {
		return "+" + s[2:]
	}
	return s
}

// NormalizeEmail trims and lowercases an email address.
func NormalizeEmail(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}
