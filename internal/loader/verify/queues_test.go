package verify

import (
	"slices"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestQueuesRefusedInIR: a DB schema authored as IR is held to what @queue
// takes and what a message is (D53): arguments @queue takes, an embedded
// struct with fields and no table's declarations, fields that are values
// and take no column the queue keeps, and a table no other table's name
// takes; and only a DB declares a queue.
func TestQueuesRefusedInIR(t *testing.T) {
	uuid := ir.TypeRef{Name: "Identity.UUID"}
	field := func(name string, mod func(*ir.FieldDef)) *ir.FieldDef {
		f := &ir.FieldDef{Name: name, TypeRef: uuid, Required: true}
		if mod != nil {
			mod(f)
		}
		return f
	}
	negative := -1
	schema := ir.NewSchema("orders-db", ir.SchemaKindDB)
	schema.Types["Order"] = &ir.TypeDef{Name: "Order", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{field("id", func(f *ir.FieldDef) { f.Key = true })}}
	schema.Types["OrderPlacedQueue"] = &ir.TypeDef{Name: "OrderPlacedQueue", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{field("orderId", nil)}}
	schema.Types["OrderPlaced"] = &ir.TypeDef{Name: "OrderPlaced", Role: ir.RoleEmbeddedStruct, Queue: &ir.QueueDef{}, Fields: []*ir.FieldDef{field("orderId", nil)}}
	schema.Types["AsTable"] = &ir.TypeDef{Name: "AsTable", Role: ir.RoleDBTable, Queue: &ir.QueueDef{}, Fields: []*ir.FieldDef{field("orderId", nil)}}
	schema.Types["Empty"] = &ir.TypeDef{Name: "Empty", Role: ir.RoleEmbeddedStruct, Queue: &ir.QueueDef{}}
	schema.Types["Indexed"] = &ir.TypeDef{Name: "Indexed", Role: ir.RoleEmbeddedStruct, Queue: &ir.QueueDef{}, Fields: []*ir.FieldDef{field("orderId", nil)}, Indexes: []ir.IndexDef{{Keys: []string{"orderId"}}}}
	schema.Types["Bad"] = &ir.TypeDef{Name: "Bad", Role: ir.RoleEmbeddedStruct, Queue: &ir.QueueDef{Retries: &negative, Lease: "90"}, Fields: []*ir.FieldDef{
		field("attempts", nil),
		field("key", func(f *ir.FieldDef) { f.Key = true }),
		field("related", func(f *ir.FieldDef) { f.Relation = &ir.RelationDef{Type: "Order"} }),
		field("order", func(f *ir.FieldDef) { f.TypeRef = ir.TypeRef{Name: "Order"} }),
		field("other", func(f *ir.FieldDef) { f.TypeRef = ir.TypeRef{Name: "OrderPlaced"} }),
	}}
	r := &Result{}
	checkQueues(schema, r)
	var got []string
	for _, d := range r.Errors {
		got = append(got, d.Msg)
	}
	want := []string{
		"queue AsTable has role DBTable; a queue's class is its message, role EmbeddedStruct, and no table",
		"queue Bad: retries is -1; it is zero or more",
		"queue Bad field attempts is the column attempts, which the queue keeps itself; rename the field",
		"queue Bad field key is a @key, @unique, @searchField or generated field; a message's fields are values, and the queue keys each message itself",
		"queue Bad field related is a relation; a message holds the key of what it names, not a reference to its table",
		"queue Bad field order is of type Order, a table; a message holds its key instead",
		"queue Bad field other is of type OrderPlaced, another queue's message; a message holds values",
		"queue Empty has no fields; a queue's class holds its message's fields",
		"queue Indexed is also a @jsonField, @versioned, @optimistic, version graph, projection or indexed class; a queue's class declares the message and nothing else",
		"queue OrderPlaced's table is order_placed_queue, the table of OrderPlacedQueue; rename one of them",
	}
	if !slices.Equal(got, want) {
		t.Errorf("errors =\n%q\nwant\n%q", got, want)
	}

	api := ir.NewSchema("orders", ir.SchemaKindAPI)
	api.Types["Ping"] = &ir.TypeDef{Name: "Ping", Role: ir.RoleEmbeddedStruct, Queue: &ir.QueueDef{}, Fields: []*ir.FieldDef{field("orderId", nil)}}
	r = &Result{}
	checkQueues(api, r)
	if len(r.Errors) != 1 || r.Errors[0].Msg != "schema orders is a API service and declares queue Ping; only a DB service declares one, whose database holds it" {
		t.Errorf("errors = %v, want the API schema's queue refused", r.Errors)
	}
}

// TestWorkersRefusedInIR: an API schema authored as IR is held to what
// @worker takes (D53): a queue the schema imports, a concurrency of one or
// more, a grace of whole seconds, a name no other worker, job, type or
// operation set takes, and a Go method no other worker takes; and only an
// API declares a worker.
func TestWorkersRefusedInIR(t *testing.T) {
	schema := ir.NewSchema("orders", ir.SchemaKindAPI)
	schema.Imports = []ir.Import{{Package: "@acme/orders-db", Types: []string{"OrderPlaced"}}}
	schema.Types["OrderView"] = &ir.TypeDef{Name: "OrderView", Role: ir.RoleAPIView}
	schema.OperationSets = []*ir.OperationSet{{Name: "OrderQueries"}}
	schema.Jobs = []*ir.Job{{Name: "ShipOrders"}}
	schema.Workers = []*ir.Worker{
		{Name: "FulfilOrders", Queue: "OrderPlaced", Concurrency: 4},
		{Name: "Unqueued"},
		{Name: "Negative", Queue: "OrderPlaced", Concurrency: -1},
		{Name: "Slow", Queue: "OrderPlaced", Grace: "500ms"},
		{Name: "NotImported", Queue: "Ping"},
		{Name: "FulfilOrders", Queue: "OrderPlaced"},
		{Name: "OrderView", Queue: "OrderPlaced"},
		{Name: "OrderQueries", Queue: "OrderPlaced"},
		{Name: "ShipOrders", Queue: "OrderPlaced"},
		{Name: "fulfil_orders", Queue: "OrderPlaced"},
	}
	r := &Result{}
	checkWorkers(schema, r)
	var got []string
	for _, d := range r.Errors {
		got = append(got, d.Msg)
	}
	want := []string{
		"worker Unqueued: names no queue",
		"worker Negative: concurrency is -1; it is one or more",
		`worker Slow: grace: "500ms" is not a whole number of seconds`,
		"worker NotImported handles queue Ping, which the schema does not import; a queue is a @queue class of the API's database, which the schema imports from it",
		"worker FulfilOrders is declared twice",
		"worker OrderView takes the name of a type of the schema; a worker's class is no type, so give one of them another name",
		"worker OrderQueries takes the name of an operation set of the schema; give one of them another name",
		"worker ShipOrders takes the name of a job of the schema, and both would be the deployable orders-ship-orders; give one of them another name",
		"workers FulfilOrders and fulfil_orders are both the method FulfilOrders of the API's Workers interface; give one of them another name",
	}
	if !slices.Equal(got, want) {
		t.Errorf("errors =\n%q\nwant\n%q", got, want)
	}

	db := ir.NewSchema("orders-db", ir.SchemaKindDB)
	db.Workers = []*ir.Worker{{Name: "Vacuum", Queue: "Ping"}}
	r = &Result{}
	checkWorkers(db, r)
	if len(r.Errors) != 1 || r.Errors[0].Msg != "schema orders-db is a DB service and declares workers; only an API service declares one" {
		t.Errorf("errors = %v, want the DB schema's worker refused", r.Errors)
	}
}
