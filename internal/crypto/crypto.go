// Package crypto provides AES-256-GCM encryption for secret settings values.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"strings"
)

const (
	// EncryptedPrefix marks values that have been encrypted.
	EncryptedPrefix = "enc::"

	// EnvEncryptionKey is the environment variable name for the encryption key.
	EnvEncryptionKey = "SETTINGS_ENCRYPTION_KEY"
)

// deriveKey derives a 32-byte AES-256 key from an arbitrary passphrase using SHA-256.
func deriveKey(passphrase string) []byte {
	h := sha256.Sum256([]byte(passphrase))
	return h[:]
}

// getKey reads the encryption key from the environment.
// Returns empty string if not set (encryption disabled).
func getKey() string {
	return os.Getenv(EnvEncryptionKey)
}

// Enabled returns true if the encryption key is configured.
func Enabled() bool {
	return getKey() != ""
}

// Encrypt encrypts plaintext using AES-256-GCM and returns a base64-encoded
// ciphertext prefixed with "enc::". Returns the plaintext unchanged if no
// encryption key is configured.
func Encrypt(plaintext string) (string, error) {
	key := getKey()
	if key == "" {
		return plaintext, nil
	}

	block, err := aes.NewCipher(deriveKey(key))
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return EncryptedPrefix + base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts a value that was encrypted with Encrypt. If the value does
// not have the "enc::" prefix, it is returned as-is (backward compatibility
// with pre-encryption values). Returns an error if decryption fails.
func Decrypt(value string) (string, error) {
	if !strings.HasPrefix(value, EncryptedPrefix) {
		return value, nil
	}

	key := getKey()
	if key == "" {
		return "", errors.New("encrypted value found but SETTINGS_ENCRYPTION_KEY is not set")
	}

	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, EncryptedPrefix))
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(deriveKey(key))
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("ciphertext too short")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}

	return string(plaintext), nil
}
