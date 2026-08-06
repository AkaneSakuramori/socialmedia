package security

import (
	"context"
	"strings"
	"testing"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
)

// lowCostParams keeps unit tests fast while exercising the real code path.
func lowCostParams() Argon2Params {
	return Argon2Params{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}
}

func TestArgon2IDHashAndVerify(t *testing.T) {
	h, err := NewArgon2ID(lowCostParams())
	if err != nil {
		t.Fatalf("NewArgon2ID error: %v", err)
	}
	ctx := context.Background()

	hash, err := h.Hash(ctx, "correct horse battery staple")
	if err != nil {
		t.Fatalf("Hash error: %v", err)
	}
	phc := hash.String()
	if !strings.HasPrefix(phc, "$argon2id$v=19$") {
		t.Errorf("unexpected PHC prefix: %q", phc)
	}
	if len(strings.Split(phc, "$")) != 6 {
		t.Errorf("PHC string must have 6 $ segments: %q", phc)
	}

	ok, err := h.Verify(ctx, hash, "correct horse battery staple")
	if err != nil {
		t.Fatalf("Verify error: %v", err)
	}
	if !ok {
		t.Error("Verify returned false for the correct password")
	}

	ok, err = h.Verify(ctx, hash, "wrong password")
	if err != nil {
		t.Fatalf("Verify error: %v", err)
	}
	if ok {
		t.Error("Verify returned true for an incorrect password")
	}
}

func TestArgon2IDHashIsSaltedPerUser(t *testing.T) {
	h, _ := NewArgon2ID(lowCostParams())
	ctx := context.Background()

	a, _ := h.Hash(ctx, "same password")
	b, _ := h.Hash(ctx, "same password")
	if a.String() == b.String() {
		t.Error("two hashes of the same password must differ (unique salts)")
	}
}

func TestArgon2IDVerifyParamDrift(t *testing.T) {
	h, _ := NewArgon2ID(lowCostParams())
	ctx := context.Background()

	// Hash with old (cheap) params, verify with a different instance — the
	// parameters must come from the stored hash, not the verifying config.
	hash, _ := h.Hash(ctx, "some password")
	upgraded, err := NewArgon2ID(Argon2Params{Memory: 16 * 1024, Iterations: 2, Parallelism: 2, SaltLen: 16, KeyLen: 32})
	if err != nil {
		t.Fatalf("NewArgon2ID error: %v", err)
	}
	ok, err := upgraded.Verify(ctx, hash, "some password")
	if err != nil || !ok {
		t.Fatalf("Verify after param drift = %v, %v; want true, nil", ok, err)
	}
}

func TestArgon2IDRejectsMalformed(t *testing.T) {
	h, _ := NewArgon2ID(lowCostParams())
	ctx := context.Background()

	for _, phc := range []string{"", "$argon2id$broken", "$bcrypt$..."} {
		if _, err := h.Verify(ctx, domain.NewPasswordHash(phc), "x"); err == nil {
			t.Errorf("Verify(%q) expected error", phc)
		}
	}
}

func TestArgon2IDRejectsEmptyPassword(t *testing.T) {
	h, _ := NewArgon2ID(lowCostParams())
	if _, err := h.Hash(context.Background(), ""); err == nil {
		t.Error("Hash of empty password should fail")
	}
}

func TestNewArgon2IDValidation(t *testing.T) {
	for name, p := range map[string]Argon2Params{
		"memory too low":   {Memory: 4, Iterations: 1, Parallelism: 4, SaltLen: 16, KeyLen: 32},
		"zero iterations":  {Memory: 64, Iterations: 0, Parallelism: 1, SaltLen: 16, KeyLen: 32},
		"parallelism zero": {Memory: 64, Iterations: 1, Parallelism: 0, SaltLen: 16, KeyLen: 32},
		"salt too short":   {Memory: 64, Iterations: 1, Parallelism: 1, SaltLen: 4, KeyLen: 32},
		"key too short":    {Memory: 64, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 8},
	} {
		if _, err := NewArgon2ID(p); err == nil {
			t.Errorf("NewArgon2ID(%s) expected error", name)
		}
	}
}
