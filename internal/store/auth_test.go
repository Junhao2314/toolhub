package store

import (
	"strings"
	"testing"

	"github.com/Junhao2314/toolhub/internal/security"
)

func TestSingletonAccountUsesExistingPasswordBoundary(t *testing.T) {
	hash, err := security.HashPassword(strings.Repeat("safe-", 3))
	if err != nil {
		t.Fatal(err)
	}
	ok, err := security.VerifyPassword(hash, strings.Repeat("safe-", 3))
	if err != nil || !ok {
		t.Fatalf("Argon2id round trip failed: ok=%v err=%v", ok, err)
	}
}
