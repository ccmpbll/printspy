// Package auth provides password hashing and session tokens for PrintSpy's
// login system.
package auth

import (
	"crypto/rand"
	"encoding/base32"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	CookieName      = "printspy_session"
	SessionDuration = 30 * 24 * time.Hour
)

func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func NewSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), nil
}
