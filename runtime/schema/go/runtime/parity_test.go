package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/parable-work/superschematic/ir"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parityCorpus is runtime/schema/testdata/validation_parity.json, which the
// generated-validator parity harness (internal/generator/parity) writes from
// its matrix schema and vector table. The TypeScript and Python runtime
// suites read the same file.
type parityCorpus struct {
	Schema  *ir.Schema `json:"schema"`
	Vectors []struct {
		Name    string              `json:"name"`
		Type    string              `json:"type"`
		Payload map[string]any      `json:"payload"`
		Want    map[string][]string `json:"want"`
	} `json:"vectors"`
}

// flattenVerdicts maps errors to path -> sorted validator names, with nested
// object errors at dotted paths.
func flattenVerdicts(errs ValidationErrors, prefix string, out map[string][]string) {
	for key, value := range errs {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if nested, ok := value.(ValidationErrors); ok {
			flattenVerdicts(nested, path, out)
			continue
		}
		validators := []string{}
		for _, fieldErr := range errs.GetFieldErrors(key) {
			validators = append(validators, fieldErr.Validator)
		}
		sort.Strings(validators)
		out[path] = validators
	}
}

// TestValidationParityCorpus validates every corpus payload with the Go
// runtime and asserts the shared expected column: list rules for T[] and
// T[][], element checks at field[i] and field[i][j], and the scalar,
// enum and object element types.
func TestValidationParityCorpus(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "validation_parity.json"))
	require.NoError(t, err)
	var corpus parityCorpus
	require.NoError(t, json.Unmarshal(raw, &corpus))
	require.NotEmpty(t, corpus.Vectors)

	rt := New(corpus.Schema)
	for _, vector := range corpus.Vectors {
		t.Run(vector.Name, func(t *testing.T) {
			got := map[string][]string{}
			flattenVerdicts(rt.ValidateType(vector.Type, vector.Payload), "", got)
			assert.Equal(t, vector.Want, got)
		})
	}
}
