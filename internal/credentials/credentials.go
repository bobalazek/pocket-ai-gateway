package credentials

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argonMemory     = 19 * 1024
	argonIterations = 2
	argonParallel   = 1
	argonKeyLength  = 32
)

func RandomToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("create random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func Verifier(token string) [sha256.Size]byte {
	return sha256.Sum256([]byte(token))
}

func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("create password salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, argonIterations, argonMemory, argonParallel, argonKeyLength)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonIterations, argonParallel,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" || parts[3] != fmt.Sprintf("m=%d,t=%d,p=%d", argonMemory, argonIterations, argonParallel) {
		return false, errors.New("stored password hash uses unsupported parameters")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) != 16 {
		return false, errors.New("stored password hash has invalid salt")
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(expected) != argonKeyLength {
		return false, errors.New("stored password hash has invalid value")
	}
	actual := argon2.IDKey([]byte(password), salt, argonIterations, argonMemory, argonParallel, argonKeyLength)
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}
