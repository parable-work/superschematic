package namespaces

import (
	"context"
	"fmt"
	"net/url"

	"example.com/schemas/sdk/go/fixture-nested-arrays-api/runtime"
)

type jsonClient interface {
	DoJSON(ctx context.Context, method string, path string, query url.Values, body any, out any) error
	EncryptRequestPayload(payload any, override *runtime.PublicEncryptionKey) (*runtime.EncryptedPayloadEnvelope, error)
}

// pathSegment writes a path parameter value as one path segment,
// percent-encoded once. Every server decodes a path parameter exactly once,
// so a value holding %, /, ? or # reaches the implementation as it was
// passed.
func pathSegment(value any) string {
	return url.PathEscape(fmt.Sprint(value))
}
