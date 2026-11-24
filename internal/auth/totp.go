package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"time"
)

// TOTP as authenticator apps implement it (RFC 6238): HMAC-SHA1, six digits,
// 30-second steps. Anything else and half the apps silently ignore the
// parameters and produce codes that never match.
const (
	totpPeriod = 30
	totpDigits = 6
	// totpSkew is how many steps either side of "now" are accepted, to absorb
	// a phone clock that is half a minute off.
	totpSkew = 1
)

var base32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns 20 random bytes — the size of a SHA-1 block's output,
// which is what RFC 4226 recommends for the shared secret.
func NewTOTPSecret() ([]byte, error) {
	secret := make([]byte, 20)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	return secret, nil
}

// EncodeTOTPSecret is the form a person types into an authenticator app.
func EncodeTOTPSecret(secret []byte) string {
	return base32NoPad.EncodeToString(secret)
}

// TOTPURL is the otpauth:// link an authenticator app reads from a QR code.
func TOTPURL(secret []byte, issuer, account string) string {
	query := url.Values{}
	query.Set("secret", EncodeTOTPSecret(secret))
	query.Set("issuer", issuer)
	query.Set("algorithm", "SHA1")
	query.Set("digits", fmt.Sprint(totpDigits))
	query.Set("period", fmt.Sprint(totpPeriod))
	label := url.PathEscape(issuer + ":" + account)
	return "otpauth://totp/" + label + "?" + query.Encode()
}

func totpCode(secret []byte, step uint64) string {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], step)
	mac := hmac.New(sha1.New, secret)
	mac.Write(counter[:])
	sum := mac.Sum(nil)

	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, value%1_000_000)
}

// VerifyTOTP checks code against the steps around now and returns the step it
// matched, so the caller can refuse to accept the same code twice.
func VerifyTOTP(secret []byte, code string, now time.Time) (step uint64, ok bool) {
	if len(code) != totpDigits {
		return 0, false
	}
	current := uint64(now.Unix()) / totpPeriod
	for delta := -totpSkew; delta <= totpSkew; delta++ {
		candidate := uint64(int64(current) + int64(delta))
		if subtle.ConstantTimeCompare([]byte(totpCode(secret, candidate)), []byte(code)) == 1 {
			return candidate, true
		}
	}
	return 0, false
}
