package tsreader

import (
	"reflect"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// jobsSchema is an API with two jobs beside its operations (D52).
const jobsSchema = `import { HttpMethod, job, rest } from "@superschematic/api";

export abstract class OrderView {
  id: string;
}

export class OrderQueries {
  @rest(HttpMethod.GET, "orders/{id}")
  getOrder(id: string): OrderView {
    throw new Error("schema declaration only");
  }
}

// The warehouse's pick run: ships each placed order.
@job({ schedule: "*/15 * * * *", timeZone: "Europe/Paris", timeout: "5m", retries: 1 })
export abstract class ShipOrders {}

@job()
export abstract class ReindexOrders {}
`

// TestJobsAreNoTypes: each @job class is a job of the API, in declaration
// order, with its comment and arguments, and no type of the schema.
func TestJobsAreNoTypes(t *testing.T) {
	dir := decoratorTestService(t, "API", map[string]string{"src/a.schema.ts": jobsSchema})
	schema, _, err := LoadService(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []*ir.Job{
		{Name: "ShipOrders", Comment: "The warehouse's pick run: ships each placed order.", Schedule: "*/15 * * * *", TimeZone: "Europe/Paris", Timeout: "5m", Retries: 1},
		{Name: "ReindexOrders"},
	}
	if !reflect.DeepEqual(schema.Jobs, want) {
		t.Errorf("Jobs = %+v, want %+v", schema.Jobs, want)
	}
	for _, name := range []string{"ShipOrders", "ReindexOrders"} {
		if schema.Types[name] != nil {
			t.Errorf("job %s is a type", name)
		}
	}
	if schema.Types["OrderView"] == nil {
		t.Error("OrderView is no type")
	}
}

// TestJobRefusals: what @job refuses, where it is written.
func TestJobRefusals(t *testing.T) {
	dir := decoratorTestService(t, "API", map[string]string{"src/a.schema.ts": `import { HttpMethod, job, rest } from "@superschematic/api";

export class OrderQueries {
  @rest(HttpMethod.GET, "orders")
  listOrders(): string {
    throw new Error("schema declaration only");
  }
}

@job({ schedule: "@hourly" })
export abstract class Descriptor {}

@job({ schedule: "0 * * *" })
export abstract class FourFields {}

@job({ timeZone: "Mars/Olympus" })
export abstract class NoSuchZone {}

@job({ timeout: "90" })
export abstract class NoUnit {}

@job({ timeout: "1500ms" })
export abstract class NotWholeSeconds {}

@job({ retries: -1 })
export abstract class NegativeRetries {}

@job({ retries: 1.5 })
export abstract class FractionalRetries {}

@job()
export abstract class WithFields {
  limit: number;
}
`})
	_, _, err := LoadService(dir)
	if err == nil {
		t.Fatal("the load passed")
	}
	for _, want := range []string{
		`@job Descriptor: schedule: "@hourly" is a descriptor`,
		`@job FourFields: schedule: "0 * * *" has 4 fields; a schedule has five`,
		`@job NoSuchZone: timeZone: "Mars/Olympus" is no IANA time zone`,
		`@job NoUnit: timeout: "90" is no duration`,
		`@job NotWholeSeconds: timeout: "1500ms" is not a whole number of seconds`,
		`@job NegativeRetries: retries is -1; it is zero or more`,
		`@job retries is 1.5; it is a whole number`,
		`@job class WithFields has fields; a job takes no input`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%s", want, err)
		}
	}
}

// TestJobOnlyInAnAPI: a DB schema declares no job.
func TestJobOnlyInAnAPI(t *testing.T) {
	dir := decoratorTestService(t, "DB", map[string]string{"src/a.schema.ts": `import { job } from "@superschematic/api";

@job()
export abstract class Vacuum {}
`})
	_, _, err := LoadService(dir)
	if err == nil || !strings.Contains(err.Error(), "@job is only allowed in API schemas (this service is kind DB)") {
		t.Fatalf("load = %v, want @job refused in a DB schema", err)
	}
}
