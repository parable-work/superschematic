package runtime

import (
	"encoding/json"
	"fmt"
	"net/url"
)

type PublicEncryptionKey struct {
	PublicKey string
	Algorithm string
	KeyID     string
}

type EncryptedRequestOptions struct {
	PublicEncryptionKey *PublicEncryptionKey
}

type EncryptedPayloadEnvelope struct {
	Algorithm    string `json:"algorithm"`
	Payload      string `json:"payload"`
	EncryptedKey string `json:"encryptedKey,omitempty"`
	IV           string `json:"iv,omitempty"`
	KeyID        string `json:"keyId"`
}

type UploadFile struct {
	Filename    string
	Reader      []byte
	ContentType string
}

type MultipartBody struct {
	JSONData any
	Files    map[string]UploadFile
}

func StripFieldsForMultipart(input any, fieldNames []string) (any, error) {
	if input == nil || len(fieldNames) == 0 {
		return input, nil
	}

	raw, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("marshal multipart input: %w", err)
	}
	result := map[string]any{}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("unmarshal multipart input: %w", err)
	}
	for _, fieldName := range fieldNames {
		delete(result, fieldName)
	}
	return result, nil
}

func AddQueryParam(values url.Values, key string, value any) {
	if value == nil {
		return
	}
	values.Set(key, fmt.Sprintf("%v", value))
}
