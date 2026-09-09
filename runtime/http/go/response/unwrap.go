package response

import (
	"encoding/json"
	"fmt"
)

// UnwrapEnvelope enforces the RFC  success response contract
// ({data, meta: {requestId}}) and returns the inner data payload.
func UnwrapEnvelope(body []byte) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("expected RFC 9457 envelope object with data/meta.requestId in success response: %w", err)
	}
	dataRaw, hasData := envelope["data"]
	metaRaw, hasMeta := envelope["meta"]
	if !hasData || !hasMeta {
		return nil, fmt.Errorf("expected RFC 9457 envelope fields data and meta in success response")
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		return nil, fmt.Errorf("expected RFC 9457 envelope meta.requestId in success response")
	}
	if _, hasRequestID := meta["requestId"]; !hasRequestID {
		return nil, fmt.Errorf("expected RFC 9457 envelope meta.requestId in success response")
	}
	return dataRaw, nil
}
