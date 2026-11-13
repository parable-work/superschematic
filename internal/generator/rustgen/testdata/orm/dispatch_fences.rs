//! The repositories of fixture-fenced-db on a SQLite file and on Postgres
//! (D118): one script of writes runs through each dialect's repositories,
//! over the DDL the build writes for it, and reads every @fenced counter's
//! exact value after every write. A write of a column a fence covers
//! advances that fence by one and leaves the table's other fence as it
//! was; a write of uncovered columns alone advances none; a write that
//! sets a fence itself is written as it stands; and a soft delete advances
//! the fence that covers `deletedAt`.
//!
//! Each DDL holds the fences' triggers, so every write of the script is
//! one they allow, and a write that skips a value, or keeps the one it
//! holds, is refused and changes nothing.

use db::orm::postgres::{deadpool_postgres, tokio_postgres};
use db::orm::sqlite::rusqlite;
use db::orm::*;
use db::*;

const ADA: &str = "11111111-1111-4111-8111-111111111111";

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

/// The key of the row `n` of the script.
fn key(n: u8) -> IdentityUUID {
    format!("00000000-0000-4000-8000-0000000000{n:02x}")
        .parse()
        .expect("a UUID")
}

fn delivery(n: u8, address: &str) -> DeliveryCreate {
    DeliveryCreate {
        id: Some(key(n)),
        address: address.to_owned(),
        courier_id: None,
        plan_version: None,
        attempts: None,
        proof: None,
        run_version: None,
        note: None,
    }
}

fn depot(n: u8, name: &str) -> DepotCreate {
    DepotCreate {
        id: Some(key(n)),
        name: name.to_owned(),
        capacity: 10,
        revision: None,
    }
}

fn text(value: &str) -> Option<String> {
    Some(value.to_owned())
}

/// The delivery's counters as the table holds them: `planVersion`, then
/// `runVersion`.
macro_rules! counters {
    ($run:ident, $deliveries:ident, $n:expr) => {{
        let row = $run!($deliveries.get_one(&key($n))).expect("read the delivery");
        (row.plan_version, row.run_version)
    }};
}

/// An update of the delivery, whose answer holds the counters the table
/// then holds, which it answers.
macro_rules! update {
    ($run:ident, $deliveries:ident, $n:expr, $change:expr) => {{
        let row = $run!($deliveries.update_one(&key($n), &$change, &WriteOptions::default()))
            .expect("update the delivery");
        let stored = counters!($run, $deliveries, $n);
        assert_eq!((row.plan_version, row.run_version), stored);
        stored
    }};
}

/// An upsert of the delivery by its key: what it did, and the counters the
/// table then holds.
macro_rules! upsert {
    ($run:ident, $deliveries:ident, $n:expr, $change:expr, $only_if_changed:expr) => {{
        let upserted = $run!($deliveries.upsert_one(
            &delivery($n, "ignored"),
            &["id"],
            &$change,
            UpsertOptions {
                only_if_changed: $only_if_changed
            },
        ))
        .expect("upsert the delivery");
        let stored = counters!($run, $deliveries, $n);
        assert_eq!(
            (upserted.row.plan_version, upserted.row.run_version),
            stored
        );
        (upserted.status, stored.0, stored.1)
    }};
}

/// The depot's revision and whether it is deleted, a deleted one's too.
macro_rules! revision {
    ($run:ident, $depots:ident, $n:expr) => {{
        let filter = DepotFilter {
            id: Some(Equals::eq(key($n))),
            ..Default::default()
        };
        let options = FindOptions {
            include_deleted: true,
            ..Default::default()
        };
        let row = $run!($depots.find_one(Some(&filter), &options))
            .expect("read the depot")
            .expect("the depot");
        (row.revision, row.deleted_at.is_some())
    }};
}

