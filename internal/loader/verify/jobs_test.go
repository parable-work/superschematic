package verify

import (
	"slices"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestJobsRefusedInIR: a schema authored as IR is held to what @job takes
// (D52): a five-field schedule, an IANA time zone, a timeout of whole
// seconds, retries of zero or more, a name no other job, type or operation
// set takes, and a Go method no other job takes; and only an API declares
// a job.
func TestJobsRefusedInIR(t *testing.T) {
	schema := ir.NewSchema("orders", ir.SchemaKindAPI)
	schema.Types["OrderView"] = &ir.TypeDef{Name: "OrderView", Role: ir.RoleAPIView}
	schema.OperationSets = []*ir.OperationSet{{Name: "OrderQueries"}}
	schema.Jobs = []*ir.Job{
		{Name: "ShipOrders", Schedule: "*/15 * * * *", TimeZone: "Europe/Paris", Timeout: "5m", Retries: 1},
		{Name: "Hourly", Schedule: "@hourly"},
		{Name: "Zoned", TimeZone: "Mars/Olympus"},
		{Name: "Quick", Timeout: "500ms"},
		{Name: "Negative", Retries: -2},
		{Name: "ShipOrders"},
		{Name: "OrderView"},
		{Name: "OrderQueries"},
		{Name: "ship_orders"},
	}
	r := &Result{}
	checkJobs(schema, r)
	var got []string
	for _, d := range r.Errors {
		got = append(got, d.Msg)
	}
	want := []string{
		`job Hourly: schedule: "@hourly" is a descriptor; write the five fields: minute, hour, day of the month, month and day of the week`,
		`job Zoned: timeZone: "Mars/Olympus" is no IANA time zone`,
		`job Quick: timeout: "500ms" is not a whole number of seconds`,
		"job Negative: retries is -2; it is zero or more",
		"job ShipOrders is declared twice",
		"job OrderView takes the name of a type of the schema; a job's class is no type, so give one of them another name",
		"job OrderQueries takes the name of an operation set of the schema; give one of them another name",
		"jobs ShipOrders and ship_orders are both the method ShipOrders of the API's Jobs interface; give one of them another name",
	}
	if !slices.Equal(got, want) {
		t.Errorf("errors =\n%q\nwant\n%q", got, want)
	}

	db := ir.NewSchema("orders-db", ir.SchemaKindDB)
	db.Jobs = []*ir.Job{{Name: "Vacuum"}}
	r = &Result{}
	checkJobs(db, r)
	if len(r.Errors) != 1 || r.Errors[0].Msg != "schema orders-db is a DB service and declares jobs; only an API service declares one" {
		t.Errorf("errors = %v, want the DB schema's job refused", r.Errors)
	}
}
