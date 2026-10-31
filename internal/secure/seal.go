package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

// Secrets stored in the database (third-party API secrets an admin enters in
// the UI) are sealed with AES-256-GCM. The key is derived with HKDF from the
// server's SESSION_SECRET and a purpose label, so one master secret serves
// every use without ever being used directly. The associated data binds each
// ciphertext to its row (e.g. the workspace id): a value copied into another
// row does not open.
//
// Rotating SESSION_SECRET makes sealed values unreadable; Open then returns
// ErrUnsealable and the admin enters the secret again.

const sealVersion = "v1."

// ErrUnsealable means the value was tampered with, moved to another row, or
// sealed under a different SESSION_SECRET.
var ErrUnsealable = errors.New("secure: sealed value cannot be opened")

func sealAEAD(master []byte, purpose string) (cipher.AEAD, error) {
	if len(master) < 32 {
		return nil, errors.New("secure: master key too short")
	}
	key, err := hkdf.Key(sha256.New, master, nil, "hilo at-rest v1 "+purpose, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal encrypts plaintext for purpose, bound to aad. The result is printable.
func Seal(master []byte, purpose, aad, plaintext string) (string, error) {
	aead, err := sealAEAD(master, purpose)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	_, _ = rand.Read(nonce) // crypto/rand.Read never fails
	out := aead.Seal(nonce, nonce, []byte(plaintext), []byte(aad))
	return sealVersion + base64.RawURLEncoding.EncodeToString(out), nil
}

// Open reverses Seal with the same purpose and aad.
func Open(master []byte, purpose, aad, sealed string) (string, error) {
	aead, err := sealAEAD(master, purpose)
	if err != nil {
		return "", err
	}
	body, ok := strings.CutPrefix(sealed, sealVersion)
	if !ok {
		return "", ErrUnsealable
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil || len(raw) < aead.NonceSize()+aead.Overhead() {
		return "", ErrUnsealable
	}
	n := aead.NonceSize()
	plain, err := aead.Open(nil, raw[:n], raw[n:], []byte(aad))
	if err != nil {
		return "", ErrUnsealable
	}
	return string(plain), nil
}
