package namespaces

import (
	"context"
	"net/url"

	"example.com/schemas/sdk/go/fixture-nested-arrays-api/runtime"
)

type jsonClient interface {
	DoJSON(ctx context.Context, method string, path string, query url.Values, body any, out any) error
	EncryptRequestPayload(payload any, override *runtime.PublicEncryptionKey) (*runtime.EncryptedPayloadEnvelope, error)
}
