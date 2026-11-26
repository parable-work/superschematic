package conversations_test

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

// TestPartiesFixtureLoads (D157): fixture-parties-db's Contact is a party
// titled by its name; the conversations, the messages and the run requests
// hold the fields the build fills, each with the conversations origin; the
// messages keep references and files in the sub-tables D154 adds; and the
// channel identities record an opt-out.
func TestPartiesFixtureLoads(t *testing.T) {
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixtures, "fixture-parties-db"), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	parties := db.PartyTables()
	if len(parties) != 1 || parties[0].Name != "Contact" || parties[0].Party.Title != "name" {
		t.Fatalf("the party tables are %+v", parties)
	}
	want := map[*ir.TypeDef][]string{
		db.ConversationsTable(ir.ConversationsPart): {"id", "channelKind", "address", "partyTable", "party", "subject", "lastMessageAt"},
		db.ConversationsTable(ir.MessagesPart):      {"id", "conversation", "direction", "via", "fromAddress", "body", "messageId", "inReplyTo", "threadRefs", "authentication", "identity", "party", "author", "forSubject", "disposition", "state", "triage", "grant"},
		db.RunRequestsQueue():                       {"trigger", "party", "message", "grant"},
	}
	for td, fields := range want {
		if td == nil {
			t.Fatalf("a part is missing: %v", want)
		}
		for _, name := range fields {
			if !slices.Contains(fieldNames(td), name) {
				t.Errorf("%s has no %s: %v", td.Name, name, fieldNames(td))
			}
		}
		for _, fd := range td.Fields {
			if fd.Origin != ir.OriginConversations {
				t.Errorf("%s.%s has origin %q", td.Name, fd.Name, fd.Origin)
			}
		}
	}
	message := db.ConversationsTable(ir.MessagesPart)
	if message.RowReferences == nil || message.Attachments == nil {
		t.Errorf("Message keeps no references or no files: %+v", message)
	}
	subTables := 0
	for _, td := range db.Types {
		if td.RowTable != nil && td.RowTable.Row == message.Name {
			subTables++
		}
	}
	if subTables != 2 {
		t.Errorf("Message has %d sub-tables, want its references' and its attachments'", subTables)
	}
	if !slices.Contains(fieldNames(db.ChannelIdentitiesTable()), ir.ChannelIdentityOptedOutAtField) {
		t.Errorf("ChannelIdentity records no opt-out: %v", fieldNames(db.ChannelIdentitiesTable()))
	}
	if !db.UsesParties() {
		t.Error("the fixture uses no parties")
	}
}

// TestPartiesRefusals (D157): the verification pass refuses a party whose
// key is no UUID, a conversations class that
// declares fields of its own, one part without the other, run requests on
// no queue, and parties with no grants table.
func TestPartiesRefusals(t *testing.T) {
	base, err := os.ReadFile(filepath.Join(fixtures, "fixture-parties-db", "src", "parties.schema.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct{ from, to, want string }{
		"a key of no UUID": {
			"export abstract class Contact {\n  @key\n  id: AutoGenerate<Identity.UUID>;", "export abstract class Contact {\n  @key\n  id: string;",
			"Contact's key is no UUID",
		},
		"fields of its own": {
			"@conversations(\"conversations\")\nexport abstract class Conversation {}", "@conversations(\"conversations\")\nexport abstract class Conversation {\n  note: string;\n}",
			"the class declares note, and the build fills its fields",
		},
		"no messages": {
			"@conversations(\"messages\")\n", "",
			`no @conversations("messages")`,
		},
		"run requests on no queue": {
			"@queue()\n@runRequests()", "@runRequests()",
			"mark it @queue() too",
		},
		"no grants": {
			"@accessGrants()\nexport abstract class Grant {}", "",
			"no @accessGrants table",
		},
		"two marks": {
			"@party<Contact>({ title: \"name\" })", "@party<Contact>({ title: \"name\" })\n@conversations(\"conversations\")",
			"not more than one",
		},
	} {
		t.Run(name, func(t *testing.T) {
			text := string(base)
			if !strings.Contains(text, tc.from) {
				t.Fatalf("the fixture has no %q", tc.from)
			}
			dir, err := os.MkdirTemp(fixtures, "tmp-parties-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			copyDir(t, filepath.Join(fixtures, "fixture-parties-db"), dir)
			if err := os.WriteFile(filepath.Join(dir, "src", "parties.schema.ts"), []byte(strings.Replace(text, tc.from, tc.to, 1)), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err = loader.LoadService(dir, loader.WithRegistry(registry.New(naming.Default())))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("load = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

// TestPartiesExampleLoads: examples/parties's schemas load (D157):
// jobs-db's contacts are parties, its messages keep references and files,
// and jobs-api runs the notifier and the run requests on workers of their
// queues, and needs a channel adapter in every environment.
func TestPartiesExampleLoads(t *testing.T) {
	services := filepath.Join("..", "..", "..", "examples", "parties", "schemas", "services")
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(services, "jobs-db"), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load jobs-db: %v", err)
	}
	if parties := db.PartyTables(); len(parties) != 1 || parties[0].Name != "Contact" {
		t.Fatalf("jobs-db's parties are %+v", parties)
	}
	if message := db.ConversationsTable(ir.MessagesPart); message == nil || message.Attachments == nil || message.RowReferences == nil {
		t.Fatalf("jobs-db's messages are %+v", message)
	}
	if db.RunRequestsQueue() == nil {
		t.Fatal("jobs-db has no run requests queue")
	}
	api, err := loader.LoadService(filepath.Join(services, "jobs-api"), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load jobs-api: %v", err)
	}
	workers := map[string]*ir.Worker{}
	for _, w := range api.Workers {
		workers[w.Name] = w
	}
	if w := workers["Runs"]; w == nil || w.Queue != "RunRequest" {
		t.Errorf("Runs is %+v", w)
	}
	if w := workers["Notify"]; w == nil || w.Queue != "NoticeWork" {
		t.Errorf("Notify is %+v", w)
	}
	used := reg.RuntimeFeaturesUsed(registry.RuntimeFeatureInput{API: api, Databases: map[string]*ir.Schema{db.Name: db}})
	if len(used) != 1 || used[0].Interface != ir.RuntimeChannel {
		t.Errorf("jobs-api uses %v", used)
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
