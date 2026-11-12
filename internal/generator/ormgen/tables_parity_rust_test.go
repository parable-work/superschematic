package ormgen

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/rustgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestTableParityOfGoAndRustOnPostgres runs the calls of the table parity
// script that the Rust repositories have through the Go ORM and through
// the Rust repositories of fixture-ledger-db (D116), each in a schema of
// its own on the Postgres at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, each
// recording the text of every statement it sends, and requires the two
// equal, call by call, as TestTableParityOfGoAndTypeScriptOnPostgres holds
// the TypeScript repositories: the Rust runtime fills the statements
// sqlutil writes in the order the Go ORM fills them.
//
// The Rust side runs every call but the ones its repositories have no
// method for (getManyByIds, deleteMany and a transaction that enqueues).
// It needs cargo, which it skips without, and runs outside -short.
func TestTableParityOfGoAndRustOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Go and Rust table parity run in -short mode")
	}
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the Go ORM and the Rust repositories against one Postgres")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the Go and Rust table parity run")
	}
	paths := testpaths.Local(t)
	ormDir := generateORMModule(t, tableParityFixture)
	schema, err := loader.LoadService(filepath.Join(fixturesDir, tableParityFixture))
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// The Rust types crate with its repositories, and a binary over it.
	typesDir := filepath.Join(root, "types", "rust", tableParityFixture)
	output, err := rustgen.Generate(schema, rustgen.Options{SchemaName: tableParityFixture, Clock: codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))})
	if err != nil {
		t.Fatalf("generate rust types: %v", err)
	}
	if err := rustgen.SetLocalPaths(output, paths, typesDir); err != nil {
		t.Fatal(err)
	}
	if err := rustgen.WriteTypes(output, typesDir); err != nil {
		t.Fatal(err)
	}
	crateDir := filepath.Join(root, "server", "parity", "tables-rust")
	if err := os.MkdirAll(filepath.Join(crateDir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	lock, err := os.ReadFile(filepath.Join(paths.HTTPRuntimeRust, "Cargo.lock"))
	if err != nil {
		t.Fatalf("the HTTP runtime's lockfile: %v", err)
	}
	for name, content := range map[string]string{
		"Cargo.toml":  fmt.Sprintf(tableParityRustManifest, output.CrateName, filepath.ToSlash(typesDir)),
		"Cargo.lock":  string(lock),
		"src/main.rs": tableParityRust,
	} {
		if err := os.WriteFile(filepath.Join(crateDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	target := os.Getenv("CARGO_TARGET_DIR")
	if target == "" {
		target = filepath.Join(root, "target")
	}
	rustOut := filepath.Join(root, "statements.rust.json")
	run := exec.Command(cargo, "run", "--quiet", "--", filepath.Join(ormDir, "testdata", "create.sql"), rustOut)
	run.Dir = crateDir
	run.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target)
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("the Rust side failed: %v\n%s", err, out)
	}

	goOut := filepath.Join(root, "statements.go.json")
	t.Setenv("TABLE_PARITY_OUT", goOut)
	if err := os.WriteFile(filepath.Join(ormDir, "table_parity_test.go"), []byte(tableParityORMTest), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := runORMModule(t, ormDir); !strings.Contains(out, "--- PASS: TestTableParity ") {
		t.Fatalf("the Go side did not pass:\n%s", out)
	}

	read := func(path string) []parityCall {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var calls []parityCall
		if err := json.Unmarshal(data, &calls); err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return calls
	}
	goCalls := map[string]parityCall{}
	for _, call := range read(goOut) {
		goCalls[call.Name] = call
	}
	rustCalls := read(rustOut)
	// Every call of the script but the ones Rust has no method for.
	if want := len(goCalls) - len(tableParityNotInRust); len(rustCalls) != want {
		t.Fatalf("Rust made %d calls, want %d: every call of the script but %s", len(rustCalls), want, strings.Join(tableParityNotInRust, ", "))
	}
	statements := 0
	for _, rust := range rustCalls {
		g, ok := goCalls[rust.Name]
		if !ok {
			t.Fatalf("Rust made the call %s, which the Go side has none of", rust.Name)
		}
		if len(rust.Statements) == 0 {
			t.Errorf("%s: Rust sent no statement", rust.Name)
		}
		var want []string
		for _, st := range g.Statements {
			switch strings.ToUpper(strings.TrimSpace(st)) {
			case "BEGIN", "COMMIT", "ROLLBACK":
				continue
			}
			want = append(want, st)
		}
		if hydratingCalls[g.Name] && len(want) > len(rust.Statements) {
			want = want[:len(rust.Statements)]
		}
		if strings.Join(want, "\n;\n") != strings.Join(rust.Statements, "\n;\n") {
			t.Errorf("%s: the statements differ\nGo:\n%s\nRust:\n%s", g.Name, strings.Join(want, "\n"), strings.Join(rust.Statements, "\n"))
		}
		statements += len(rust.Statements)
	}
	t.Logf("Go and Rust sent the same %d statements over %d calls", statements, len(rustCalls))
}

// tableParityNotInRust are the calls of the script the Rust repositories
// have no method for.
var tableParityNotInRust = []string{"getAccounts", "deleteEntries", "transaction", "deleteAccounts"}

// tableParityRustManifest is the Rust side's Cargo.toml, given the types
// crate's name and its directory.
const tableParityRustManifest = `# The Rust side of ormgen's table parity test (D116): the parity script
# through the Rust repositories, over the DB's generated Rust types.
[package]
name = "ormgen-table-parity"
version = "0.0.0"
edition = "2021"
publish = false

[dependencies]
serde_json = "1.0.149"
tokio = { version = "1.52.2", features = ["macros", "rt"] }
db = { package = %q, path = %q, features = ["postgres"] }

# Its own workspace, so no manifest above the test's directory claims it.
[workspace]
`

// tableParityRust is the Rust side's src/main.rs: the calls of
// tableParityScript the Rust repositories have, with its values, over a
// schema of its own with create.sql (its first argument) applied, each
// call's statements written as JSON to its second argument.
const tableParityRust = `//! The table parity script (D79, D116) through the Rust repositories.

use std::sync::Mutex;

use db::orm::postgres::{deadpool_postgres, tokio_postgres, Repositories};
use db::orm::*;
use db::*;
use serde_json::json;

type Failure = Box<dyn std::error::Error + Send + Sync>;

#[tokio::main(flavor = "current_thread")]
async fn main() -> Result<(), Failure> {
    let args: Vec<String> = std::env::args().collect();
    let (create_sql, out) = (&args[1], &args[2]);
    let dsn = std::env::var("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")?;
    let schema = format!("table_parity_rust_{}", std::process::id());
    let mut config: tokio_postgres::Config = dsn.parse()?;
    let (admin, connection) = config.connect(tokio_postgres::NoTls).await?;
    tokio::spawn(connection);
    admin.batch_execute(&format!("CREATE SCHEMA {schema}")).await?;
    config.options(format!("-c search_path={schema},public"));
    let manager = deadpool_postgres::Manager::new(config, tokio_postgres::NoTls);
    let pool = deadpool_postgres::Pool::builder(manager).max_size(2).build()?;
    let result = script(&pool, create_sql, out).await;
    drop(pool);
    admin.batch_execute(&format!("DROP SCHEMA {schema} CASCADE")).await?;
    result
}

fn day(text: &str) -> TemporalDate {
    text.to_owned()
}

async fn script(pool: &deadpool_postgres::Pool, create_sql: &str, out: &str) -> Result<(), Failure> {
    let client = pool.get().await?;
    client.batch_execute(&std::fs::read_to_string(create_sql)?).await?;
    let calls: Mutex<Vec<(String, Vec<String>)>> = Mutex::new(Vec::new());
    let trace = |sql: &str| {
        if let Some(call) = calls.lock().unwrap().last_mut() {
            call.1.push(sql.to_owned());
        }
    };
    let call = |name: &str| calls.lock().unwrap().push((name.to_owned(), Vec::new()));
    let orm = Repositories::new(&client)?
        .acting(Some("TKbHsHDO6x9RCjbs5c2u4".to_owned()))
        .with_trace(&trace);
    let accounts = orm.account()?;
    let entries = orm.entry()?;
    let account = |name: &str, code: &str, opened_on: Option<&str>| AccountCreate {
        id: None,
        name: name.to_owned(),
        code: code.to_owned(),
        opened_on: opened_on.map(day),
        mailing_address: None,
    };

    call("createAccount");
    let cash = accounts.create_one(&account("Cash", "cash", None)).await?;
    call("createAccounts");
    let created = accounts
        .create_many(&[account("Bank", "bank", Some("2026-10-01")), account("Card", "card", None)])
        .await?;
    let (bank, card) = (&created[0], &created[1]);
    call("getAccount");
    accounts.get_one(&cash.id).await?;
    call("findAccount");
    accounts
        .find_one(
            Some(&AccountFilter { code: Some(Text::eq("bank".to_owned())), ..Default::default() }),
            &FindOptions::default(),
        )
        .await?;
    call("findAccounts");
    accounts
        .find_many(
            Some(&AccountFilter {
                name: Some(Text { ilike: Some("a".to_owned()), ..Default::default() }),
                search_text: Some("ca".to_owned()),
                referenced_by_entry_account: Some(ReferencedBy { exists: false, include_deleted: false }),
                ..Default::default()
            }),
            &FindOptions { order_by: vec![OrderBy::desc("code")], limit: Some(10), offset: Some(1), ..Default::default() },
        )
        .await?;
    call("pageAccounts");
    let by_name = |after| PageOptions { sort: Some("name".to_owned()), limit: Some(2), after, ..Default::default() };
    let first = accounts.find_page(None, &by_name(None)).await?;
    call("pageAccountsAfter");
    accounts.find_page(None, &by_name(first.next.clone())).await?;
    call("pageAccountsByKey");
    let listed = AccountFilter {
        code: Some(Text { in_: vec!["bank".to_owned(), "card".to_owned()], ..Default::default() }),
        ..Default::default()
    };
    accounts
        .find_page(
            Some(&listed),
            &PageOptions { sort: Some("id".to_owned()), desc: true, limit: Some(1), after: first.next.clone(), ..Default::default() },
        )
        .await?;
    call("updateAccount");
    accounts
        .update_one(&cash.id, &AccountUpdate { name: Some("Cash box".to_owned()), ..Default::default() }, &WriteOptions::default())
        .await?;
    call("updateAccounts");
    accounts
        .update_many(Some(&listed), &AccountUpdate { opened_on: Some(Some(day("2026-10-02"))), ..Default::default() })
        .await?;
    call("upsertAccount");
    accounts
        .upsert_one(
            &account("Cash", "cash", None),
            &["code"],
            &AccountUpdate { name: Some("Cash again".to_owned()), ..Default::default() },
            UpsertOptions::default(),
        )
        .await?;

    let entry = |account: &AccountRow, amount: i64, booked_on: &str| EntryCreate {
        id: None,
        account_id: account.id.clone(),
        transfer_id: None,
        amount_cents: amount,
        booked_on: day(booked_on),
        kind: None,
        memo: None,
        external_id: None,
        tags: Vec::new(),
        metadata: None,
        reviewed_at: None,
    };
    call("createEntry");
    let e1 = entries
        .create_one(&EntryCreate {
            memo: Some("Coffee".to_owned()),
            tags: vec!["food".to_owned()],
            ..entry(&cash, -1250, "2026-10-25")
        })
        .await?;
    call("createEntries");
    entries
        .create_many(&[
            EntryCreate {
                external_id: Some("x1".to_owned()),
                kind: Some(EntryKind::Credit),
                ..entry(bank, 500, "2026-10-26")
            },
            EntryCreate {
                transfer_id: Some(cash.id.clone()),
                memo: Some("Transfer".to_owned()),
                ..entry(card, -75, "2026-10-27")
            },
        ])
        .await?;
    call("getEntry");
    entries.get_one(&e1.id).await?;
    call("findEntry");
    entries
        .find_one(
            Some(&EntryFilter {
                amount_cents: Some(Ordered { between: Some((-2000, 0)), ..Default::default() }),
                booked_on: Some(Ordered { gte: Some(day("2026-10-01")), ..Default::default() }),
                kind: Some(Text { in_: vec![EntryKind::Debit], ..Default::default() }),
                memo: Some(Text { is_null: Some(false), ..Default::default() }),
                account_id: Some(Equals::eq(cash.id.clone())),
                search_text: Some("coffee".to_owned()),
                ..Default::default()
            }),
            &FindOptions::default(),
        )
        .await?;
    let by_amount = FindOptions { order_by: vec![OrderBy::asc("amountCents")], ..Default::default() };
    call("findEntries");
    entries
        .find_many(
            Some(&EntryFilter {
                reviewed_at: Some(Ordered { is_null: Some(true), ..Default::default() }),
                transfer_id: Some(Equals { is_null: Some(true), ..Default::default() }),
                amount_cents: Some(Ordered { in_: vec![500, -1250], ..Default::default() }),
                ..Default::default()
            }),
            &by_amount,
        )
        .await?;
    call("findEntriesByMetadata");
    entries
        .find_many(
            Some(&EntryFilter { metadata: Some(Equals::eq(json!({"source": "bank"}))), ..Default::default() }),
            &by_amount,
        )
        .await?;
    call("pageEntries");
    entries
        .find_page(
            Some(&EntryFilter {
                account_id: Some(Equals { in_: vec![cash.id.clone(), bank.id.clone()], ..Default::default() }),
                ..Default::default()
            }),
            &PageOptions { sort: Some("amountCents".to_owned()), desc: true, limit: Some(1), ..Default::default() },
        )
        .await?;
    call("findEntriesNegated");
    entries
        .find_many(
            Some(&EntryFilter {
                amount_cents: Some(Ordered { ne: Some(1), not_in: vec![500, -75], ..Default::default() }),
                booked_on: Some(Ordered { ne: Some(day("2026-10-26")), ..Default::default() }),
                kind: Some(Text { ne: Some(EntryKind::Credit), ..Default::default() }),
                memo: Some(Text {
                    ne: Some("Transfer".to_owned()),
                    not_in: vec!["Refund".to_owned(), "Fee".to_owned()],
                    ..Default::default()
                }),
                transfer_id: Some(Equals { ne: Some(cash.id.clone()), not_in: vec![bank.id.clone()], ..Default::default() }),
                ..Default::default()
            }),
            &by_amount,
        )
        .await?;
    call("pageEntriesByMemo");
    entries
        .find_page(
            None,
            &PageOptions {
                sort: Some("memo".to_owned()),
                desc: true,
                after: Some(serde_json::to_value(&e1.id)?),
                limit: Some(2),
                ..Default::default()
            },
        )
        .await?;
    call("updateEntry");
    entries
        .update_one(
            &e1.id,
            &EntryUpdate {
                memo: Some(None),
                amount_cents: Some(-1300),
                transfer_id: Some(Some(bank.id.clone())),
                ..Default::default()
            },
            &WriteOptions::default(),
        )
        .await?;
    call("updateEntries");
    entries
        .update_many(
            Some(&EntryFilter {
                account_id: Some(Equals { in_: vec![bank.id.clone(), card.id.clone()], ..Default::default() }),
                ..Default::default()
            }),
            &EntryUpdate { kind: Some(EntryKind::Credit), ..Default::default() },
        )
        .await?;
    let upsert = EntryCreate {
        external_id: Some("x1".to_owned()),
        kind: Some(EntryKind::Credit),
        ..entry(bank, 600, "2026-10-26")
    };
    let on = ["account", "externalId"];
    call("upsertEntry");
    entries
        .upsert_one(&upsert, &on, &EntryUpdate { amount_cents: Some(600), ..Default::default() }, UpsertOptions::default())
        .await?;
    call("upsertEntryKeep");
    entries.upsert_one(&upsert, &on, &EntryUpdate::default(), UpsertOptions::default()).await?;
    call("deleteEntry");
    entries.delete_one(&e1.id, &WriteOptions::default()).await?;
    call("findDeleted");
    entries
        .find_one(
            Some(&EntryFilter { account_id: Some(Equals::eq(card.id.clone())), ..Default::default() }),
            &FindOptions { include_deleted: true, ..Default::default() },
        )
        .await?;
    call("hardDeleteEntry");
    entries.hard_delete_one(&e1.id).await?;
    // The script deletes card's entries before its account; here they are
    // there still, so the delete is refused, after its statement is sent.
    call("deleteAccount");
    let _ = accounts.delete_one(&card.id, &WriteOptions::default()).await;

    let calls = calls.into_inner().unwrap();
    let written: Vec<_> = calls
        .iter()
        .map(|(name, statements)| json!({"name": name, "statements": statements}))
        .collect();
    std::fs::write(out, serde_json::to_string_pretty(&written)?)?;
    Ok(())
}
`
