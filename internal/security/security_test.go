package security

import (
	"bytes"
	"encoding/json"
	"regexp"
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

func TestNormalizeUsername(t *testing.T) {
	tests := []struct {
		input string
		want  string
		valid bool
	}{
		{input: " LiuJH.273 ", want: "liujh.273", valid: true},
		{input: "abc", want: "abc", valid: true},
		{input: "ab", valid: false},
		{input: "user@example.com", valid: false},
		{input: "contains space", valid: false},
		{input: strings.Repeat("a", 33), valid: false},
	}
	for _, test := range tests {
		got, err := NormalizeUsername(test.input)
		if test.valid && (err != nil || got != test.want) {
			t.Fatalf("NormalizeUsername(%q) = %q, %v; want %q", test.input, got, err, test.want)
		}
		if !test.valid && err == nil {
			t.Fatalf("NormalizeUsername(%q) unexpectedly succeeded", test.input)
		}
	}
}

func TestGenerateTemporaryPassword(t *testing.T) {
	password, err := GenerateTemporaryPassword()
	if err != nil {
		t.Fatal(err)
	}
	if len(password) < 24 || !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(password) {
		t.Fatalf("temporary password is not URL-safe or long enough: length=%d", len(password))
	}
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := VerifyPassword(hash, password)
	if err != nil || !valid {
		t.Fatalf("temporary password did not verify: valid=%v err=%v", valid, err)
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

func TestFingerprintSecretMapIsStableAndNodeScoped(t *testing.T) {
	valuesA := map[string]string{"TOKEN": "secret-value", "REGION": "test"}
	valuesB := map[string]string{"REGION": "test", "TOKEN": "secret-value"}
	first := FingerprintSecretMap(bytes.Repeat([]byte{1}, 32), valuesA)
	second := FingerprintSecretMap(bytes.Repeat([]byte{1}, 32), valuesB)
	if first != second {
		t.Fatalf("fingerprints depend on map order: %s != %s", first, second)
	}
	otherNode := FingerprintSecretMap(bytes.Repeat([]byte{2}, 32), valuesA)
	if first == otherNode {
		t.Fatal("fingerprint was reusable across node task keys")
	}
}
