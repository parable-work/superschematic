package registry

import (
	"encoding/json"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
)

// TestEveryCoreBundleFileIsRegistered: each declaration under bundles/ is
// one coreBundles lists, under its file's name, so no file is embedded and
// left unregistered.
func TestEveryCoreBundleFileIsRegistered(t *testing.T) {
	entries, err := fs.ReadDir(coreBundleFiles, "bundles")
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, entry := range entries {
		files = append(files, strings.TrimSuffix(entry.Name(), ".bundle.json"))
	}
	listed := slices.Clone(coreBundles)
	slices.Sort(files)
	slices.Sort(listed)
	if !slices.Equal(files, listed) {
		t.Errorf("bundle files = %v, coreBundles = %v", files, listed)
	}
	reg := New(naming.Default())
	for _, file := range coreBundles {
		if _, ok := reg.Bundle(strings.ToUpper(file[:1]) + file[1:]); !ok {
			t.Errorf("bundles/%s.bundle.json registers no bundle %s", file, strings.ToUpper(file[:1])+file[1:])
		}
	}
}

// TestCoreBundlesExpand: each of the core's bundles (D103's step 5,
// D104's Barrier, Mailbox and Assignment) expands, its settings given,
// into parts whose configs their behaviors' config schemas take, each
// marked with the bundle, and an optional setting the table leaves out
// leaves out the member that reads it.
func TestCoreBundlesExpand(t *testing.T) {
	reg := New(naming.Default())
	cases := []struct {
		bundle   string
		settings string
		parts    []string
		contains []string
		lacks    []string
	}{
		{
			bundle:   "Reservation",
			settings: `{"resource": "room", "interval": {"start": "startsAt", "end": "endsAt"}, "amount": "priceCents", "currency": "GBP"}`,
			parts:    []string{"Holds", "Payments", "Timers", "Workflow@status"},
			contains: []string{`"link":"room"`, `"interval":{"end":"endsAt","start":"startsAt"}`, `"amount":{"field":"priceCents"}`, `"currency":"GBP"`, `"holdExpiry"`},
		},
		{
			bundle:   "Barrier",
			settings: `{"rows": "Agent", "link": "rollout", "timeoutMs": 300000, "releasePermission": "rollouts.release"}`,
			parts:    []string{"Rollups", "Timers", "Workflow@status"},
			contains: []string{`"schema":"Agent"`, `"link":"rollout"`, `"afterMs":300000`, `"permission":"rollouts.release"`},
		},
		{
			bundle:   "Mailbox",
			settings: `{"ttlMs": 30000}`,
			parts:    []string{"StateRules", "Lease", "Workflow@status"},
			contains: []string{`"ttlMs":30000`, `"by":["createdBy"]`, `"frozen":"all"`},
			lacks:    []string{"acquirePermission"},
		},
		{
			bundle:   "Assignment",
			parts:    []string{"Seats"},
			contains: []string{`"assignee":{"max":1,"onFull":"refuse"}`},
			lacks:    []string{`"take"`, `"assign"`},
		},
	}
	for _, c := range cases {
		t.Run(c.bundle, func(t *testing.T) {
			bundle, ok := reg.Bundle(c.bundle)
			if !ok {
				t.Fatalf("bundle %s is not registered", c.bundle)
			}
			var settings json.RawMessage
			if c.settings != "" {
				settings = json.RawMessage(c.settings)
			}
			parts, err := bundle.Expand(settings)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			var configs strings.Builder
			for _, part := range parts {
				label := part.Name
				if part.Field != "" {
					label += "@" + part.Field
				}
				got = append(got, label)
				if part.Bundle != c.bundle || part.Implements == (part.Field != "") {
					t.Errorf("part %s = %+v, want it marked with %s and implemented unless anchored on a field", label, part, c.bundle)
				}
				behavior, ok := reg.Behavior(part.Name)
				if !ok {
					t.Fatalf("part %s's behavior is not registered", label)
				}
				if err := behavior.ValidateConfig(part.Config); err != nil {
					t.Errorf("part %s: %v", label, err)
				}
				configs.Write(part.Config)
			}
			if strings.Join(got, ",") != strings.Join(c.parts, ",") {
				t.Errorf("parts = %v, want %v", got, c.parts)
			}
			for _, want := range c.contains {
				if !strings.Contains(configs.String(), want) {
					t.Errorf("configs lack %s:\n%s", want, configs.String())
				}
			}
			for _, unwanted := range c.lacks {
				if strings.Contains(configs.String(), unwanted) {
					t.Errorf("configs hold %s, which no setting gives:\n%s", unwanted, configs.String())
				}
			}
		})
	}
}

