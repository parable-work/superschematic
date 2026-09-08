package middleware_test

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/runtime/http/go/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockDecryptor struct {
	fn func(ctx context.Context, ciphertext string) ([]byte, error)
}

func (m *mockDecryptor) Decrypt(ctx context.Context, ciphertext string) ([]byte, error) {
	return m.fn(ctx, ciphertext)
}

func envelopeBody(t *testing.T, v any) io.Reader {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return strings.NewReader(string(b))
}

func TestExtractSubdomain(t *testing.T) {
	tests := []struct {
		name       string
		host       string
		baseDomain string
		want       string
	}{
		{"standard subdomain", "acme.example.com", "example.com", "acme"},
		{"bare domain", "example.com", "example.com", ""},
		{"non-matching domain", "acme.other.com", "example.com", ""},
		{"host with port", "acme.example.com:8080", "example.com", "acme"},
		{"case insensitive", "ACME.Example.Com", "example.com", "acme"},
		{"multi-level subdomain", "sub.acme.example.com", "example.com", "sub.acme"},
		{"empty host", "", "example.com", ""},
		{"localhost bare", "localhost", "localhost", ""},
		{"localhost with port", "localhost:8080", "localhost", ""},
		{"partial suffix not a subdomain", "notexample.com", "example.com", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := middleware.ExtractSubdomain(tc.host, tc.baseDomain)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestDecryptPayloadMiddleware(t *testing.T) {
	plainJSON := `{"username":"admin","password":"secret"}`

	rsaDecryptor := &mockDecryptor{
		fn: func(_ context.Context, _ string) ([]byte, error) {
			return []byte(plainJSON), nil
		},
	}

	t.Run("nil decryptor returns 500", func(t *testing.T) {
		handler := middleware.DecryptPayloadMiddleware(nil, nopLoggerGetter)(okHandler())

		body := envelopeBody(t, map[string]string{"algorithm": "RSA_OAEP_256", "payload": "encrypted"})
		req := httptest.NewRequest(http.MethodPost, "/", body)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
		assert.Contains(t, w.Body.String(), "Server configuration error")
	})

	t.Run("invalid JSON body returns 400", func(t *testing.T) {
		handler := middleware.DecryptPayloadMiddleware(rsaDecryptor, nopLoggerGetter)(okHandler())

		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("not json"))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), "Invalid encrypted payload envelope")
	})

	t.Run("unsupported algorithm returns 400", func(t *testing.T) {
		handler := middleware.DecryptPayloadMiddleware(rsaDecryptor, nopLoggerGetter)(okHandler())

		body := envelopeBody(t, map[string]string{"algorithm": "ROT13", "payload": "encrypted"})
		req := httptest.NewRequest(http.MethodPost, "/", body)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), "Unsupported encryption algorithm")
	})

	t.Run("empty payload in RSA mode returns 400", func(t *testing.T) {
		handler := middleware.DecryptPayloadMiddleware(rsaDecryptor, nopLoggerGetter)(okHandler())

		body := envelopeBody(t, map[string]string{"algorithm": "RSA_OAEP_256"})
		req := httptest.NewRequest(http.MethodPost, "/", body)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), "Encrypted payload is required")
	})

	t.Run("decryption failure returns 400", func(t *testing.T) {
		failDecryptor := &mockDecryptor{
			fn: func(_ context.Context, _ string) ([]byte, error) {
				return nil, errors.New("decryption failed")
			},
		}
		handler := middleware.DecryptPayloadMiddleware(failDecryptor, nopLoggerGetter)(okHandler())

		body := envelopeBody(t, map[string]string{"algorithm": "RSA_OAEP_256", "payload": "encrypted"})
		req := httptest.NewRequest(http.MethodPost, "/", body)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), "Failed to decrypt request payload")
	})

	t.Run("non-JSON plaintext returns 400", func(t *testing.T) {
		nonJSONDecryptor := &mockDecryptor{
			fn: func(_ context.Context, _ string) ([]byte, error) {
				return []byte("not json"), nil
			},
		}
		handler := middleware.DecryptPayloadMiddleware(nonJSONDecryptor, nopLoggerGetter)(okHandler())

		body := envelopeBody(t, map[string]string{"algorithm": "RSA_OAEP_256", "payload": "encrypted"})
		req := httptest.NewRequest(http.MethodPost, "/", body)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), "Decrypted payload must be valid JSON")
	})

	t.Run("successful RSA decryption replaces body", func(t *testing.T) {
		var capturedBody string
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			capturedBody = string(b)
			w.WriteHeader(http.StatusOK)
		})

		handler := middleware.DecryptPayloadMiddleware(rsaDecryptor, nopLoggerGetter)(inner)

		body := envelopeBody(t, map[string]string{"algorithm": "RSA_OAEP_256", "payload": "encrypted"})
		req := httptest.NewRequest(http.MethodPost, "/", body)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, plainJSON, capturedBody)
	})

	t.Run("EncryptedBody backward compatibility", func(t *testing.T) {
		var capturedBody string
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			capturedBody = string(b)
			w.WriteHeader(http.StatusOK)
		})

		handler := middleware.DecryptPayloadMiddleware(rsaDecryptor, nopLoggerGetter)(inner)

		body := envelopeBody(t, map[string]string{"encryptedBody": "encrypted"})
		req := httptest.NewRequest(http.MethodPost, "/", body)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, plainJSON, capturedBody)
	})

	t.Run("empty algorithm defaults to RSA path", func(t *testing.T) {
		var capturedBody string
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			capturedBody = string(b)
			w.WriteHeader(http.StatusOK)
		})

		handler := middleware.DecryptPayloadMiddleware(rsaDecryptor, nopLoggerGetter)(inner)

		body := envelopeBody(t, map[string]string{"payload": "encrypted"})
		req := httptest.NewRequest(http.MethodPost, "/", body)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, plainJSON, capturedBody)
	})

	t.Run("NONE algorithm uses RSA path", func(t *testing.T) {
		var capturedBody string
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			capturedBody = string(b)
			w.WriteHeader(http.StatusOK)
		})

		handler := middleware.DecryptPayloadMiddleware(rsaDecryptor, nopLoggerGetter)(inner)

		body := envelopeBody(t, map[string]string{"algorithm": "NONE", "payload": "encrypted"})
		req := httptest.NewRequest(http.MethodPost, "/", body)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, plainJSON, capturedBody)
	})

	t.Run("successful hybrid AES-GCM decryption", func(t *testing.T) {
		aesKey := make([]byte, 32)
		_, err := rand.Read(aesKey)
		require.NoError(t, err)

		iv := make([]byte, 12)
		_, err = rand.Read(iv)
		require.NoError(t, err)

		block, err := aes.NewCipher(aesKey)
		require.NoError(t, err)
		gcm, err := cipher.NewGCM(block)
		require.NoError(t, err)
		ct := gcm.Seal(nil, iv, []byte(plainJSON), nil)

		hybridDecryptor := &mockDecryptor{
			fn: func(_ context.Context, _ string) ([]byte, error) {
				return aesKey, nil
			},
		}

		var capturedBody string
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, readErr := io.ReadAll(r.Body)
			require.NoError(t, readErr)
			capturedBody = string(b)
			w.WriteHeader(http.StatusOK)
		})

		handler := middleware.DecryptPayloadMiddleware(hybridDecryptor, nopLoggerGetter)(inner)

		envelope := map[string]string{
			"algorithm":    "AES_256_GCM_RSA_OAEP_256",
			"payload":      base64.StdEncoding.EncodeToString(ct),
			"encryptedKey": "wrapped-aes-key",
			"iv":           base64.StdEncoding.EncodeToString(iv),
		}
		req := httptest.NewRequest(http.MethodPost, "/", envelopeBody(t, envelope))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, plainJSON, capturedBody)
	})

	t.Run("hybrid missing encryptedKey returns 400", func(t *testing.T) {
		nopDecryptor := &mockDecryptor{
			fn: func(_ context.Context, _ string) ([]byte, error) {
				return nil, nil
			},
		}

		handler := middleware.DecryptPayloadMiddleware(nopDecryptor, nopLoggerGetter)(okHandler())

		envelope := map[string]string{
			"algorithm": "AES_256_GCM_RSA_OAEP_256",
			"payload":   base64.StdEncoding.EncodeToString([]byte("cipher")),
			"iv":        base64.StdEncoding.EncodeToString(make([]byte, 12)),
		}
		req := httptest.NewRequest(http.MethodPost, "/", envelopeBody(t, envelope))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), "Failed to decrypt request payload")
	})

	t.Run("hybrid key unwrap failure returns 400", func(t *testing.T) {
		failDecryptor := &mockDecryptor{
			fn: func(_ context.Context, _ string) ([]byte, error) {
				return nil, errors.New("KMS unavailable")
			},
		}

		handler := middleware.DecryptPayloadMiddleware(failDecryptor, nopLoggerGetter)(okHandler())

		envelope := map[string]string{
			"algorithm":    "AES_256_GCM_RSA_OAEP_256",
			"payload":      base64.StdEncoding.EncodeToString([]byte("cipher")),
			"encryptedKey": "wrapped-key",
			"iv":           base64.StdEncoding.EncodeToString(make([]byte, 12)),
		}
		req := httptest.NewRequest(http.MethodPost, "/", envelopeBody(t, envelope))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), "Failed to decrypt request payload")
	})

	t.Run("content length updated after decryption", func(t *testing.T) {
		var capturedLen int64
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedLen = r.ContentLength
			w.WriteHeader(http.StatusOK)
		})

		handler := middleware.DecryptPayloadMiddleware(rsaDecryptor, nopLoggerGetter)(inner)

		body := envelopeBody(t, map[string]string{"algorithm": "RSA_OAEP_256", "payload": "encrypted"})
		req := httptest.NewRequest(http.MethodPost, "/", body)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, int64(len(plainJSON)), capturedLen)
	})
}
