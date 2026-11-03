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
// type's @display beside its Workflow, with an icon, a field's multiline
// hint and an enum whose members take titles (D48, amended), loads from
// TypeScript and from the JSON format wrote to the same IR; json-schema
// admits the decorators' arguments and nothing else under display; and
// the loader refuses a display whose states its Workflow does not list, a
// multiline hint on a field that holds no text, a currency on a field that
// holds no amount, and a title two members share.
func TestDisplayThroughTheCommands(t *testing.T) {
	emit := func(service string) (map[string]json.RawMessage, map[string]json.RawMessage) {
		t.Helper()
		out, err := runCommandWith(t, nil, "build", service, "--emit-ir", "--out", t.TempDir())
		require.NoError(t, err)
		var schema struct {
			Types map[string]map[string]json.RawMessage `json:"types"`
			Enums map[string]map[string]json.RawMessage `json:"enums"`
		}
		require.NoError(t, json.Unmarshal([]byte(out), &schema))
		return schema.Types["Ticket"], schema.Enums["Priority"]
	}
	fromJSON, jsonPriority := emit(filepath.Join(loaderTestdata, "fixture-display-json"))
	fromTS, tsPriority := emit(filepath.Join(tsreaderTestdata, "fixture-display"))
	assert.JSONEq(t, string(fromJSON["display"]), string(fromTS["display"]))
	assert.JSONEq(t, string(fromJSON["fields"]), string(fromTS["fields"]))
	// A member's JSDoc @title is its title, and the rest of its comment
	// stays its comment (D48, amended).
	assert.JSONEq(t, string(jsonPriority["values"]), string(tsPriority["values"]))
	assert.JSONEq(t, `[{"name": "Low", "serializedAs": "low"},
		{"name": "High", "title": "Needs attention", "comment": "Within the week.", "serializedAs": "high"},
		{"name": "Urgent", "title": "Drop everything", "comment": "Drop everything else.", "serializedAs": "urgent"}]`, string(tsPriority["values"]))
	var fields []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(fromTS["fields"], &fields))
	require.Len(t, fields, 3)
	assert.JSONEq(t, `{"multiline": true}`, string(fields[2]["display"]))
	assert.JSONEq(t, `{"noun": "Ticket", "plural": "Tickets", "titleField": "title", "createLabel": "New ticket", "icon": "ticket",
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
			FieldDisplay struct {
				MinProperties int                        `json:"minProperties"`
				Properties    map[string]json.RawMessage `json:"properties"`
			} `json:"FieldDisplay"`
			EnumValueDef struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"EnumValueDef"`
		} `json:"$defs"`
	}
	require.NoError(t, json.Unmarshal([]byte(schemaOut), &def))
	assert.Equal(t, 1, def.Defs.TypeDisplay.MinProperties)
	assert.Len(t, def.Defs.TypeDisplay.Properties, 8)
	assert.Contains(t, def.Defs.TypeDisplay.Properties, "icon")
	assert.Equal(t, []string{"muted", "active", "success", "warning", "danger"}, def.Defs.DisplayState.Properties.Tone.Enum)
	assert.Equal(t, 1, def.Defs.FieldDisplay.MinProperties)
	assert.Len(t, def.Defs.FieldDisplay.Properties, 2)
	var multiline struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.Unmarshal(def.Defs.FieldDisplay.Properties["multiline"], &multiline))
	assert.Equal(t, "boolean", multiline.Type)
	// A currency is a code or a field (D48, amended).
	var currency struct {
		OneOf []map[string]string `json:"oneOf"`
	}
	require.NoError(t, json.Unmarshal(def.Defs.FieldDisplay.Properties["currency"], &currency))
	assert.Equal(t, []map[string]string{{"type": "string", "pattern": "^[A-Z]{3}$"}, {"$ref": "#/$defs/DisplayCurrency"}}, currency.OneOf)
	assert.Contains(t, def.Defs.EnumValueDef.Properties, "title")

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

	// A multiline hint on a field that holds no text fails the load, and
	// so does a title two members share (D48, amended).
	numeric := strings.Replace(string(data), `"name": "body",
          "display": {
            "multiline": true
          },
          "typeRef": {
            "name": "string"
          }`, `"name": "body",
          "display": {
            "multiline": true
          },
          "typeRef": {
            "name": "Priority"
          }`, 1)
	require.NotEqual(t, string(data), numeric)
	require.NoError(t, os.WriteFile(filepath.Join(service, "src", "ticket.schema.json"), []byte(numeric), 0o644))
	_, err = runCommandWith(t, nil, "build", service, "--emit-ir", "--out", t.TempDir())
	require.ErrorContains(t, err, `src/ticket.schema.json: type Ticket: field body takes @display multiline, which is for text, but it is Priority`)

	// So does a currency on a field that holds no amount.
	priced := strings.Replace(string(data), `"display": {
            "multiline": true
          },`, `"display": {
            "currency": "GBP"
          },`, 1)
	require.NotEqual(t, string(data), priced)
	require.NoError(t, os.WriteFile(filepath.Join(service, "src", "ticket.schema.json"), []byte(priced), 0o644))
	_, err = runCommandWith(t, nil, "build", service, "--emit-ir", "--out", t.TempDir())
	require.ErrorContains(t, err, `src/ticket.schema.json: type Ticket: field body takes @display currency, which is for an amount in minor units, but it is string`)

	shared := strings.Replace(string(data), `"title": "Drop everything"`, `"title": "needs attention"`, 1)
	require.NotEqual(t, string(data), shared)
	require.NoError(t, os.WriteFile(filepath.Join(service, "src", "ticket.schema.json"), []byte(shared), 0o644))
	_, err = runCommandWith(t, nil, "build", service, "--emit-ir", "--out", t.TempDir())
	require.ErrorContains(t, err, `enum Priority: members High and Urgent share the title "needs attention"`)
}
