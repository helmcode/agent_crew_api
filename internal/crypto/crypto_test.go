package crypto

import (
	"os"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	os.Setenv(EnvEncryptionKey, "test-secret-key-32bytes!")
	defer os.Unsetenv(EnvEncryptionKey)

	plaintext := "sk-ant-api03-very-secret-key"
	encrypted, err := Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	if encrypted == plaintext {
		t.Fatal("encrypted value should differ from plaintext")
	}

	if encrypted[:len(EncryptedPrefix)] != EncryptedPrefix {
		t.Fatalf("encrypted value should start with %q, got %q", EncryptedPrefix, encrypted[:10])
	}

	decrypted, err := Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}

	if decrypted != plaintext {
		t.Fatalf("expected %q, got %q", plaintext, decrypted)
	}
}

func TestEncryptWithoutKey(t *testing.T) {
	os.Unsetenv(EnvEncryptionKey)

	plaintext := "not-encrypted"
	result, err := Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}
	if result != plaintext {
		t.Fatalf("without key, Encrypt should return plaintext; got %q", result)
	}
}

func TestDecryptPlaintextPassthrough(t *testing.T) {
	os.Setenv(EnvEncryptionKey, "some-key")
	defer os.Unsetenv(EnvEncryptionKey)

	// Values without the enc:: prefix should pass through unchanged.
	plain := "plain-value"
	result, err := Decrypt(plain)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}
	if result != plain {
		t.Fatalf("expected %q, got %q", plain, result)
	}
}

func TestDecryptWithoutKeyReturnsError(t *testing.T) {
	os.Unsetenv(EnvEncryptionKey)

	_, err := Decrypt(EncryptedPrefix + "some-data")
	if err == nil {
		t.Fatal("expected error when decrypting without key")
	}
}

func TestEnabled(t *testing.T) {
	os.Unsetenv(EnvEncryptionKey)
	if Enabled() {
		t.Fatal("expected Enabled() to return false without key")
	}

	os.Setenv(EnvEncryptionKey, "my-key")
	defer os.Unsetenv(EnvEncryptionKey)
	if !Enabled() {
		t.Fatal("expected Enabled() to return true with key set")
	}
}

func TestEncryptProducesDifferentCiphertexts(t *testing.T) {
	os.Setenv(EnvEncryptionKey, "test-key")
	defer os.Unsetenv(EnvEncryptionKey)

	plaintext := "same-value"
	enc1, _ := Encrypt(plaintext)
	enc2, _ := Encrypt(plaintext)

	if enc1 == enc2 {
		t.Fatal("two encryptions of the same value should produce different ciphertexts (random nonce)")
	}

	// Both should still decrypt to the same value.
	dec1, _ := Decrypt(enc1)
	dec2, _ := Decrypt(enc2)
	if dec1 != plaintext || dec2 != plaintext {
		t.Fatal("both ciphertexts should decrypt to the original plaintext")
	}
}
