package tsgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// releasedCheck is a file of fixture-version-graph-db's package that tsc
// checks (D139): the repository of PartReleased, the released rows of
// Part, has a table's reads, each typed as a table's, a filter through a
// relation and an include among them, on the ORM and in a transaction, and
// its members are those reads alone, so each write of a table is refused;
// Recipe includes its released parts; and Part's own repository writes as
// before.
const releasedCheck = `import type { FixtureVersionGraphDbOrm, PartReleasedFilter, PartReleasedPageKey, PartReleasedRepository, PartReleasedRow, PartReleasedSortField, PartRepository, PartRow } from './orm';

declare const orm: FixtureVersionGraphDbOrm;
declare const root: PartReleasedRow['recipeId'];
declare const after: PartReleasedPageKey | null;

type Same<A, B> = [A] extends [B] ? ([B] extends [A] ? true : false) : false;
type Reads = 'getOne' | 'getManyByIds' | 'findOne' | 'findMany' | 'findPage';
type Writes = 'createOne' | 'createMany' | 'updateOne' | 'updateMany' | 'upsertOne' | 'upsertOneResult' | 'upsertMany' | 'deleteOne' | 'deleteMany';

// The repository's members are a table's reads, and no other.
export const members: Same<keyof PartReleasedRepository, Reads> = true;
// Part's are its reads and its writes.
export const partMembers: Same<keyof PartRepository, Reads | Writes> = true;

export async function reads(): Promise<PartReleasedRow[]> {
  const filter: PartReleasedFilter = {
    recipeId: { eq: root },
    recipe: { title: { eq: 'Bread' } },
    anyOf: [{ heading: { ilike: 'B%' } }, { parentKey: { isNull: true } }],
  };
  const sort: PartReleasedSortField = 'rank';
  const one: PartReleasedRow = await orm.partReleased.getOne(root);
  const byId: Map<PartReleasedRow['id'], PartReleasedRow> = await orm.partReleased.getManyByIds([one.id]);
  const first: PartReleasedRow | null = await orm.partReleased.findOne(filter, { orderBy: [{ field: 'rank', desc: true }] });
  const { rows, total } = await orm.partReleased.findMany(filter, { limit: 10, include: { recipe: true, commit: true } });
  const title: string = rows[0]!.recipe.title;
  const count: number = total;
  const uncounted: undefined = (await orm.partReleased.findMany(filter, { count: false })).total;
  const page = await orm.partReleased.findPage(filter, { sort: { field: sort }, after, limit: 10, select: ['heading', 'rank'] });
  const next: PartReleasedPageKey | null = page.next;
  const heading: string = page.rows[0]!.heading;
  // @ts-expect-error The page selects heading and rank alone.
  void page.rows[0]!.sequence;
  const recipe = await orm.recipe.getOne(root, { include: { partReleaseds: true } });
  const released: PartReleasedRow[] = recipe.partReleaseds;
  void [byId, first, title, count, uncounted, next, heading, released];
  return orm.transaction(async tx => (await tx.partReleased.findMany({ recipeId: { eq: root } })).rows);
}

export async function writes(row: PartReleasedRow): Promise<void> {
  // @ts-expect-error The graph's release writes the rows: no createOne.
  await orm.partReleased.createOne(row);
  // @ts-expect-error No createMany.
  await orm.partReleased.createMany([row]);
  // @ts-expect-error No updateOne.
  await orm.partReleased.updateOne(row.id, { heading: 'Bake' });
  // @ts-expect-error No updateMany.
  await orm.partReleased.updateMany(null, { heading: 'Bake' });
  // @ts-expect-error No upsertOne.
  await orm.partReleased.upsertOne(row, ['recipe', 'entityKey'], { heading: 'Bake' });
  // @ts-expect-error No upsertOneResult.
  await orm.partReleased.upsertOneResult(row, ['recipe', 'entityKey'], { heading: 'Bake' });
  // @ts-expect-error No upsertMany.
  await orm.partReleased.upsertMany([row], ['recipe', 'entityKey'], ['heading']);
  // @ts-expect-error No deleteOne.
  await orm.partReleased.deleteOne(row.id);
  // @ts-expect-error No deleteMany.
  await orm.partReleased.deleteMany(null);
  // @ts-expect-error No hardDeleteOne.
  await orm.partReleased.hardDeleteOne(row.id);
  // Nor in a transaction.
  await orm.transaction(async tx => {
    // @ts-expect-error No createOne.
    await tx.partReleased.createOne(row);
  });
}

export async function part(ref: PartRow['refId']): Promise<PartRow> {
  const made = await orm.part.createOne({ recipeId: root, rank: 'F', heading: 'Mix', refId: ref });
  await orm.part.createMany([{ recipeId: root, rank: 'V', heading: 'Knead', refId: ref }]);
  await orm.part.updateMany({ recipeId: { eq: root } }, { heading: 'Rest' });
  await orm.part.upsertOne({ recipeId: root, rank: 'k', heading: 'Bake', refId: ref }, ['entityKey', 'ref'], { heading: 'Bake' });
  await orm.part.deleteMany({ recipeId: { eq: root }, rank: { eq: 'k' } });
  await orm.part.deleteOne(made.id);
  return orm.part.updateOne(made.id, { heading: 'Mix well' });
}
`

