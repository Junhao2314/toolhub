package security

import (
	"errors"
	"regexp"
	"strings"
)

var usernamePattern = regexp.MustCompile(`^[a-z0-9._-]{3,32}$`)

func NormalizeUsername(value string) (string, error) {
	username := strings.ToLower(strings.TrimSpace(value))
	if strings.Contains(username, "@") || !usernamePattern.MatchString(username) {
		return "", errors.New("username must be 3-32 lowercase letters, numbers, dots, underscores, or hyphens")
	}
	return username, nil
}

func GenerateTemporaryPassword() (string, error) {
	return RandomToken(24)
}