// TestBundleExpandRefusesSettings: settings the settings schema does not
// take are refused before any part is expanded, and so is a bundle that
// requires settings given none.
func TestBundleExpandRefusesSettings(t *testing.T) {
	reg := New(naming.Default())
	reservation, _ := reg.Bundle("Reservation")
	assignment, _ := reg.Bundle("Assignment")
	for _, c := range []struct {
		bundle   Bundle
		settings string
		want     string
	}{
		{reservation, ``, "missing properties"},
		{reservation, `{"resource": "room", "interval": {"start": "a", "end": "b"}, "amount": "x", "currency": "pounds"}`, "does not match pattern"},
		{assignment, `{"seat": "owner"}`, "additional properties 'seat' not allowed"},
	} {
		var settings json.RawMessage
		if c.settings != "" {
			settings = json.RawMessage(c.settings)
		}
		if _, err := c.bundle.Expand(settings); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s.Expand(%s) = %v, want an error containing %q", c.bundle.Name, c.settings, err, c.want)
		}
	}
	if !reservation.SettingsRequired() || assignment.SettingsRequired() {
		t.Errorf("SettingsRequired: Reservation %v, Assignment %v; want true, false", reservation.SettingsRequired(), assignment.SettingsRequired())
	}
}

// TestRegisterBundleRefuses: a declaration is held to its shape and to
// the registry's behaviors.
func TestRegisterBundleRefuses(t *testing.T) {
	settings := `"settingsSchema": {"type": "object", "additionalProperties": false, "properties": {"who": {"type": "string"}}}`
	for _, c := range []struct{ name, declaration, want string }{
		{"a lower-case name", `{"name": "rental", "description": "", "parts": [{"behavior": "Seats", "config": {"seats": {"a": {}}}}]}`, "is not a letter A-Z"},
		{"a taken name", `{"name": "Reservation", "description": "", "parts": [{"behavior": "Seats", "config": {"seats": {"a": {}}}}]}`, "already registered"},
		{"no part", `{"name": "Rental", "description": "", "parts": []}`, "lists no parts"},
		{"an unknown key", `{"name": "Rental", "description": "", "rules": {}, "parts": []}`, "unknown field"},
		{"an unregistered behavior", `{"name": "Rental", "description": "", "parts": [{"behavior": "Ghost"}]}`, "behavior Ghost is not registered"},
		{"a field part of a behavior with no value", `{"name": "Rental", "description": "", "parts": [{"behavior": "Seats", "field": "seats"}]}`, "names no value field"},
		{"an implemented part of a field behavior", `{"name": "Rental", "description": "", "parts": [{"behavior": "Workflow"}]}`, "names a value field"},
		{"a behavior implemented twice", `{"name": "Rental", "description": "", "parts": [{"behavior": "Seats"}, {"behavior": "Seats"}]}`, "implements Seats twice"},
		{"two parts on one field", `{"name": "Rental", "description": "", "parts": [{"behavior": "Workflow", "field": "status"}, {"behavior": "Workflow", "field": "status"}]}`, "two parts on field status"},
		{"a placeholder of no setting", `{"name": "Rental", "description": "", ` + settings + `, "parts": [{"behavior": "Seats", "config": {"seats": {"a": {"take": {"$setting": "whom"}}}}}]}`, `names "whom"`},
		{"an open settings schema", `{"name": "Rental", "description": "", "settingsSchema": {"type": "object"}, "parts": [{"behavior": "Seats"}]}`, "additionalProperties"},
	} {
		t.Run(c.name, func(t *testing.T) {
			reg := New(naming.Default())
			err := reg.registerBundle([]byte(c.declaration))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("registerBundle = %v, want an error containing %q", err, c.want)
			}
		})
	}
}
