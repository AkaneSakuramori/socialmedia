package domain

import (
	"regexp"
	"strings"
)

// ValidationError is a field-level validation failure (ENGINEERING.md §14.2,
// §15). It maps to the errors[] array of the problem+json contract
// (API.md §2.5).
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return e.Field + ": " + e.Reason
}

// DisplayName max length per API.md §4.1/§5.2 (1–64 chars).
const displayNameMaxLen = 64

// DisplayName is the human name shown everywhere in the UI.
type DisplayName struct{ value string }

// NewDisplayName validates and normalizes a display name (1–64 chars).
func NewDisplayName(s string) (DisplayName, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return DisplayName{}, &ValidationError{Field: "display_name", Reason: "must_not_be_blank"}
	}
	if len([]rune(s)) > displayNameMaxLen {
		return DisplayName{}, &ValidationError{Field: "display_name", Reason: "too_long"}
	}
	return DisplayName{value: s}, nil
}

func (d DisplayName) String() string { return d.value }

// Username rules per API.md §4.1: 3–30 chars, [a-z0-9._], reserved-word check.
const (
	usernameMinLen = 3
	usernameMaxLen = 30
)

var usernameRe = regexp.MustCompile(`^[a-z0-9._]+$`)

// reservedUsernames can never be claimed; they are blocked at validation so the
// service never issues an account with an ambiguous or privileged handle.
var reservedUsernames = map[string]struct{}{
	"admin": {}, "administrator": {}, "support": {}, "help": {}, "system": {},
	"root": {}, "moderator": {}, "official": {}, "staff": {}, "team": {},
	"api": {}, "info": {}, "service": {}, "security": {}, "privacy": {},
}

// Username is the immutable, lowercase public handle.
type Username struct{ value string }

// NewUsername validates and normalizes a username (lowercase, 3–30,
// [a-z0-9._], not reserved).
func NewUsername(s string) (Username, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if n := len([]rune(s)); n < usernameMinLen || n > usernameMaxLen {
		return Username{}, &ValidationError{Field: "username", Reason: "length"}
	}
	if !usernameRe.MatchString(s) {
		return Username{}, &ValidationError{Field: "username", Reason: "invalid_chars"}
	}
	if strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") ||
		strings.HasPrefix(s, "_") || strings.HasSuffix(s, "_") {
		return Username{}, &ValidationError{Field: "username", Reason: "invalid_edges"}
	}
	if _, ok := reservedUsernames[s]; ok {
		return Username{}, &ValidationError{Field: "username", Reason: "reserved"}
	}
	return Username{value: s}, nil
}

func (u Username) String() string { return u.value }
