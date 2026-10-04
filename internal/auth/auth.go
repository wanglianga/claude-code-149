package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

type Claims struct {
	Sub     int64  `json:"sub"`
	Kind    string `json:"kind"` // member / staff
	Role    string `json:"role,omitempty"`
	VenueID int64  `json:"venue,omitempty"`
	Name    string `json:"name,omitempty"`
}

func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

func sign(secret, payloadB64 string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(payloadB64))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func Issue(secret string, c Claims) string {
	b, _ := json.Marshal(c)
	p := base64.RawURLEncoding.EncodeToString(b)
	return p + "." + sign(secret, p)
}

func Parse(secret, token string) (Claims, error) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return Claims{}, fmt.Errorf("malformed token")
	}
	want := sign(secret, parts[0])
	if !hmac.Equal([]byte(want), []byte(parts[1])) {
		return Claims{}, fmt.Errorf("bad signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Claims{}, err
	}
	var c Claims
	if err := json.Unmarshal(raw, &c); err != nil {
		return Claims{}, err
	}
	return c, nil
}
