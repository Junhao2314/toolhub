package security

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
)

func RandomToken(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func TokenHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func SignPayload(key, payload []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func VerifyPayload(key, payload []byte, signature string) bool {
	want, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	return hmac.Equal(mac.Sum(nil), want)
}

func FingerprintSecretMap(key []byte, values map[string]string) string {
	keys := make([]string, 0, len(values))
	for name := range values {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	var canonical bytes.Buffer
	for _, name := range keys {
		writeFingerprintPart(&canonical, name)
		writeFingerprintPart(&canonical, values[name])
	}
	return SignPayload(key, canonical.Bytes())
}

func writeFingerprintPart(buffer *bytes.Buffer, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = buffer.Write(length[:])
	_, _ = buffer.WriteString(value)
}
