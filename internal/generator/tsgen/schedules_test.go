package tsgen

import (
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
)

// schedulesFixture is the DB service of D104's Schedules: a Problem that
// implements Schedules beside its status Workflow, whose terminal states
// finish its schedules, and whose runs are events of its event queue.
const schedulesFixture = "fixture-schedules-db"

// schedulesCheck is the file tsc checks the fixture's repositories
// against: Schedules' operations under schedules, typed from its
// declaration as the config narrows them (the kinds and the outcomes); a
// behavior's sub-table has no repository, a table that keeps one has no
// upserts, and one whose behaviors emit events hands an event of its event
// queue to a handler in the transaction that spends its claim (runEvent),
// beside its deadline and change queues.
const schedulesCheck = `import type { FixtureSchedulesDbOrm, ProblemRow } from './orm';
import { fixtureSchedulesDbRules } from './orm';

export async function wake(orm: FixtureSchedulesDbOrm, id: ProblemRow['id']): Promise<number> {
  const set = await orm.problem.schedules.setSchedule(id, { name: 'nudge', after: 300000 });
  const kind: 'after' | 'every' | 'cron' = set.kind;
  void kind;
  await orm.problem.schedules.setSchedule(id, { name: 'morning', cron: '0 9 * * MON-FRI', timeZone: 'Europe/London' });
  await orm.problem.schedules.pauseSchedule(id, { name: 'nudge' });
  await orm.problem.schedules.resumeSchedule(id, { name: 'nudge' });
  await orm.problem.schedules.cancelSchedule(id, { name: 'nudge' });
  const reported = await orm.problem.schedules.reportRun(id, { name: 'nudge', run: 1, outcome: 'missed' }, { caller: { subject: 'worker', permissions: ['problems.schedule'] } });
  const at: number | undefined = reported.retryAt;
  void at;
  // @ts-expect-error missed, told, resumed and held are Problem's outcomes.
  await orm.problem.schedules.reportRun(id, { name: 'nudge', run: 1, outcome: 'lost' });
  const listed = await orm.problem.schedules.listSchedules(id, {});
  return listed.items.length;
}

export async function handle(orm: FixtureSchedulesDbOrm, event: { rowKey: ProblemRow['id']; useId: string; kind: string; at: number; payload: unknown }): Promise<boolean> {
  const run = await orm.transaction(tx => tx.problem.runEvent(event, { claim: { id: 'message', token: 'token' } }, async () => {}));
  return run.ran;
}

// @ts-expect-error A behavior's sub-table has no repository.
export const subTable = (orm: FixtureSchedulesDbOrm) => orm.problemSchedulesSchedules;
// @ts-expect-error A table whose behaviors keep a sub-table has no upserts.
export const upsert = (orm: FixtureSchedulesDbOrm) => orm.problem.upsertOne;
export const queue: string = fixtureSchedulesDbRules.types.Problem.events!.table;
export const deadlines: string = fixtureSchedulesDbRules.types.Problem.deadlines!.table;
`

// TestSchedulesTablesCompile type-checks fixture-schedules-db's generated
// package (buildTSPackages, over the local http runtime and
// @superschematic/rules) against schedulesCheck: Schedules, whose
// operations, sub-table and queues orm.ts types from its declaration as
// Seats' (D104), with no code of its own in the generator, and runEvent,
// which a table whose behaviors emit events has.
func TestSchedulesTablesCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated repositories' type check in -short mode")
	}
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, schedulesFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", schedulesFixture, err)
	}
	buildTSPackages(t, []tsPackageCase{{
		name:     schedulesFixture,
		schema:   schema,
		registry: reg,
		extra:    map[string]string{"schedules.check.ts": schedulesCheck},
	}})
}
