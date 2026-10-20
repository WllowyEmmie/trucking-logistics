// Package auth holds the pieces of login that don't need a database:
// hashing passwords and PINs, and making and hashing session tokens.
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// Iterations for PBKDF2-SHA256, OWASP's 2023 figure. Kept in the hash, so it
// can be raised later without breaking existing hashes.
const iterations = 600_000

const keyLen = 32

var b64 = base64.RawStdEncoding

// HashSecret hashes a password or PIN for storage:
// "pbkdf2-sha256$<iterations>$<salt>$<key>".
func HashSecret(secret string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, secret, salt, iterations, keyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", iterations, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// CheckSecret reports whether secret matches a hash made by HashSecret.
// A malformed hash never matches.
func CheckSecret(hash, secret string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 {
		return false
	}
	salt, err1 := b64.DecodeString(parts[2])
	want, err2 := b64.DecodeString(parts[3])
	if err1 != nil || err2 != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, secret, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash is checked against when a login names nobody, so a wrong email
// takes as long as a wrong password and doesn't reveal who has an account.
var dummyHash, _ = HashSecret("not a real password")

// SpendTime does the work of one CheckSecret and throws the answer away.
func SpendTime(secret string) { CheckSecret(dummyHash, secret) }

// NewToken returns a session token for the client and the hash to store.
// Only the hash is kept, so a leaked sessions table logs nobody in.
func NewToken() (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

// HashToken is how a presented token is looked up.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// SameKey compares a presented service key with the configured one in
// constant time. An empty configured key matches nothing.
func SameKey(got, want string) bool {
	if want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// NormPhone keeps digits and a leading +, the form drivers are matched on.
func NormPhone(s string) string {
	var b strings.Builder
	for i, r := range s {
		if (r >= '0' && r <= '9') || (r == '+' && i == 0) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ValidPIN: 4 to 8 digits.
func ValidPIN(pin string) error {
	if len(pin) < 4 || len(pin) > 8 {
		return errors.New("PIN must be 4 to 8 digits")
	}
	for _, r := range pin {
		if r < '0' || r > '9' {
			return errors.New("PIN must be digits only")
		}
	}
	return nil
}

// ValidPassword: at least 10 characters. Length beats composition rules.
func ValidPassword(pw string) error {
	if len(pw) < 10 {
		return errors.New("password must be at least 10 characters")
	}
	return nil
}

// GeneratePIN returns a random 6-digit PIN (leading zeros kept), uniform
// over 000000-999999 from the system's secure random source. Six digits,
// not four: with sign-in capped at twenty tries a day, guessing one takes
// over a hundred years on average.
func GeneratePIN() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}
