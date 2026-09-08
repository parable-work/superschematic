package response

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnwrapEnvelope(t *testing.T) {
	const (
		errExpectedObject = "expected RFC 9457 envelope object with data/meta.requestId in success response"
		errExpectedFields = "expected RFC 9457 envelope fields data and meta in success response"
		errExpectedMeta   = "expected RFC 9457 envelope meta.requestId in success response"
	)

	tests := []struct {
		name     string
		input    []byte
		wantData string
		wantErr  string
		wrapErr  bool
	}{
		{
			name:     "valid_envelope",
			input:    []byte(`{"data":{"id":"x"},"meta":{"requestId":"abc"}}`),
			wantData: `{"id":"x"}`,
		},
		{
			name:     "valid_envelope_null_data",
			input:    []byte(`{"data":null,"meta":{"requestId":"abc"}}`),
			wantData: `null`,
		},
		{
			name:     "valid_envelope_array_data",
			input:    []byte(`{"data":[1,2,3],"meta":{"requestId":"abc"}}`),
			wantData: `[1,2,3]`,
		},
		{
			name:     "valid_envelope_string_data",
			input:    []byte(`{"data":"hello","meta":{"requestId":"abc"}}`),
			wantData: `"hello"`,
		},
		{
			name:     "valid_envelope_number_data",
			input:    []byte(`{"data":42,"meta":{"requestId":"abc"}}`),
			wantData: `42`,
		},
		{
			name:     "valid_envelope_boolean_data",
			input:    []byte(`{"data":true,"meta":{"requestId":"abc"}}`),
			wantData: `true`,
		},
		{
			name:     "extra_meta_fields",
			input:    []byte(`{"data":{"k":"v"},"meta":{"requestId":"abc","extra":"ok"}}`),
			wantData: `{"k":"v"}`,
		},
		{
			name:    "no_data_key",
			input:   []byte(`{"result":{"id":"x"},"meta":{"requestId":"abc"}}`),
			wantErr: errExpectedFields,
		},
		{
			name:    "no_meta_key",
			input:   []byte(`{"data":{"id":"x"}}`),
			wantErr: errExpectedFields,
		},
		{
			name:    "meta_missing_requestId",
			input:   []byte(`{"data":{"id":"x"},"meta":{"other":"val"}}`),
			wantErr: errExpectedMeta,
		},
		{
			name:    "meta_not_object",
			input:   []byte(`{"data":{"id":"x"},"meta":"string"}`),
			wantErr: errExpectedMeta,
		},
		{
			name:    "meta_is_null",
			input:   []byte(`{"data":{"id":"x"},"meta":null}`),
			wantErr: errExpectedMeta,
		},
		{
			name:    "meta_is_array",
			input:   []byte(`{"data":{"id":"x"},"meta":[1]}`),
			wantErr: errExpectedMeta,
		},
		{
			name:    "empty_object",
			input:   []byte(`{}`),
			wantErr: errExpectedFields,
		},
		{
			name:    "empty_body",
			input:   []byte{},
			wantErr: errExpectedObject,
			wrapErr: true,
		},
		{
			name:    "not_json",
			input:   []byte(`not json at all`),
			wantErr: errExpectedObject,
			wrapErr: true,
		},
		{
			name:    "json_array",
			input:   []byte(`[1,2,3]`),
			wantErr: errExpectedObject,
			wrapErr: true,
		},
		{
			name:    "json_string",
			input:   []byte(`"hello"`),
			wantErr: errExpectedObject,
			wrapErr: true,
		},
		{
			name:    "json_number",
			input:   []byte(`42`),
			wantErr: errExpectedObject,
			wrapErr: true,
		},
		{
			name:    "json_null",
			input:   []byte(`null`),
			wantErr: errExpectedFields,
		},
		{
			name:    "error_response",
			input:   []byte(`{"error":"bad request"}`),
			wantErr: errExpectedFields,
		},
		{
			name:    "validation_error",
			input:   []byte(`{"error":"Validation failed","errors":[{"field":"name","message":"required"}]}`),
			wantErr: errExpectedFields,
		},
		{
			name:    "legacy_paginated",
			input:   []byte(`{"data":[{"id":"1"}],"total":5}`),
			wantErr: errExpectedFields,
		},
		{
			name:     "with_links",
			input:    []byte(`{"data":{"id":"x"},"meta":{"requestId":"abc"},"links":{}}`),
			wantData: `{"id":"x"}`,
		},
		{
			name:     "nested_envelope",
			input:    []byte(`{"data":{"data":{"id":"inner"},"meta":{"requestId":"inner-id"}},"meta":{"requestId":"outer"}}`),
			wantData: `{"data":{"id":"inner"},"meta":{"requestId":"inner-id"}}`,
		},
		{
			name:     "empty_requestId",
			input:    []byte(`{"data":{"id":"x"},"meta":{"requestId":""}}`),
			wantData: `{"id":"x"}`,
		},
		{
			name: "large_payload",
			input: func() []byte {
				big := strings.Repeat("x", 1_000_000)
				return []byte(`{"data":"` + big + `","meta":{"requestId":"abc"}}`)
			}(),
			wantData: `"` + strings.Repeat("x", 1_000_000) + `"`,
		},
		{
			name:     "unicode_in_data",
			input:    []byte(`{"data":{"name":"日本語"},"meta":{"requestId":"abc"}}`),
			wantData: `{"name":"日本語"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := UnwrapEnvelope(tt.input)
			if tt.wantErr != "" {
				require.Error(t, err)
				if tt.wrapErr {
					assert.ErrorContains(t, err, tt.wantErr)
				} else {
					assert.EqualError(t, err, tt.wantErr)
				}
				assert.Nil(t, result)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			if json.Valid([]byte(tt.wantData)) && (tt.wantData[0] == '{' || tt.wantData[0] == '[') {
				assert.JSONEq(t, tt.wantData, string(result))
			} else {
				assert.Equal(t, tt.wantData, string(result))
			}
		})
	}
}

func TestUnwrapEnvelope_InvalidJSONIncludesParseError(t *testing.T) {
	const errExpectedObject = "expected RFC 9457 envelope object with data/meta.requestId in success response"

	_, err := UnwrapEnvelope([]byte("<html>bad gateway</html>"))
	require.Error(t, err)
	assert.ErrorContains(t, err, errExpectedObject)
	assert.ErrorContains(t, err, "invalid character '<'")
}
