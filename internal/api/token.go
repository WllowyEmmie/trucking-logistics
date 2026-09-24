package api

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
)

func newTrackingToken() (string, error) {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	return strings.ToLower(enc), nil
}