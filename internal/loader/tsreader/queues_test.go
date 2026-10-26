package tsreader

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestQueuesAreMessageTypes: each @queue class of a DB schema stays a type,
// the message, with its fields, as an embedded struct that carries the
// queue's arguments; no other type does (D53).
func TestQueuesAreMessageTypes(t *testing.T) {
	schema, _, err := LoadService("testdata/services/fixture-queue-db")
	if err != nil {
		t.Fatal(err)
	}
	three := 3
	for name, want := range map[string]*ir.QueueDef{
		"OrderPlaced":   {Retries: &three, Backoff: "1m", MaxBackoff: "200s", Lease: "2m", Retain: "1h"},
		"Ping":          {Retain: "0s"},
		"OrderChanged":  {Key: "orderId", OnDuplicate: "replace", Serialize: true},
		"StockChecked":  {Key: "sku", OnDuplicate: "skip"},
		"SyncRequested": {Key: "accountId", Serialize: true},
	} {
		td := schema.Types[name]
		if td == nil {
			t.Fatalf("queue %s is no type", name)
		}
		if !reflect.DeepEqual(td.Queue, want) {
			t.Errorf("%s.Queue = %+v, want %+v", name, td.Queue, want)
		}
		if td.Role != ir.RoleEmbeddedStruct {
			t.Errorf("%s has role %s, want %s", name, td.Role, ir.RoleEmbeddedStruct)
		}
	}
	if fields := schema.Types["OrderPlaced"].Fields; len(fields) != 7 {
		t.Errorf("OrderPlaced has %d fields, want 7", len(fields))
	}
	if order := schema.Types["Order"]; order == nil || order.Queue != nil || order.Role != ir.RoleDBTable {
		t.Errorf("Order = %+v, want a table", order)
	}
	var names []string
	for _, td := range schema.Queues() {
		names = append(names, td.Name)
	}
	if strings.Join(names, ",") != "OrderChanged,OrderPlaced,Ping,StockChecked,SyncRequested" {
		t.Errorf("Queues() = %v", names)
	}
}

// TestEnqueueOnChangeIsATablesTrigger: @enqueueOnChange on a table records
// the queue, its writes in their one order, the fields, the conditions
// with their literals and the message, from the TypeScript the author
// writes (D80).
func TestEnqueueOnChangeIsATablesTrigger(t *testing.T) {
	schema, _, err := LoadService("testdata/services/fixture-queue-db")
	if err != nil {
		t.Fatal(err)
	}
	line := schema.Types["OrderLine"]
	if line == nil || line.Role != ir.RoleDBTable {
		t.Fatalf("OrderLine = %+v, want a table", line)
	}
	want := []*ir.EnqueueOnChange{{
		Queue:   "OrderChanged",
		On:      []string{"insert", "update", "delete"},
		Fields:  []string{"product", "quantity"},
		When:    []*ir.EnqueueCondition{{Field: "product", NotNull: true}},
		Message: map[string]string{"orderId": "order", "lineId": "id"},
	}}
	if !reflect.DeepEqual(line.EnqueueOnChange, want) {
		t.Errorf("OrderLine.EnqueueOnChange = %+v, want %+v", line.EnqueueOnChange, want)
	}
	for _, name := range []string{"Order", "Product", "OrderChanged"} {
		if len(schema.Types[name].EnqueueOnChange) > 0 {
			t.Errorf("%s has an @enqueueOnChange", name)
		}
	}
}

