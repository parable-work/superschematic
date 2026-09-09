package middleware

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/parable-work/superschematic/runtime/http/go/response"
	"go.uber.org/zap"
)

// JWTValidator validates a JWT token string and returns its claims.
type JWTValidator func(tokenString string) (map[string]any, error)

// PayloadDecryptor decrypts an encrypted payload envelope ciphertext. The
// key scope (which key, which principal, which account) is the
// implementation's to read from ctx; the middleware carries no scope of its
// own, so an auth provider that scopes keys per account installs the scope on
// the request context before this middleware runs.
type PayloadDecryptor interface {
	Decrypt(ctx context.Context, ciphertext string) ([]byte, error)
}

type encryptedRequestEnvelope struct {
	Algorithm     string `json:"algorithm"`
	Payload       string `json:"payload"`
	EncryptedKey  string `json:"encryptedKey,omitempty"`
	IV            string `json:"iv,omitempty"`
	KeyId         string `json:"keyId,omitempty"`
	EncryptedBody string `json:"encryptedBody,omitempty"` // Deprecated: use Payload.
}

// DecryptPayloadMiddleware decrypts envelope-encoded request payloads for encrypted endpoints.
// Supports both legacy RSA-only envelopes and hybrid AES-256-GCM + RSA-OAEP envelopes.
func DecryptPayloadMiddleware(decryptor PayloadDecryptor, getLogger LoggerGetter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if decryptor == nil {
				response.Error(w, http.StatusInternalServerError, "Server configuration error")
				return
			}

			var envelope encryptedRequestEnvelope
			if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
				response.Error(w, http.StatusBadRequest, "Invalid encrypted payload envelope")
				return
			}

			logger := loggerFromContext(r.Context(), getLogger)

			var plaintext []byte
			var err error

			switch strings.TrimSpace(envelope.Algorithm) {
			case "AES_256_GCM_RSA_OAEP_256":
				plaintext, err = decryptHybridEnvelope(r.Context(), decryptor, envelope, logger)
			case "RSA_OAEP_256", "NONE", "":
				ciphertext := strings.TrimSpace(envelope.Payload)
				if ciphertext == "" {
					ciphertext = strings.TrimSpace(envelope.EncryptedBody)
				}
				if ciphertext == "" {
					response.Error(w, http.StatusBadRequest, "Encrypted payload is required")
					return
				}
				plaintext, err = decryptor.Decrypt(r.Context(), ciphertext)
			default:
				response.Error(w, http.StatusBadRequest, "Unsupported encryption algorithm")
				return
			}

			if err != nil {
				logger.Warn("failed to decrypt request payload",
					zap.String("algorithm", envelope.Algorithm),
					zap.Error(err),
				)
				response.Error(w, http.StatusBadRequest, "Failed to decrypt request payload")
				return
			}

			if !json.Valid(plaintext) {
				response.Error(w, http.StatusBadRequest, "Decrypted payload must be valid JSON")
				return
			}

			r.Body = io.NopCloser(bytes.NewReader(plaintext))
			r.ContentLength = int64(len(plaintext))
			next.ServeHTTP(w, r)
		})
	}
}

// decryptHybridEnvelope handles AES-256-GCM + RSA-OAEP hybrid envelopes.
// The AES key is unwrapped by the decryptor (typically a KMS call), then the
// payload is decrypted locally with AES-GCM.
func decryptHybridEnvelope(
	ctx context.Context,
	decryptor PayloadDecryptor,
	envelope encryptedRequestEnvelope,
	logger *zap.Logger,
) ([]byte, error) {
	if envelope.EncryptedKey == "" {
		return nil, fmt.Errorf("hybrid envelope missing encryptedKey")
	}
	if envelope.IV == "" {
		return nil, fmt.Errorf("hybrid envelope missing iv")
	}
	if envelope.Payload == "" {
		return nil, fmt.Errorf("hybrid envelope missing payload")
	}

	aesKey, err := decryptor.Decrypt(ctx, envelope.EncryptedKey)
	if err != nil {
		return nil, fmt.Errorf("failed to unwrap AES key: %w", err)
	}

	iv, err := base64.StdEncoding.DecodeString(envelope.IV)
	if err != nil {
		return nil, fmt.Errorf("failed to decode IV: %w", err)
	}

	ciphertext, err := base64.StdEncoding.DecodeString(envelope.Payload)
	if err != nil {
		return nil, fmt.Errorf("failed to decode payload ciphertext: %w", err)
	}

	plaintext, err := DecryptAESGCM(aesKey, iv, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("AES-GCM decryption failed: %w", err)
	}

	logger.Debug("hybrid decryption successful",
		zap.Int("plaintext_size", len(plaintext)),
	)

	return plaintext, nil
}

// ExtractSubdomain extracts the subdomain from a host string given a base domain.
// For example, ExtractSubdomain("acme.example.com", "example.com") returns "acme".
// Returns empty string if the host doesn't have a subdomain or doesn't match the base domain.
func ExtractSubdomain(host, baseDomain string) string {
	hostWithoutPort, _, err := net.SplitHostPort(host)
	if err == nil {
		host = hostWithoutPort
	}

	host = strings.ToLower(host)
	baseDomain = strings.ToLower(baseDomain)

	if !strings.HasSuffix(host, baseDomain) {
		return ""
	}

	if host == baseDomain {
		return ""
	}

	prefixLen := len(host) - len(baseDomain) - 1
	if prefixLen <= 0 {
		return ""
	}

	if host[prefixLen] != '.' {
		return ""
	}

	return host[:prefixLen]
}
