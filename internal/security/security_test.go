package security

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestPasswordRoundTrip(t *testing.T) {
	encoded, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := VerifyPassword(encoded, "correct horse battery staple")
	if err != nil || !ok {
		t.Fatalf("valid password rejected: ok=%v err=%v", ok, err)
	}
	ok, err = VerifyPassword(encoded, "wrong password")
	if err != nil || ok {
		t.Fatalf("invalid password accepted: ok=%v err=%v", ok, err)
	}
}

func TestCipherUsesAuthenticatedContext(t *testing.T) {
	cipher, err := NewCipher(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := cipher.Encrypt([]byte("secret"), "secret-id")
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := cipher.Decrypt(ciphertext, "secret-id")
	if err != nil || string(plaintext) != "secret" {
		t.Fatalf("round trip failed: %q %v", plaintext, err)
	}
	if _, err := cipher.Decrypt(ciphertext, "other-id"); err == nil {
		t.Fatal("ciphertext decrypted under the wrong associated context")
	}
}

func TestRedactionIsRecursive(t *testing.T) {
	redacted := RedactMap(map[string]any{
		"name":          "provider",
		"config":        map[string]any{"api_key": "sk-sensitive", "url": "https://example.com"},
		"authorization": "Bearer sensitive",
	})
	encodedBytes, err := json.Marshal(redacted)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(RedactJSON(encodedBytes))
	if strings.Contains(encoded, "sensitive") || strings.Contains(encoded, "sk-") {
		t.Fatalf("secret leaked: %s", encoded)
	}
}
