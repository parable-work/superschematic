package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/registry/registrytest"
)

// TestCatalogCommand prints each catalog from the binary's registry: a
// binary that links an extension lists its decorators beside the core's.
func TestCatalogCommand(t *testing.T) {
	out, err := runCommandWith(t, nil, "catalog", "scalars")
	require.NoError(t, err)
	assert.Contains(t, out, "| `Contact.Email` | An email address | `CITEXT` |")
	// The scalars of D84: a signed amount and an exact decimal, each with
	// its SQL type, its four languages' types and its checks.
	assert.Contains(t, out, "| `Finance.SignedMoney` | Signed monetary amount in smallest currency unit (e.g., cents for USD) | `BIGINT` | int64 / number / int / i64 | -9007199254740991 to 9007199254740991 |")
	assert.Contains(t, out, "| `Generic.Decimal` | Exact decimal number in plain notation, carried as a string | `NUMERIC` | string / string / decimal.Decimal / superscalar::Decimal | pattern")

	out, err = runCommandWith(t, nil, "catalog", "decorators")
	require.NoError(t, err)
	assert.Contains(t, out, "### `@rest`\n\n```ts\nfunction rest(method: HttpMethod, path?: string): MethodDecorator\n```")
	assert.NotContains(t, out, "`@shelf`")

	out, err = runCommandWith(t, []registry.Extension{registrytest.Acme{}}, "catalog", "decorators")
	require.NoError(t, err)
	assert.Contains(t, out, "### `@shelf`\n\nRegistered by the `acme` extension.")

	out, err = runCommandWith(t, nil, "catalog", "behaviors")
	require.NoError(t, err)
	assert.Contains(t, out, "## `Workflow`")

	out, err = runCommandWith(t, nil, "catalog", "capabilities")
	require.NoError(t, err)
	assert.Contains(t, out, "# Capability map\n")

	_, err = runCommandWith(t, nil, "catalog", "everything")
	require.ErrorContains(t, err, `no catalog named "everything"`)
}