// TestWorkersAreNoTypes: each @worker class of an API is a worker of it, in
// declaration order, with its comment and arguments, and no type of the
// schema; its queue, a class of the API's database, is an import.
func TestWorkersAreNoTypes(t *testing.T) {
	schema, _, err := LoadService("testdata/services/fixture-queue-api")
	if err != nil {
		t.Fatal(err)
	}
	want := []*ir.Worker{
		{Name: "FulfilOrders", Comment: "Fulfils each order placed, four at a time.", Queue: "OrderPlaced", Concurrency: 4, Grace: "5s"},
		{Name: "AnswerPings", Comment: "Answers pings, one at a time.", Queue: "Ping"},
	}
	if !reflect.DeepEqual(schema.Workers, want) {
		t.Errorf("Workers = %+v, want %+v", schema.Workers, want)
	}
	for _, name := range []string{"FulfilOrders", "AnswerPings"} {
		if schema.Types[name] != nil {
			t.Errorf("worker %s is a type", name)
		}
	}
	imported := map[string]bool{}
	for _, imp := range schema.Imports {
		for _, name := range imp.Types {
			imported[name] = true
		}
	}
	if !imported["OrderPlaced"] || !imported["Ping"] {
		t.Errorf("imports = %+v, want OrderPlaced and Ping", schema.Imports)
	}
}

// TestQueueAndWorkerRefusals: what @queue and @worker refuse, where they
// are written. Verification holds the classes they declare to the rest, in
// every form (internal/loader/verify).
func TestQueueAndWorkerRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, kind, source string
		want               []string
	}{
		{
			name: "queue arguments",
			kind: "DB",
			source: `import { Identity } from "superscalar";
import { queue } from "@superschematic/db";

@queue({ retries: -1 })
export abstract class NegativeRetries {
  orderId: Identity.UUID;
}

@queue({ backoff: "1500ms" })
export abstract class NotWholeSeconds {
  orderId: Identity.UUID;
}

@queue({ retries: 1.5 })
export abstract class FractionalRetries {
  orderId: Identity.UUID;
}

@queue()
export abstract class NoFields {}

@queue({ backoff: "1m", maxBackoff: "30s" })
export abstract class CapUnderTheBackoff {
  orderId: Identity.UUID;
}

@queue({ maxBackoff: "10s" })
export abstract class CapUnderTheDefaultBackoff {
  orderId: Identity.UUID;
}

@queue({ retain: "-1h" })
export abstract class NegativeRetention {
  orderId: Identity.UUID;
}

@queue({ retain: "1.5s" })
export abstract class RetentionNotWholeSeconds {
  orderId: Identity.UUID;
}

@queue({ retain: "a week" })
export abstract class RetentionNoDuration {
  orderId: Identity.UUID;
}
`,
			want: []string{
				`@queue NegativeRetries: retries is -1; it is zero or more`,
				`@queue NotWholeSeconds: backoff: "1500ms" is not a whole number of seconds`,
				`@queue retries is 1.5; it is a whole number`,
				`@queue class NoFields has no fields`,
				`@queue CapUnderTheBackoff: maxBackoff is "30s", shorter than the backoff of 60s; it caps the backoff a failure doubles, so it is the backoff or more`,
				`@queue CapUnderTheDefaultBackoff: maxBackoff is "10s", shorter than the backoff of 30s`,
				`@queue NegativeRetention: retain: "-1h" is negative; it is zero or more, and zero keeps no done message`,
				`@queue RetentionNotWholeSeconds: retain: "1.5s" is not a whole number of seconds`,
				"@queue RetentionNoDuration: retain: \"a week\" is no duration; write one as `168h`, `30m` or `0s`",
			},
		},
		{
			name: "worker arguments",
			kind: "API",
			source: `import { worker } from "@superschematic/api";

export abstract class Local {
  id: string;
}

@worker({ queue: Local, concurrency: 0 })
export abstract class NoConcurrency {}

@worker({ queue: Local, grace: "1500ms" })
export abstract class NotWholeSeconds {}

@worker({ queue: Local })
export abstract class WithFields {
  limit: number;
}
`,
			want: []string{
				`@worker concurrency is 0; it is a whole number, one or more`,
				`@worker NotWholeSeconds: grace: "1500ms" is not a whole number of seconds`,
				`@worker class WithFields has fields`,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := decoratorTestService(t, tc.kind, map[string]string{"src/a.schema.ts": tc.source})
			_, _, err := LoadService(dir)
			if err == nil {
				t.Fatal("the load passed")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("missing %q in:\n%s", want, err)
				}
			}
		})
	}
}

