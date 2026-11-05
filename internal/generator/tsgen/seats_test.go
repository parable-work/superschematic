package tsgen

import (
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
)

// seatsFixture is the DB service of D104's first behaviors only the core
// implements: a Problem that implements Seats beside its StateRules and its
// status Workflow, whose by name seats, and a Sandbox whose observed field
// is a Reconcile that moves its status Workflow.
const seatsFixture = "fixture-seats-db"

// seatsCheck is the file tsc checks the fixture's repositories against: a
// use the table implements has its operations under the behavior's member,
// typed from its declaration as the config narrows it (the seats' names); a
// field anchor other than Workflow's has its operations under its field, a
// Reconcile's state typed by the Workflow its config names; the row's
// observed is one of those states or null, and a create or an update sets
// none of Reconcile's fields; a behavior's sub-table has no repository, a
// table that keeps one has no upserts, and one whose behaviors arm
// deadlines keeps their queue.
const seatsCheck = `import type { FixtureSeatsDbOrm, ProblemRow, SandboxCreate, SandboxRow, SandboxUpdate } from './orm';
import { fixtureSeatsDbRules } from './orm';

export async function seat(orm: FixtureSeatsDbOrm, id: ProblemRow['id']): Promise<readonly string[]> {
  const taken = await orm.problem.seats.takeSeat(id, { seat: 'accountable' });
  const name: 'accountable' | 'responsible' | 'handles' = taken.seat;
  void name;
  await orm.problem.seats.leaveSeat(id, { seat: 'handles', subject: 'someone' }, { caller: { subject: 'pat', permissions: ['problems.release'] } });
  const listed = await orm.problem.seats.listSeats(id, {});
  const at: number | null = listed.items[0]!.seatedAt;
  void at;
  // @ts-expect-error Problem's Seats has no seat owners.
  await orm.problem.seats.takeSeat(id, { seat: 'owners' });
  return taken.displaced;
}

export async function observe(orm: FixtureSeatsDbOrm, id: SandboxRow['id']): Promise<SandboxRow['status'] | null> {
  const observed = await orm.sandbox.observed.observe(id, { state: 'running', at: 0 });
  const state: SandboxRow['status'] = observed.observed;
  void state;
  // @ts-expect-error Sandbox's Workflow has no state hibernating.
  await orm.sandbox.observed.observe(id, { state: 'hibernating', at: 0 });
  return observed.status;
}

export const state: SandboxRow['observed'] = 'unreachable';
export const unobserved: SandboxRow['observed'] = null;
// @ts-expect-error A row's observed is one of its Workflow's states.
export const lost: SandboxRow['observed'] = 'lost';
export const create: SandboxCreate = { name: 'box' };
// @ts-expect-error A create sets no owned field.
export const owned: SandboxCreate = { name: 'box', observed: 'running' };
// @ts-expect-error An update sets no owned field.
export const patch: SandboxUpdate = { observedDrift: null };
// @ts-expect-error A behavior's sub-table has no repository.
export const subTable = (orm: FixtureSeatsDbOrm) => orm.problemSeatsOccupants;
// @ts-expect-error A table whose behaviors keep a sub-table has no upserts.
export const upsert = (orm: FixtureSeatsDbOrm) => orm.problem.upsertOne;
export const queue: string = fixtureSeatsDbRules.types.Sandbox.deadlines!.table;
`

// TestSeatsTablesCompile type-checks fixture-seats-db's generated package
// (buildTSPackages, over the local http runtime and @superschematic/rules)
// against seatsCheck: Seats and Reconcile, whose operations, row fields and
// queues orm.ts types from their declarations as Workflow's and Timers'
// (D103, D104), with no code of their own in the generator.
func TestSeatsTablesCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated repositories' type check in -short mode")
	}
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, seatsFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", seatsFixture, err)
	}
	buildTSPackages(t, []tsPackageCase{{
		name:     seatsFixture,
		schema:   schema,
		registry: reg,
		extra:    map[string]string{"seats.check.ts": seatsCheck},
	}})
}
