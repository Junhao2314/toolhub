package security

import (
	"crypto/rand"
	"errors"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
)

type Cipher struct{ key []byte }

func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != chacha20poly1305.KeySize {
		return nil, fmt.Errorf("master key must be %d bytes", chacha20poly1305.KeySize)
	}
	return &Cipher{key: append([]byte(nil), key...)}, nil
}

func (c *Cipher) Encrypt(plaintext []byte, associatedData string) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(c.key)
	if err != nil {
		return nil, fmt.Errorf("initialize cipher: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	sealed := aead.Seal(nil, nonce, plaintext, []byte(associatedData))
	result := make([]byte, 1+len(nonce)+len(sealed))
	result[0] = 1
	copy(result[1:], nonce)
	copy(result[1+len(nonce):], sealed)
	return result, nil
}

func (c *Cipher) Decrypt(ciphertext []byte, associatedData string) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(c.key)
	if err != nil {
		return nil, fmt.Errorf("initialize cipher: %w", err)
	}
	if len(ciphertext) < 1+aead.NonceSize()+aead.Overhead() || ciphertext[0] != 1 {
		return nil, errors.New("invalid encrypted value")
	}
	nonce := ciphertext[1 : 1+aead.NonceSize()]
	plaintext, err := aead.Open(nil, nonce, ciphertext[1+aead.NonceSize():], []byte(associatedData))
	if err != nil {
		return nil, errors.New("decrypt encrypted value")
	}
	return plaintext, nil
}
