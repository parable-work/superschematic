package notifications_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

const fixtures = "../tsreader/testdata/services"

func fieldNames(td *ir.TypeDef) []string {
	var out []string
	for _, fd := range td.Fields {
		out = append(out, fd.Name)
	}
	return out
}

// TestNotificationsFixtureLoads (D153): fixture-notifications-db's events
// keep their kinds, urgencies and payloads, and every notifier class and
// the channel identities hold the fields the build fills, each with the
// notifications origin.
func TestNotificationsFixtureLoads(t *testing.T) {
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixtures, "fixture-notifications-db"), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	ticket := db.Types["Ticket"]
	if ticket == nil || len(ticket.Events) != 4 {
		t.Fatalf("Ticket's events are %+v", ticket)
	}
	if overdue := ticket.EventOf("ticket.overdue"); overdue == nil || overdue.Urgency != ir.UrgencyUrgent || overdue.Title != "title" || !slices.Equal(overdue.Payload, []string{"title"}) {
		t.Errorf("ticket.overdue = %+v", overdue)
	}
	want := map[string][]string{
		ir.NotifierInbox:         {"recipient", "reference", "eventId", "eventKind", "urgency", "state", "acknowledgedAt"},
		ir.NotifierDeliveries:    {"notice", "channelKind", "slot", "address", "attempt", "state", "providerMessageId", "endedAt"},
		ir.NotifierRoutes:        {"eventKind", "filter", "recipients", "urgency", "escalation", "enabled"},
		ir.NotifierPreferences:   {"principal", "eventKind", "urgency", "channels", "quietStart", "quietEnd", "timeZone", "digest"},
		ir.NotifierSubscriptions: {"principal", "subjectTable", "filter", "rule", "start", "timeZone", "channels"},
		ir.NotifierChannelTrust:  {"channelKind", "level", "permissions"},
		ir.NotifierReplies:       {"channelKind", "address", "principal", "interpreter", "verb", "outcome", "undo"},
		ir.NotifierDispatch:      {"step", "event", "notice", "delivery", "recipient", "subscription"},
	}
	for part, fields := range want {
		td := db.NotifierTable(part)
		if td == nil {
			t.Errorf("no %s part", part)
			continue
		}
		for _, name := range fields {
			if !slices.Contains(fieldNames(td), name) {
				t.Errorf("%s (%s) has no %s: %v", td.Name, part, name, fieldNames(td))
			}
		}
		for _, fd := range td.Fields {
			if fd.Origin != ir.OriginNotifications {
				t.Errorf("%s.%s has origin %q", td.Name, fd.Name, fd.Origin)
			}
		}
	}
	identities := db.ChannelIdentitiesTable()
	if identities == nil {
		t.Fatal("no channel identities")
	}
	for _, name := range []string{"kind", "address", "principalKind", "principalTable", "principal", "trust", "verifiedAt", "codeHash"} {
		if !slices.Contains(fieldNames(identities), name) {
			t.Errorf("ChannelIdentity has no %s", name)
		}
	}
	if !db.UsesNotifications() {
		t.Error("the fixture uses no notifications")
	}
	used := reg.RuntimeFeaturesUsed(registry.RuntimeFeatureInput{API: &ir.Schema{Name: "api"}, Databases: map[string]*ir.Schema{db.Name: db}})
	if len(used) != 1 || used[0].Feature != "notifications" {
		t.Errorf("RuntimeFeaturesUsed = %v", used)
	}
}

// TestNotificationsRefusals (D153): the verification pass refuses an
// event's payload that names no field, a notifier class that declares its
// own fields, the dispatch part on no queue, and a schema that leaves out a
// part routing writes.
func TestNotificationsRefusals(t *testing.T) {
	base, err := os.ReadFile(filepath.Join(fixtures, "fixture-notifications-db", "src", "notices.schema.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct{ from, to, want string }{
		"a payload of no field": {
			`payload: ["title", "status"]`, `payload: ["title", "priority"]`,
			`"priority"`,
		},
		"a title of no field": {
			`kind: "ticket.summary", title: "title"`, `kind: "ticket.summary", title: "heading"`,
			`"heading"`,
		},
		"fields of its own": {
			"@notifier(\"inbox\")\nexport abstract class Notice {}", "@notifier(\"inbox\")\nexport abstract class Notice {\n  note: string;\n}",
			"the class declares note, and the build fills its fields",
		},
		"the dispatch part on no queue": {
			"@queue()\n@notifier(\"dispatch\")", "@notifier(\"dispatch\")",
			"mark the class @queue() too",
		},
		"no routes": {
			"@notifier(\"routes\")\nexport abstract class NoticeRoute {}", "",
			`declares no @notifier("routes")`,
		},
		"a kind of one word": {
			`kind: "ticket.assigned"`, `kind: "assigned"`,
			"dotted lower-camel words",
		},
	} {
		t.Run(name, func(t *testing.T) {
			text := string(base)
			if !strings.Contains(text, tc.from) {
				t.Fatalf("the fixture has no %q", tc.from)
			}
			dir, err := os.MkdirTemp(fixtures, "tmp-notifications-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			copyDir(t, filepath.Join(fixtures, "fixture-notifications-db"), dir)
			if err := os.WriteFile(filepath.Join(dir, "src", "notices.schema.ts"), []byte(strings.Replace(text, tc.from, tc.to, 1)), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err = loader.LoadService(dir, loader.WithRegistry(registry.New(naming.Default())))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("load = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

func copyDir(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.Walk(from, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		target := filepath.Join(to, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}