/// The script, over the repositories `$orm`.
macro_rules! script {
    ($orm:ident, $run:ident) => {{
        let couriers = $orm.courier().unwrap();
        let deliveries = $orm.delivery().unwrap();
        let parcels = $orm.parcel().unwrap();
        let depots = $orm.depot().unwrap();
        let plain = WriteOptions::default();
        let (first, other, fresh) = (10, 11, 12);

        let ada = $run!(couriers.create_one(&CourierCreate {
            id: Some(key(1)),
            name: "Ada".to_owned(),
        }))
        .expect("create the courier");
        $run!(deliveries.create_one(&delivery(first, "1 Main St"))).expect("create the delivery");
        assert_eq!(counters!($run, deliveries, first), (0, 0));

        // A covered column advances its fence by one, and the other stays.
        let address = |address: &str| DeliveryUpdate {
            address: text(address),
            ..Default::default()
        };
        assert_eq!(update!($run, deliveries, first, address("2 Main St")), (1, 0));
        // note and updatedAt are no fence's: the update stamps, and bumps
        // none.
        let note = |note: Option<&str>| DeliveryUpdate {
            note: Some(note.map(str::to_owned)),
            ..Default::default()
        };
        assert_eq!(update!($run, deliveries, first, note(Some("ring twice"))), (1, 0));
        assert_eq!(update!($run, deliveries, first, note(None)), (1, 0));
        let attempts = |attempts: i64| DeliveryUpdate {
            attempts: Some(attempts),
            ..Default::default()
        };
        assert_eq!(update!($run, deliveries, first, attempts(1)), (1, 1));
        let both = DeliveryUpdate {
            address: text("3 Main St"),
            proof: Some(text("signed")),
            ..Default::default()
        };
        assert_eq!(update!($run, deliveries, first, both), (2, 2));
        // Two covered columns of one fence advance it once.
        let run_twice = DeliveryUpdate {
            attempts: Some(2),
            proof: Some(None),
            ..Default::default()
        };
        assert_eq!(update!($run, deliveries, first, run_twice), (2, 3));
        // A value the row already holds counts: the statement cannot know.
        assert_eq!(update!($run, deliveries, first, address("3 Main St")), (3, 3));
        // A relation is its key's column.
        let courier = |courier: Option<IdentityUUID>| DeliveryUpdate {
            courier_id: Some(courier),
            ..Default::default()
        };
        assert_eq!(update!($run, deliveries, first, courier(Some(ada.id.clone()))), (4, 3));
        assert_eq!(update!($run, deliveries, first, courier(None)), (5, 3));

        // A write that sets the fence itself is written as it stands: the
        // caller read 5 and writes 6, and runVersion is still advanced.
        let driven = |address: &str, plan: i64| DeliveryUpdate {
            address: text(address),
            plan_version: Some(plan),
            ..Default::default()
        };
        assert_eq!(update!($run, deliveries, first, driven("4 Main St", 6)), (6, 3));
        let beside = DeliveryUpdate {
            plan_version: Some(7),
            attempts: Some(3),
            ..Default::default()
        };
        assert_eq!(update!($run, deliveries, first, beside), (7, 4));
        // A value that skips one is written as it stands too, and refused.
        let refused = $run!(deliveries.update_one(&key(first), &driven("5 Main St", 9), &plain));
        assert!(refused.is_err(), "planVersion 9 over 7 was written: {refused:?}");
        assert_eq!(counters!($run, deliveries, first), (7, 4));
        let held = $run!(deliveries.get_one(&key(first))).unwrap();
        assert_eq!(held.address, "4 Main St");

        // update_many advances each row it updates, and a filter on the
        // fence is a compare-and-set: the second, at the version the first
        // read, finds no row.
        $run!(deliveries.create_one(&delivery(other, "9 Side St"))).expect("create another");
        let at = DeliveryFilter {
            id: Some(Equals::eq(key(first))),
            plan_version: Some(Ordered::eq(7)),
            ..Default::default()
        };
        assert_eq!($run!(deliveries.update_many(Some(&at), &driven("6 Main St", 8))).unwrap(), 1);
        assert_eq!(counters!($run, deliveries, first), (8, 4));
        assert_eq!($run!(deliveries.update_many(Some(&at), &driven("7 Main St", 8))).unwrap(), 0);
        assert_eq!(counters!($run, deliveries, first), (8, 4));
        assert_eq!($run!(deliveries.update_many(None, &note(Some("fragile")))).unwrap(), 2);
        assert_eq!(counters!($run, deliveries, first), (8, 4));
        assert_eq!(counters!($run, deliveries, other), (0, 0));
        assert_eq!($run!(deliveries.update_many(None, &attempts(5))).unwrap(), 2);
        assert_eq!(counters!($run, deliveries, first), (8, 5));
        assert_eq!(counters!($run, deliveries, other), (0, 1));
        let handed = DeliveryUpdate {
            courier_id: Some(Some(ada.id.clone())),
            proof: Some(text("left at door")),
            ..Default::default()
        };
        assert_eq!($run!(deliveries.update_many(None, &handed)).unwrap(), 2);
        assert_eq!(counters!($run, deliveries, first), (9, 6));
        assert_eq!(counters!($run, deliveries, other), (1, 2));

        // An upsert's update of the row its key holds, as update_one; one
        // that sets no field, or inserts, advances nothing.
        use UpsertStatus::{Inserted, Unchanged, Updated};
        assert_eq!(upsert!($run, deliveries, first, note(Some("gate code 12")), false), (Updated, 9, 6));
        let proof = |proof: &str| DeliveryUpdate {
            proof: Some(text(proof)),
            ..Default::default()
        };
        assert_eq!(upsert!($run, deliveries, first, proof("photo"), false), (Updated, 9, 7));
        let moved = DeliveryUpdate {
            address: text("8 Main St"),
            attempts: Some(6),
            ..Default::default()
        };
        assert_eq!(upsert!($run, deliveries, first, moved, false), (Updated, 10, 8));
        assert_eq!(upsert!($run, deliveries, first, DeliveryUpdate::default(), false), (Unchanged, 10, 8));
        assert_eq!(upsert!($run, deliveries, first, address("8 Main St"), true), (Unchanged, 10, 8));
        assert_eq!(upsert!($run, deliveries, first, address("9 Main St"), true), (Updated, 11, 8));
        let run_driven = DeliveryUpdate {
            proof: Some(text("signed")),
            run_version: Some(9),
            ..Default::default()
        };
        assert_eq!(upsert!($run, deliveries, first, run_driven, false), (Updated, 11, 9));
        assert_eq!(upsert!($run, deliveries, fresh, address("1 New St"), false), (Inserted, 0, 0));

        // A fence with no covers advances on every update.
        let parcel = ParcelCreate {
            id: Some(key(20)),
            tracking_code: "TRK-1".to_owned(),
            weight_grams: 500,
            note: None,
            version: None,
        };
        let created = $run!(parcels.create_one(&parcel)).expect("create the parcel");
        assert_eq!(created.version, 0);
        let noted = |note: &str| ParcelUpdate {
            note: Some(text(note)),
            ..Default::default()
        };
        let weighed = |grams: i64| ParcelUpdate {
            weight_grams: Some(grams),
            ..Default::default()
        };
        assert_eq!($run!(parcels.update_one(&key(20), &noted("fragile"), &plain)).unwrap().version, 1);
        let lighter = ParcelUpdate {
            weight_grams: Some(510),
            note: Some(None),
            ..Default::default()
        };
        assert_eq!($run!(parcels.update_one(&key(20), &lighter, &plain)).unwrap().version, 2);
        let by_code = $run!(parcels.upsert_one(&parcel, &["trackingCode"], &weighed(520), UpsertOptions::default()))
            .expect("upsert by trackingCode");
        assert_eq!((by_code.status, by_code.row.version), (Updated, 3));
        let kept = $run!(parcels.upsert_one(&parcel, &["trackingCode"], &ParcelUpdate::default(), UpsertOptions::default()))
            .expect("upsert that sets no field");
        assert_eq!((kept.status, kept.row.version), (Unchanged, 3));
        assert_eq!($run!(parcels.update_many(None, &noted("this side up"))).unwrap(), 1);
        assert_eq!($run!(parcels.get_one(&key(20))).unwrap().version, 4);
        let counted = |note: &str, version: i64| ParcelUpdate {
            note: Some(text(note)),
            version: Some(version),
            ..Default::default()
        };
        assert_eq!($run!(parcels.update_one(&key(20), &counted("heavy", 5), &plain)).unwrap().version, 5);
        let refused = $run!(parcels.update_one(&key(20), &counted("light", 5), &plain));
        assert!(refused.is_err(), "version 5 over 5 was written: {refused:?}");
        assert_eq!($run!(parcels.get_one(&key(20))).unwrap().version, 5);

        // A soft delete stamps deletedAt, which Depot's fence covers; a
        // write of capacity alone advances nothing.
        let (north, south, east) = (30, 31, 32);
        for (n, name) in [(north, "North"), (south, "South"), (east, "East")] {
            $run!(depots.create_one(&depot(n, name))).expect("create the depot");
        }
        let sized = |capacity: i64, name: Option<&str>| DepotUpdate {
            capacity: Some(capacity),
            name: name.map(str::to_owned),
            ..Default::default()
        };
        $run!(depots.update_one(&key(north), &sized(12, None), &plain)).expect("resize the depot");
        assert_eq!(revision!($run, depots, north), (0, false));
        $run!(depots.update_one(&key(north), &sized(14, Some("North Yard")), &plain)).expect("rename the depot");
        assert_eq!(revision!($run, depots, north), (1, false));
        $run!(depots.delete_one(&key(north), &plain)).expect("delete the depot");
        assert_eq!(revision!($run, depots, north), (2, true));
        assert!(matches!(
            $run!(depots.delete_one(&key(north), &plain)),
            Err(Error::NotFound(_))
        ));
        assert_eq!(revision!($run, depots, north), (2, true));
        $run!(depots.delete_one(&key(south), &plain)).expect("delete another depot");
        assert_eq!(revision!($run, depots, south), (1, true));
        assert_eq!(revision!($run, depots, east), (0, false));

        // A table with no fence is written as before.
        let renamed = CourierUpdate {
            name: text("Ada L."),
        };
        assert_eq!($run!(couriers.update_one(&ada.id, &renamed, &plain)).unwrap().name, "Ada L.");
    }};
}

