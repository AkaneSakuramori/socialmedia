package domain

import (
	"encoding/json"
	"time"
)

// CredentialMethod is a login method stored in user_credentials
// (DATABASE.md §4.3).
type CredentialMethod string

const (
	MethodPassword CredentialMethod = "password"
	MethodPasskey  CredentialMethod = "passkey"
	MethodOAuth    CredentialMethod = "oauth"
)

// Credential is one verifiable login method for a user (DATABASE.md §4.3).
// Data holds the method-specific material (e.g. {"hash": "<argon2id phc>"}).
type Credential struct {
	ID        int64
	UserID    int64
	Method    CredentialMethod
	Provider  *string
	Data      []byte // raw JSONB payload
	RevokedAt *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// PasswordCredentialData is the JSONB payload of a password credential.
type PasswordCredentialData struct {
	Hash string `json:"hash"`
}

// NewPasswordCredential builds a password-method credential for a user. It
// returns an error only if the credential payload cannot be marshaled (never in
// practice, since it is a fixed shape).
func NewPasswordCredential(id, userID int64, hash PasswordHash, now time.Time) (*Credential, error) {
	data, err := json.Marshal(PasswordCredentialData{Hash: hash.String()})
	if err != nil {
		return nil, err
	}
	return &Credential{
		ID:        id,
		UserID:    userID,
		Method:    MethodPassword,
		Data:      data,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}
