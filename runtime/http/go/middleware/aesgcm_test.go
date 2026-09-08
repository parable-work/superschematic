package middleware

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func aesGCMEncrypt(t *testing.T, key, iv, plaintext []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	require.NoError(t, err)
	gcm, err := cipher.NewGCM(block)
	require.NoError(t, err)
	return gcm.Seal(nil, iv, plaintext, nil)
}

func TestDecryptAESGCM(t *testing.T) {
	validKey := make([]byte, 32)
	_, err := rand.Read(validKey)
	require.NoError(t, err)

	validIV := make([]byte, 12)
	_, err = rand.Read(validIV)
	require.NoError(t, err)

	tests := []struct {
		name       string
		key        []byte
		iv         []byte
		ciphertext func() []byte
		wantErr    error
		wantPlain  string
	}{
		{
			name: "valid JSON payload",
			key:  validKey,
			iv:   validIV,
			ciphertext: func() []byte {
				return aesGCMEncrypt(t, validKey, validIV, []byte(`{"key":"value"}`))
			},
			wantPlain: `{"key":"value"}`,
		},
		{
			name: "empty plaintext round-trips",
			key:  validKey,
			iv:   validIV,
			ciphertext: func() []byte {
				return aesGCMEncrypt(t, validKey, validIV, []byte(""))
			},
			wantPlain: "",
		},
		{
			name: "large payload exceeding RSA limit",
			key:  validKey,
			iv:   validIV,
			ciphertext: func() []byte {
				payload := make([]byte, 2048)
				_, readErr := rand.Read(payload)
				require.NoError(t, readErr)
				return aesGCMEncrypt(t, validKey, validIV, payload)
			},
		},
		{
			name:       "rejects 16-byte key (AES-128)",
			key:        make([]byte, 16),
			iv:         validIV,
			ciphertext: func() []byte { return []byte("dummy") },
			wantErr:    ErrInvalidAESKeySize,
		},
		{
			name:       "rejects 0-byte key",
			key:        []byte{},
			iv:         validIV,
			ciphertext: func() []byte { return []byte("dummy") },
			wantErr:    ErrInvalidAESKeySize,
		},
		{
			name:       "rejects 16-byte IV",
			key:        validKey,
			iv:         make([]byte, 16),
			ciphertext: func() []byte { return []byte("dummy") },
			wantErr:    ErrInvalidIVSize,
		},
		{
			name: "tampered ciphertext fails authentication",
			key:  validKey,
			iv:   validIV,
			ciphertext: func() []byte {
				ct := aesGCMEncrypt(t, validKey, validIV, []byte("original"))
				ct[0] ^= 0xFF
				return ct
			},
			wantErr: ErrAESGCMDecrypt,
		},
		{
			name: "wrong key fails authentication",
			key: func() []byte {
				k := make([]byte, 32)
				_, readErr := rand.Read(k)
				require.NoError(t, readErr)
				return k
			}(),
			iv: validIV,
			ciphertext: func() []byte {
				return aesGCMEncrypt(t, validKey, validIV, []byte("test"))
			},
			wantErr: ErrAESGCMDecrypt,
		},
		{
			name: "truncated auth tag fails",
			key:  validKey,
			iv:   validIV,
			ciphertext: func() []byte {
				ct := aesGCMEncrypt(t, validKey, validIV, []byte("test"))
				return ct[:len(ct)-4]
			},
			wantErr: ErrAESGCMDecrypt,
		},
		{
			name:       "nil key",
			key:        nil,
			iv:         validIV,
			ciphertext: func() []byte { return []byte("dummy") },
			wantErr:    ErrInvalidAESKeySize,
		},
		{
			name:       "nil IV",
			key:        validKey,
			iv:         nil,
			ciphertext: func() []byte { return []byte("dummy") },
			wantErr:    ErrInvalidIVSize,
		},
		{
			name:       "nil ciphertext fails authentication",
			key:        validKey,
			iv:         validIV,
			ciphertext: func() []byte { return nil },
			wantErr:    ErrAESGCMDecrypt,
		},
		{
			name:       "rejects 24-byte key (AES-192)",
			key:        make([]byte, 24),
			iv:         validIV,
			ciphertext: func() []byte { return []byte("dummy") },
			wantErr:    ErrInvalidAESKeySize,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plaintext, decErr := DecryptAESGCM(tt.key, tt.iv, tt.ciphertext())
			if tt.wantErr != nil {
				require.ErrorIs(t, decErr, tt.wantErr)
				return
			}
			require.NoError(t, decErr)
			if tt.wantPlain != "" {
				assert.Equal(t, tt.wantPlain, string(plaintext))
			}
		})
	}
}

func TestDecryptAESGCM_ConcurrentSafety(t *testing.T) {
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)

	iv := make([]byte, 12)
	_, err = rand.Read(iv)
	require.NoError(t, err)

	plaintext := []byte(`{"concurrent":"test"}`)
	ciphertext := aesGCMEncrypt(t, key, iv, plaintext)

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			result, decErr := DecryptAESGCM(key, iv, ciphertext)
			assert.NoError(t, decErr)
			assert.Equal(t, plaintext, result)
		}()
	}

	wg.Wait()
}