// TestQueueOnlyInADBAndWorkerOnlyInAnAPI: an API schema declares no queue,
// and a DB schema no worker.
func TestQueueOnlyInADBAndWorkerOnlyInAnAPI(t *testing.T) {
	dir := decoratorTestService(t, "API", map[string]string{"src/a.schema.ts": `import { queue } from "@superschematic/db";

@queue()
export abstract class Ping {
  sentAt: string;
}
`})
	if _, _, err := LoadService(dir); err == nil || !strings.Contains(err.Error(), "@queue is only allowed in DB schemas (this service is kind API)") {
		t.Fatalf("load = %v, want @queue refused in an API schema", err)
	}
	dir = decoratorTestService(t, "DB", map[string]string{"src/a.schema.ts": `import { worker } from "@superschematic/api";

export abstract class Ping {
  sentAt: string;
}

@worker({ queue: Ping })
export abstract class AnswerPings {}
`})
	if _, _, err := LoadService(dir); err == nil || !strings.Contains(err.Error(), "@worker is only allowed in API schemas (this service is kind DB)") {
		t.Fatalf("load = %v, want @worker refused in a DB schema", err)
	}
}

// TestDeriversAreNoTypes: each @deriver class of an API is a deriver of it,
// in declaration order, with its comment and arguments, and no type of the
// schema; each of its schemas is a name, which the schema does not import
// (D74).
func TestDeriversAreNoTypes(t *testing.T) {
	schema, _, err := LoadService("testdata/services/fixture-derived-api")
	if err != nil {
		t.Fatal(err)
	}
	want := []*ir.Deriver{
		{Name: "TriageTickets", Comment: "Triages each ticket, eight answers at a time.", Schemas: []string{"Ticket"}, Concurrency: 8, BatchSize: 20},
		{Name: "FillAll", Comment: "Fills every derived field of the database."},
	}
	if !reflect.DeepEqual(schema.Derivers, want) {
		t.Errorf("Derivers = %+v, want %+v", schema.Derivers, want)
	}
	for _, name := range []string{"TriageTickets", "FillAll"} {
		if schema.Types[name] != nil {
			t.Errorf("deriver %s is a type", name)
		}
	}
	for _, imp := range schema.Imports {
		if slices.Contains(imp.Types, "Ticket") {
			t.Errorf("imports = %+v, want no engine type", schema.Imports)
		}
	}
}

// TestDeriverRefusals: what @deriver refuses, where it is written, and
// that only an API declares one.
func TestDeriverRefusals(t *testing.T) {
	dir := decoratorTestService(t, "API", map[string]string{"src/a.schema.ts": `import { deriver } from "@superschematic/api";

export abstract class Local {
  id: string;
}

@deriver({ concurrency: 0 })
export abstract class NoConcurrency {}

@deriver({ batchSize: 101 })
export abstract class TooLarge {}

@deriver({ schemas: ["Local", "Local"] })
export abstract class Twice {}

@deriver()
export abstract class WithFields {
  limit: number;
}
`})
	_, _, err := LoadService(dir)
	if err == nil {
		t.Fatal("the load passed")
	}
	for _, want := range []string{
		`@deriver concurrency is 0; it is a whole number, one or more`,
		`@deriver batchSize is 101; it is a whole number from 1 to 100`,
		`@deriver Twice: names schema Local twice`,
		`@deriver class WithFields has fields`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%s", want, err)
		}
	}
	dir = decoratorTestService(t, "DB", map[string]string{"src/a.schema.ts": `import { deriver } from "@superschematic/api";

@deriver()
export abstract class Fill {}
`})
	if _, _, err := LoadService(dir); err == nil || !strings.Contains(err.Error(), "@deriver is only allowed in API schemas (this service is kind DB)") {
		t.Fatalf("load = %v, want @deriver refused in a DB schema", err)
	}
}
