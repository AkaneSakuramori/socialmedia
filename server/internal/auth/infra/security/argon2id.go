// Package security implements the auth module's cryptographic adapters:
// Argon2id password hashing (SECURITY_SPEC.md PASS-1) and the JWT/refresh token
// factory (ARCHITECTURE.md §10.2, SECURITY_SPEC.md §5–§6).
package security

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	"golang.org/x/crypto/argon2"
)

// Argon2Params configures the Argon2id KDF (SECURITY_SPEC.md PASS-1). Memory
// is in KiB; production defaults live in config (64 MiB, t=3, p=4).
type Argon2Params struct {
	Memory      uint32 // KiB
	Iterations  uint32
	Parallelism uint8
	SaltLen     uint32
	KeyLen      uint32
}

// DefaultArgon2Params matches the OWASP-recommended floor for interactive logins.
func DefaultArgon2Params() Argon2Params {
	return Argon2Params{Memory: 64 * 1024, Iterations: 3, Parallelism: 4, SaltLen: 16, KeyLen: 32}
}

// Argon2ID hashes and verifies passwords with Argon2id.
type Argon2ID struct {
	params Argon2Params
}

// NewArgon2ID builds a hasher with explicit parameters. Passwords are hashed
// with a unique random salt and serialized as a PHC string:
//
//	$argon2id$v=19$m=<kib>,t=<iterations>,p=<parallelism>$<salt>$<hash>
func NewArgon2ID(params Argon2Params) (*Argon2ID, error) {
	if params.Memory < 8*uint32(params.Parallelism) {
		return nil, errors.New("argon2id: memory must be >= 8 * parallelism (KiB)")
	}
	if params.Iterations < 1 {
		return nil, errors.New("argon2id: iterations must be >= 1")
	}
	if params.Parallelism < 1 || params.Parallelism > 255 {
		return nil, errors.New("argon2id: parallelism must be in [1, 255]")
	}
	if params.SaltLen < 8 {
		return nil, errors.New("argon2id: salt length must be >= 8")
	}
	if params.KeyLen < 16 {
		return nil, errors.New("argon2id: key length must be >= 16")
	}
	return &Argon2ID{params: params}, nil
}

// Hash derives a salted Argon2id PHC hash for a plaintext password.
func (a *Argon2ID) Hash(_ context.Context, plaintext string) (domain.PasswordHash, error) {
	if plaintext == "" {
		return domain.PasswordHash{}, errors.New("argon2id: refusing to hash empty password")
	}
	salt := make([]byte, a.params.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return domain.PasswordHash{}, fmt.Errorf("argon2id: read salt: %w", err)
	}
	key := argon2.IDKey([]byte(plaintext), salt, a.params.Iterations, a.params.Memory, a.params.Parallelism, a.params.KeyLen)

	b64 := base64.RawStdEncoding
	phc := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, a.params.Memory, a.params.Iterations, a.params.Parallelism,
		b64.EncodeToString(salt), b64.EncodeToString(key))
	return domain.NewPasswordHash(phc), nil
}

// Verify checks a plaintext password against a stored PHC hash with a
// constant-time comparison. Parameters are read from the hash itself, so
// verification survives future parameter changes.
func (a *Argon2ID) Verify(_ context.Context, hash domain.PasswordHash, plaintext string) (bool, error) {
	phc := hash.String()
	if phc == "" {
		return false, errors.New("argon2id: empty stored hash")
	}
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("argon2id: malformed PHC string")
	}

	params, err := parsePHCParams(parts[3])
	if err != nil {
		return false, err
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("argon2id: decode salt: %w", err)
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("argon2id: decode hash: %w", err)
	}

	got := argon2.IDKey([]byte(plaintext), salt, params.Iterations, params.Memory, params.Parallelism, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func parsePHCParams(s string) (Argon2Params, error) {
	var p Argon2Params
	for _, pair := range strings.Split(s, ",") {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			return p, errors.New("argon2id: malformed PHC params")
		}
		switch kv[0] {
		case "m":
			if _, err := fmt.Sscanf(kv[1], "%d", &p.Memory); err != nil {
				return p, errors.New("argon2id: malformed m param")
			}
		case "t":
			if _, err := fmt.Sscanf(kv[1], "%d", &p.Iterations); err != nil {
				return p, errors.New("argon2id: malformed t param")
			}
		case "p":
			var pl int
			if _, err := fmt.Sscanf(kv[1], "%d", &pl); err != nil || pl < 1 || pl > 255 {
				return p, errors.New("argon2id: malformed p param")
			}
			p.Parallelism = uint8(pl)
		}
	}
	if p.Memory == 0 || p.Iterations == 0 || p.Parallelism == 0 {
		return p, errors.New("argon2id: incomplete PHC params")
	}
	return p, nil
}
