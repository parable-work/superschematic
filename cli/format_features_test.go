package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A JSON file whose tables declare the platform's features (D153, D156,
// D157) converts to TypeScript with each declaration as its decorator, and
// a field Aliased keeps (D147) as the field type the binary's registry
// declares, which takes the value's type first.
func TestFormatCommand_JSONToTSWritesTableFeatures(t *testing.T) {
	for _, tc := range []struct {
		file string
		want []string
	}{
		{"fixture-merge-access-db/src/merge.schema.ts", []string{
			`@audience<Brand>({ owner: "owner" })`,
			"  name: Aliased<string>;",
			"@accessGrants()\nexport abstract class Grant {}",
		}},
		{"fixture-parties-db/src/parties.schema.ts", []string{
			`import { Contact as ContactScalars, Identity, Temporal } from "superscalar";`,
			`@party<Contact>({ title: "name" })`,
			`@conversations("conversations")`,
			"@runRequests()",
			"@references<Message>({ cleared: ReferenceCleared })",
		}},
		{"fixture-notifications-db/src/notices.schema.ts", []string{
			`@event<Ticket>({ kind: "ticket.assigned", payload: ["title", "status"], title: "title" })`,
			`@notifier("dispatch")`,
			"@channelIdentities()",
			"@references<Ticket>({ cleared: ReferenceCleared })",
		}},
	} {
		t.Run(filepath.Dir(filepath.Dir(tc.file)), func(t *testing.T) {
			asJSON, err := runFormatCommand(t, "--to=json", "--stdout", filepath.Join(tsTestdata, tc.file))
			require.NoError(t, err)
			input := filepath.Join(t.TempDir(), "features.schema.json")
			require.NoError(t, os.WriteFile(input, []byte(asJSON), 0o644))

			out, err := runFormatCommand(t, "--to=ts", "--stdout", input)
			require.NoError(t, err)
			for _, want := range tc.want {
				assert.Contains(t, out, want)
			}
		})
	}
}
