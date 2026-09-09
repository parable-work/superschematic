package middleware

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
)

var (
	ErrInvalidAESKeySize = errors.New("AES key must be 32 bytes (AES-256)")
	ErrInvalidIVSize     = errors.New("IV must be 12 bytes for AES-GCM")
	ErrAESGCMDecrypt     = errors.New("AES-GCM decryption failed: ciphertext is invalid or has been tampered with")
)

// DecryptAESGCM decrypts ciphertext encrypted with AES-256-GCM.
// The key must be exactly 32 bytes (AES-256) and the IV must be exactly
// 12 bytes (standard GCM nonce). The ciphertext must include the 128-bit
// GCM authentication tag appended by the encryptor (WebCrypto default).
func DecryptAESGCM(key, iv, ciphertext []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("%w: got %d bytes", ErrInvalidAESKeySize, len(key))
	}
	if len(iv) != 12 {
		return nil, fmt.Errorf("%w: got %d bytes", ErrInvalidIVSize, len(iv))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	plaintext, err := gcm.Open(nil, iv, ciphertext, nil)
	if err != nil {
		return nil, ErrAESGCMDecrypt
	}

	return plaintext, nil
}
