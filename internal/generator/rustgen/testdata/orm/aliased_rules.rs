//! The repositories of fixture-aliased-db's Company over the behaviors'
//! core (D147, D116): a create gives the name, and one another company
//! holds, as a name or an alias, with its case ignored, is refused, naming
//! nothing of that company; a rename keeps the old name as an alias, which
//! resolve, on the table, finds; a removed alias and a deleted company's
//! names are freed for another; and merge_into folds one company into
//! another, moving its aliases, its name among them, its names and its
//! contacts, then deleting it through the core. The same cases run on a
//! SQLite file and, with SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL set, on
//! Postgres, through `$run`.

use db::orm::postgres::{deadpool_postgres, tokio_postgres};
use db::orm::sqlite::rusqlite;
use db::orm::*;
use db::*;
use serde_json::{json, Value};

const ALICE: &str = "5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90";

/// A call of a SQLite repository, which answers at once.
macro_rules! at_once {
    ($call:expr) => {
        $call
    };
}

/// A call of a Postgres repository, which is awaited.
macro_rules! awaited {
    ($call:expr) => {
        $call.await
    };
}

/// A map of members.
fn values(value: Value) -> Values {
    match value {
        Value::Object(members) => members,
        other => panic!("{other} is no object"),
    }
}

/// The veto a result holds, its details, and the refusal's text, its
/// message and its details together.
fn taken<T: std::fmt::Debug>(what: &str, result: Result<T, Error>) -> (String, Value, String) {
    match result {
        Err(Error::Refused(refusal)) => (
            refusal.details["vetoCode"].as_str().unwrap_or_default().to_owned(),
            refusal.details["vetoDetails"].clone(),
            format!("{} {}", refusal.message, refusal.details),
        ),
        other => panic!("{what} answered {other:?}, not the core's refusal"),
    }
}

/// Holds a refusal of `name` to name_taken naming nothing of the row whose
/// key is `holder` (D156): its details are the name and its normalized
/// form, and its text holds no key of the row.
fn names_nothing_of(refused: (String, Value, String), name: &str, normalized: &str, holder: &Value) {
    let (code, details, text) = refused;
    assert_eq!(code, "name_taken", "{text}");
    assert_eq!(details, json!({"name": name, "normalized": normalized}), "{text}");
    let key = holder.as_str().unwrap();
    assert!(!text.contains(key), "the refusal names the row that holds {name}: {text}");
}

