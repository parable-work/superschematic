package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// D48 through the binary with no extension linked: fixture-display, a
// type's @display beside its Workflow, loads from TypeScript and from the
// JSON format wrote to the same IR; json-schema admits the decorator's
// argument and nothing else under display; and the loader refuses a
// display whose states its Workflow does not list.
func TestDisplayThroughTheCommands(t *testing.T) {
	emit := func(service string) map[string]json.RawMessage {
		t.Helper()
		out, err := runCommandWith(t, nil, "build", service, "--emit-ir", "--out", t.TempDir())
		require.NoError(t, err)
		var schema struct {
			Types map[string]map[string]json.RawMessage `json:"types"`
		}
		require.NoError(t, json.Unmarshal([]byte(out), &schema))
		return schema.Types["Ticket"]
	}
	fromJSON := emit(filepath.Join(loaderTestdata, "fixture-display-json"))
	fromTS := emit(filepath.Join(tsreaderTestdata, "fixture-display"))
	assert.JSONEq(t, string(fromJSON["display"]), string(fromTS["display"]))
	assert.JSONEq(t, string(fromJSON["fields"]), string(fromTS["fields"]))
	assert.JSONEq(t, `{"noun": "Ticket", "plural": "Tickets", "titleField": "title", "createLabel": "New ticket",
		"summaryFields": ["Workflow.status", "assignee"],
		"states": {"done": {"label": "Done", "tone": "success"}, "dropped": {"label": "Dropped", "tone": "danger"},
			"implementing": {"label": "Implement", "activeForm": "Implementing", "tone": "active"},
			"review": {"label": "Review", "activeForm": "In review", "tone": "warning"}, "todo": {"label": "To do", "tone": "muted"}},
		"transitions": {"implementing": {"review": "Send to review"}, "review": {"done": "Accept", "implementing": "Request changes"},
			"todo": {"dropped": "Drop", "implementing": "Start"}}}`, string(fromJSON["display"]))

	schemaOut, err := runCommandWith(t, nil, "json-schema")
	require.NoError(t, err)
	var def struct {
		Defs struct {
			TypeDisplay struct {
				MinProperties int                        `json:"minProperties"`
				Properties    map[string]json.RawMessage `json:"properties"`
			} `json:"TypeDisplay"`
			DisplayState struct {
				Properties struct {
					Tone struct {
						Enum []string `json:"enum"`
					} `json:"tone"`
				} `json:"properties"`
			} `json:"DisplayState"`
		} `json:"$defs"`
	}
	require.NoError(t, json.Unmarshal([]byte(schemaOut), &def))
	assert.Equal(t, 1, def.Defs.TypeDisplay.MinProperties)
	assert.Len(t, def.Defs.TypeDisplay.Properties, 7)
	assert.Equal(t, []string{"muted", "active", "success", "warning", "danger"}, def.Defs.DisplayState.Properties.Tone.Enum)

	// A state the type's Workflow does not list fails the load.
	service := t.TempDir()
	data, err := os.ReadFile(filepath.Join(loaderTestdata, "fixture-display-json", "schema.config.json"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(service, "schema.config.json"), data, 0o644))
	data, err = os.ReadFile(filepath.Join(loaderTestdata, "fixture-display-json", "src", "ticket.schema.json"))
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(service, "src"), 0o755))
	lost := strings.Replace(string(data), `"todo": {
            "label": "To do",`, `"lost": {
            "label": "To do",`, 1)
	require.NotEqual(t, string(data), lost)
	require.NoError(t, os.WriteFile(filepath.Join(service, "src", "ticket.schema.json"), []byte(lost), 0o644))
	_, err = runCommandWith(t, nil, "build", service, "--emit-ir", "--out", t.TempDir())
	require.ErrorContains(t, err, `src/ticket.schema.json: type Ticket: @display states labels "lost", which is not a state of its Workflow (todo, implementing, review, done, dropped)`)
}
