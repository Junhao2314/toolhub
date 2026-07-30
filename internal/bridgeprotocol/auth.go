package bridgeprotocol

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Junhao2314/toolhub/internal/security"
)

const (
	HeaderTimestamp      = "X-ToolHub-Timestamp"
	HeaderNonce          = "X-ToolHub-Nonce"
	HeaderSignature      = "X-ToolHub-Signature"
	HeaderIdempotencyKey = "Idempotency-Key"
)

func SigningBytes(method, requestURI, timestamp, nonce string, body []byte) []byte {
	sum := sha256.Sum256(body)
	return []byte(strings.ToUpper(method) + "\n" + requestURI + "\n" + timestamp + "\n" + nonce + "\n" + hex.EncodeToString(sum[:]))
}

func SignRequest(request *http.Request, key, body []byte, now time.Time, nonce string) {
	timestamp := strconv.FormatInt(now.UTC().Unix(), 10)
	request.Header.Set(HeaderTimestamp, timestamp)
	request.Header.Set(HeaderNonce, nonce)
	request.Header.Set(HeaderSignature, security.SignPayload(key, SigningBytes(request.Method, request.URL.RequestURI(), timestamp, nonce, body)))
}

func VerifyRequest(request *http.Request, key, body []byte, now time.Time, maxSkew time.Duration) error {
	timestamp := request.Header.Get(HeaderTimestamp)
	nonce := request.Header.Get(HeaderNonce)
	signature := request.Header.Get(HeaderSignature)
	if timestamp == "" || nonce == "" || signature == "" {
		return &APIError{Code: ErrAuthentication, Message: "Bridge authentication headers are required"}
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return &APIError{Code: ErrAuthentication, Message: "Bridge timestamp is invalid"}
	}
	requestTime := time.Unix(seconds, 0)
	if delta := now.UTC().Sub(requestTime); delta > maxSkew || delta < -maxSkew {
		return &APIError{Code: ErrExpiredRequest, Message: "Bridge request timestamp is outside the allowed window"}
	}
	if len(nonce) < 16 || len(nonce) > 128 {
		return &APIError{Code: ErrAuthentication, Message: "Bridge nonce is invalid"}
	}
	if !security.VerifyPayload(key, SigningBytes(request.Method, request.URL.RequestURI(), timestamp, nonce, body), signature) {
		return &APIError{Code: ErrAuthentication, Message: "Bridge request signature is invalid"}
	}
	return nil
}

func ParseKey(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if len(value) == 64 {
		decoded, err := hex.DecodeString(value)
		if err == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}
	if len(value) == 32 {
		return []byte(value), nil
	}
	return nil, fmt.Errorf("Bridge HMAC key must be 32 raw bytes or 64 hexadecimal characters")
}