/// The cases every dialect's repositories are held to, over `$orm`.
macro_rules! cases {
    ($orm:expr, $run:ident) => {{
        let orm = $orm.acting(Some(ALICE.to_owned()));
        let companies = orm.company().unwrap();
        let company = |name: &str| values(json!({"name": name, "city": "Oslo"}));
        let none = WriteOptions::default();

        // A name another company holds is refused, naming nothing of it.
        let acme = $run!(companies.untyped().create_one(&company("Acme")))
            .expect("create Acme");
        assert_eq!(acme["name"], json!("Acme"), "{acme:?}");
        $run!(companies.untyped().operation("name", "addAlias", &acme["id"], &json!({"alias": "Acme Corp"}), &none))
            .expect("add an alias");
        names_nothing_of(
            taken("a create of ACME", $run!(companies.untyped().create_one(&company("ACME")))),
            "ACME",
            "acme",
            &acme["id"],
        );
        let (code, _, _) = taken("a create of acme corp", $run!(companies.untyped().create_one(&company("  acme   corp"))));
        assert_eq!(code, "name_taken");

        // A rename keeps the old name as an alias, which resolve finds.
        let initech = $run!(companies.untyped().create_one(&company("Initech")))
            .expect("create Initech");
        let renamed = $run!(companies.untyped().operation("name", "rename", &initech["id"], &json!({"name": "Initech Group"}), &none))
            .expect("rename");
        assert_eq!(renamed, json!({"name": "Initech Group", "alias": "Initech"}));
        let found = $run!(companies.untyped().table_operation("name", "resolve", &json!({"name": "INITECH"})))
            .expect("resolve");
        assert_eq!(found["row"]["key"], initech["id"], "{found}");
        assert_eq!(found["row"]["alias"], json!(true), "{found}");
        let found = $run!(companies.untyped().table_operation("name", "resolve", &json!({"name": "Hooli"})))
            .expect("resolve");
        assert_eq!(found, json!({"row": null}));

        // A removed alias and a deleted company's names are freed.
        let umbrella = $run!(companies.untyped().create_one(&company("Umbrella")))
            .expect("create Umbrella");
        $run!(companies.untyped().operation("name", "addAlias", &umbrella["id"], &json!({"alias": "Umbrella Corp"}), &none))
            .expect("add an alias");
        $run!(companies.untyped().operation("name", "removeAlias", &umbrella["id"], &json!({"alias": "UMBRELLA CORP"}), &none))
            .expect("remove the alias");
        let corp = $run!(companies.untyped().create_one(&company("Umbrella Corp")))
            .expect("the alias removed is free");
        $run!(companies.untyped().delete_one(&umbrella["id"], &none)).expect("delete Umbrella");
        $run!(companies.untyped().operation("name", "addAlias", &corp["id"], &json!({"alias": "Umbrella"}), &none))
            .expect("the deleted company's name is free");

        // A merge folds one company into another: its contact, its aliases
        // and its name move to the company that stays.
        let from = $run!(companies.untyped().create_one(&company("Hooli")))
            .expect("create Hooli");
        $run!(companies.untyped().operation("name", "addAlias", &from["id"], &json!({"alias": "Hooli XYZ"}), &none))
            .expect("add an alias");
        let into = $run!(companies.untyped().create_one(&company("Nucleus")))
            .expect("create Nucleus");
        let contacts = orm.contact().unwrap();
        let contact = $run!(contacts.untyped().create_one(&values(json!({"companyId": from["id"], "email": "gavin@hooli.example"}))))
            .expect("create a contact");
        let from_key: IdentityUUID = serde_json::from_value(from["id"].clone()).unwrap();
        let into_key: IdentityUUID = serde_json::from_value(into["id"].clone()).unwrap();
        let refused = $run!(companies.merge_into(&from_key, &from_key));
        assert!(matches!(refused, Err(Error::Merge { code: "self", .. })), "{refused:?}");
        let merged = $run!(companies.merge_into(&from_key, &into_key)).expect("merge");
        assert_eq!(
            serde_json::to_value(&merged).unwrap(),
            json!({"repointed": {"CompanyNameAliases": 1, "CompanyNameNames": 2, "Contact": 1}, "deleted": {}})
        );
        for name in ["hooli", "Hooli XYZ", "nucleus"] {
            let found = $run!(companies.untyped().table_operation("name", "resolve", &json!({"name": name})))
                .expect("resolve");
            assert_eq!(found["row"]["key"], into["id"], "{name}: {found}");
        }
        let moved = $run!(contacts.untyped().get_one(&contact["id"])).expect("the contact");
        assert_eq!(moved["companyId"], into["id"], "{moved:?}");
        let gone = $run!(companies.untyped().get_one(&from["id"]));
        assert!(matches!(gone, Err(Error::NotFound(_))), "{gone:?}");
        names_nothing_of(
            taken("a create of Hooli after the merge", $run!(companies.untyped().create_one(&company("Hooli")))),
            "Hooli",
            "hooli",
            &into["id"],
        );
        let again = $run!(companies.merge_into(&from_key, &into_key));
        assert!(matches!(again, Err(Error::NotFound(_))), "{again:?}");
    }};
}

#[test]
fn aliased_names_and_a_merge_on_sqlite() {
    let mut connection = rusqlite::Connection::open_in_memory().expect("open");
    connection
        .execute_batch(include_str!("../create.sql"))
        .expect("create the tables");
    let transaction = connection
        .transaction_with_behavior(rusqlite::TransactionBehavior::Immediate)
        .expect("begin");
    cases!(sqlite::Repositories::new(&transaction).unwrap(), at_once);
    transaction.commit().expect("commit");
    println!("the repositories ran on SQLite");
}

#[tokio::test]
async fn aliased_names_and_a_merge_on_postgres() {
    let Ok(dsn) = std::env::var("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") else {
        return;
    };
    let schema = format!("orm_aliased_rules_rust_{}", std::process::id());
    let mut config: tokio_postgres::Config = dsn.parse().expect("the database's URL");
    let (admin, connection) = config
        .connect(tokio_postgres::NoTls)
        .await
        .expect("connect");
    tokio::spawn(connection);
    admin
        .batch_execute(&format!("CREATE SCHEMA {schema}"))
        .await
        .expect("create the schema");
    config.options(format!("-c search_path={schema},public"));
    let manager = deadpool_postgres::Manager::new(config, tokio_postgres::NoTls);
    let pool = deadpool_postgres::Pool::builder(manager)
        .max_size(2)
        .build()
        .expect("the pool");
    let ran = on_postgres(&pool).await;
    drop(pool);
    admin
        .batch_execute(&format!("DROP SCHEMA {schema} CASCADE"))
        .await
        .expect("drop the schema");
    ran.expect("the cases on Postgres");
    println!("the repositories ran on Postgres");
}

async fn on_postgres(pool: &deadpool_postgres::Pool) -> Result<(), Box<dyn std::error::Error>> {
    let mut client = pool.get().await?;
    client
        .batch_execute(include_str!("../postgres/create.sql"))
        .await?;
    let transaction = client.transaction().await?;
    cases!(postgres::Repositories::new(&transaction).unwrap(), awaited);
    transaction.commit().await?;
    Ok(())
}