// TestAReleasedTablesRepositoryOnlyReads generates fixture-version-graph-db,
// whose Part is @graphMember({ released: true }), so the loader adds
// PartReleased, its released rows (D139). The table's info is read-only by
// ir.TypeDef.ReleasedOf, not by its name; orm.ts types its repository as a
// ReadRepository over a table's row, filter, order, sort, page key and
// relations, and declares no create, update or unique key type for it; its
// comment and the README say the graph's release owns the rows; and Part's
// repository, and its types, are a table's as before. tsc holds the types
// to releasedCheck in TestGeneratedPackagesCompile.
func TestAReleasedTablesRepositoryOnlyReads(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-version-graph-db"))
	if err != nil {
		t.Fatalf("load fixture-version-graph-db: %v", err)
	}
	if td := schema.Types["PartReleased"]; td == nil || td.ReleasedOf != "Part" {
		t.Fatalf("PartReleased = %+v, want the released rows of Part", td)
	}
	output, err := Generate(schema, Options{SchemaName: "fixture-version-graph-db"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if output.ORM == nil {
		t.Fatal("the package has no orm.ts")
	}
	readOnly := map[string]string{}
	for _, table := range output.ORM.Tables {
		if table.ReadOnly {
			readOnly[table.Type] = table.ReleasedOf
		}
	}
	if len(readOnly) != 1 || readOnly["PartReleased"] != "Part" {
		t.Fatalf("the read-only tables are %v, want PartReleased, the released rows of Part, alone", readOnly)
	}
	if got := output.ORM.ReadOnlyTables(); len(got) != 1 || got[0].Member != "partReleased" {
		t.Fatalf("ReadOnlyTables = %+v, want partReleased's", got)
	}

	dir := t.TempDir()
	if err := WriteTypes(output, dir); err != nil {
		t.Fatalf("write: %v", err)
	}
	read := func(name string) string {
		t.Helper()
		text, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(text)
	}
	orm := read(ORMFile)
	for _, want := range []string{
		// The released rows' repository: the reads, over a table's types.
		"export type PartReleasedRepository = ReadRepository<PartReleasedRow, PartReleasedFilter, PartReleasedOrderField, PartReleasedSortField, T.IdentityUUID, PartReleasedPageKey, PartReleasedRelations>;",
		"/** The repository of PartReleased, with reads alone: its rows are the released rows of Part, which the graph's release owns and rewrites (D139). */",
		"/** The rows of PartReleased a read finds: each member set is one condition, all of them ANDed. */",
		"/** The part_released table, as the runtime reads it. */",
		"export interface PartReleasedRow {",
		"export interface PartReleasedRelations {",
		"  readonly partReleased: PartReleasedRepository;",
		"    partReleased: partReleasedTable,",
		"The table of a graph member's released rows (D139), <Member>Released, has\na repository of reads alone",
		// A read reaches the rows from their root and their commit.
		"  readonly partReleaseds: Related<PartReleasedRow, PartReleasedRelations, 'many'>;",
		// Part's own repository and types, a table's.
		"export type PartRepository = Repository<\n  PartRow,\n  PartCreate,\n  PartUpdate,\n  PartFilter,\n  PartOrderField,\n  PartSortField,\n  PartUniqueKey,\n",
		"export interface PartCreate {",
		"export interface PartUpdate {",
		"export type PartUniqueKey = ",
		"/** The rows of Part a read or a write finds: each member set is one condition, all of them ANDed. */",
		"/** The part table, as the runtime reads and writes it. */",
	} {
		if !strings.Contains(orm, want) {
			t.Errorf("orm.ts lacks %q", want)
		}
	}
	for _, not := range []string{"PartReleasedCreate", "PartReleasedUpdate", "PartReleasedUniqueKey", "PartReleasedPreconditions", "PartReleasedUses"} {
		if strings.Contains(orm, not) {
			t.Errorf("orm.ts declares %s, a write's type, for the released rows", not)
		}
	}
	// The repository's type names no write's repository.
	_, after, _ := strings.Cut(orm, "export type PartReleasedRepository = ")
	declaration, _, _ := strings.Cut(after, ";")
	for _, not := range []string{"SoftDeleteRepository", "EncryptedRepository", "WithoutUpserts", "Uses", "Behavior"} {
		if strings.Contains(declaration, not) {
			t.Errorf("PartReleasedRepository = %s, which names %s", declaration, not)
		}
	}
	if !strings.HasPrefix(declaration, "ReadRepository<") {
		t.Errorf("PartReleasedRepository = %s, want a ReadRepository", declaration)
	}

	want := "- `orm.ts` - the repositories of the database's tables; the repository of a graph member's released rows only reads them, with no create, update, upsert or delete, since the graph's release owns the rows and rewrites them (D139): `partReleased`, the released rows of `Part`\n"
	if readme := read("README.md"); !strings.Contains(readme, want) {
		t.Errorf("README.md lacks %q", want)
	}
}

// TestATableWithNoReleasedRowsIsNotReadOnly generates fixture-db, no table
// of which holds a graph member's released rows: none is read-only, and
// neither orm.ts nor the README says a word of released rows (D139).
func TestATableWithNoReleasedRowsIsNotReadOnly(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatalf("load fixture-db: %v", err)
	}
	output, err := Generate(schema, Options{SchemaName: "fixture-db"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if output.ORM == nil || len(output.ORM.Tables) == 0 {
		t.Fatal("the package has no table")
	}
	if got := output.ORM.ReadOnlyTables(); len(got) != 0 {
		t.Fatalf("ReadOnlyTables = %+v, want none", got)
	}
	dir := t.TempDir()
	if err := WriteTypes(output, dir); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, name := range []string{ORMFile, "README.md"} {
		text, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(text), "released rows") || strings.Contains(string(text), "D139") {
			t.Errorf("%s speaks of released rows", name)
		}
	}
}