#[test]
fn the_repositories_advance_each_fence_on_sqlite() {
    let connection = rusqlite::Connection::open_in_memory().expect("open");
    connection
        .execute_batch(include_str!("../create.sql"))
        .expect("create the tables");
    let triggers: i64 = connection
        .query_row(
            "SELECT count(*) FROM sqlite_master WHERE type = 'trigger' AND name LIKE '%\\_fence' ESCAPE '\\'",
            [],
            |row| row.get(0),
        )
        .expect("read the triggers");
    assert_eq!(triggers, 4, "a trigger for each of the four fences");
    println!("SQLite holds {triggers} fence triggers");
    let orm = sqlite::Repositories::new(&connection)
        .expect("the repositories")
        .acting(Some(ADA.to_owned()));
    script!(orm, at_once);
}

#[tokio::test]
async fn the_repositories_advance_each_fence_on_postgres() {
    let dsn = std::env::var("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL").expect("the database's URL");
    let schema = format!("orm_fences_rust_{}", std::process::id());
    let mut config: tokio_postgres::Config = dsn.parse().expect("the database's URL");
    let (admin, connection) = config.connect(tokio_postgres::NoTls).await.expect("connect");
    tokio::spawn(connection);
    admin
        .batch_execute(&format!("CREATE SCHEMA {schema}"))
        .await
        .expect("create the schema");
    config.options(format!("-c search_path={schema},public"));
    let manager = deadpool_postgres::Manager::new(config, tokio_postgres::NoTls);
    let pool = deadpool_postgres::Pool::builder(manager).max_size(2).build().expect("the pool");
    {
        let client = pool.get().await.expect("a connection");
        client
            .batch_execute(include_str!("../postgres/create.sql"))
            .await
            .expect("create the tables");
        let triggers: i64 = client
            .query_one(
                "SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND NOT t.tgisinternal AND t.tgname LIKE '%\\_fence'",
                &[&schema],
            )
            .await
            .expect("read the triggers")
            .get(0);
        assert_eq!(triggers, 4, "a trigger for each of the four fences");
        println!("Postgres holds {triggers} fence triggers");
        let orm = postgres::Repositories::new(&client)
            .expect("the repositories")
            .acting(Some(ADA.to_owned()));
        script!(orm, awaited);
    }
    drop(pool);
    admin
        .batch_execute(&format!("DROP SCHEMA {schema} CASCADE"))
        .await
        .expect("drop the schema");
}
