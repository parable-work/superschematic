//! fixture-access-db's per-record access in the Rust repositories on a
//! SQLite file (D156, D182, #611), run by TestRustAccessOnSQLite: the
//! build's SQLite DDL applied to a file of the test's own, and the cases of
//! TestRustAccessEndToEnd on Postgres held to the repositories, the grants'
//! authenticator and the access log over it. An audience is the JSON array
//! the file's triggers keep, which a read reaches through json_each.
//!
//! Each call is polled once: the SQLite repositories never wait, and with
//! no tokio runtime the file's work runs on the caller's thread.

use std::future::Future;
use std::pin::pin;
use std::sync::Arc;
use std::task::{Context, Poll, Waker};
use std::time::{Duration, SystemTime};

use runtime::access::sqlite::{access_log_of, active_grants, authenticate_grant, issue_grant, revoke_grant, GrantAuthenticator};
use runtime::access::routes::{AccessGrantsOperation, AccessGrantsQuery};
use runtime::access::{ActiveGrantsFilter, EndsWhen, IssueGrant, Logged};
use runtime::orm::routes::{TableOperation, TableRequest};
use runtime::orm::sqlite::{rusqlite, Database, Repositories};
use runtime::orm::{AccessCaller, Error, FieldKeyring, FindOptions, Values};
use runtime::{ActingFor, Authenticator, Principal, PrincipalKind};
use serde_json::{json, Value};

/// The parity vectors' key (runtime/schema/testdata/fieldcrypt_parity.json).
const KEYS: &str = "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA=";

const BASE62: &[u8; 62] = b"0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";

/// The UUID 00000000-0000-4000-8000-<n> in its wire form, base62.
fn uuid(n: u128) -> String {
    let mut number = (0x4000u128 << 64) | (0x8000u128 << 48) | n;
    let mut digits = Vec::new();
    while number > 0 {
        digits.push(BASE62[(number % 62) as usize]);
        number /= 62;
    }
    digits.reverse();
    String::from_utf8(digits).unwrap()
}

/// The answer of work that never waits.
fn ready<T>(work: impl Future<Output = T>) -> T {
    match pin!(work).as_mut().poll(&mut Context::from_waker(Waker::noop())) {
        Poll::Ready(out) => out,
        Poll::Pending => panic!("the work waited"),
    }
}

fn values(value: Value) -> Values {
    value.as_object().unwrap().clone()
}

fn case(name: &str) {
    println!("case: {name}");
}

/// Whom the repositories read and write as.
#[derive(Clone)]
enum Who {
    /// The system, which reads every row.
    System,
    /// A caller, whose audience and grants they keep to.
    As(Box<Principal>),
}

fn user(id: &str) -> Who {
    Who::As(Box::new(Principal::new(id, Vec::<String>::new())))
}

fn agent(run: &str, for_whom: &str) -> Who {
    Who::As(Box::new(Principal::new(run, Vec::<String>::new()).of_kind(PrincipalKind::Agent).acting_for(ActingFor::user(for_whom))))
}

struct File {
    database: Arc<Database>,
    keys: Arc<FieldKeyring>,
    _dir: TempDir,
}

/// A directory of the test's own, removed when it ends.
struct TempDir(std::path::PathBuf);

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

impl File {
    fn new() -> Self {
        let nanos = SystemTime::now().duration_since(SystemTime::UNIX_EPOCH).unwrap().as_nanos();
        let dir = std::env::temp_dir().join(format!("access-rust-sqlite-{nanos}"));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("access.db");
        let connection = rusqlite::Connection::open(&path).unwrap();
        connection.execute_batch(include_str!("../create.sql")).unwrap();
        drop(connection);
        Self {
            database: Arc::new(Database::open(&path).unwrap()),
            keys: Arc::new(FieldKeyring::parse(KEYS).expect("the parity key")),
            _dir: TempDir(dir),
        }
    }

    /// Runs `work` in a write transaction of the repositories as `who`,
    /// which commits when it answers and rolls back when it fails.
    fn tx<T>(&self, who: &Who, work: impl FnOnce(&Repositories<'_>) -> Result<T, Error>) -> Result<T, Error> {
        let definition = types::orm::sqlite_definition()?;
        self.database.write(|connection| {
            let repositories = Repositories::new(connection, definition)?.with_keys(self.keys.as_ref());
            let repositories = match who {
                Who::System => repositories.as_system(),
                Who::As(principal) => repositories
                    .acting(Some(principal.subject.clone()))
                    .with_access(AccessCaller::of(Some(principal.as_ref()))),
            };
            work(&repositories)
        })
    }

    fn create(&self, who: &Who, table: &str, row: Value) -> Result<Values, Error> {
        self.tx(who, |r| r.table(table)?.create_one(&values(row)))
    }

    fn titles(&self, who: &Who, table: &str) -> Vec<String> {
        let mut titles: Vec<String> = self
            .tx(who, |r| r.table(table)?.find_many(None, &FindOptions::default()))
            .unwrap()
            .rows
            .iter()
            .map(|row| row.get("title").or_else(|| row.get("name")).and_then(Value::as_str).unwrap().to_owned())
            .collect();
        titles.sort();
        titles
    }

    fn sees(&self, who: &Who, table: &str, key: &str) -> bool {
        match self.tx(who, |r| r.table(table)?.get_one(&Value::from(key))) {
            Ok(_) => true,
            Err(Error::NotFound(_)) => false,
            Err(err) => panic!("{err}"),
        }
    }

    fn update(&self, who: &Who, table: &str, key: &str, update: Value) -> Result<Values, Error> {
        self.tx(who, |r| r.table(table)?.update_one(&Value::from(key), &values(update), &Default::default()))
    }

    fn log_of(&self, kind: &str) -> Vec<Values> {
        let filter = values(json!({"kind": {"eq": kind}}));
        self.tx(&Who::System, |r| r.table("AccessEntry")?.find_many(Some(&filter), &FindOptions::default())).unwrap().rows
    }
}

fn id(row: &Values) -> String {
    row["id"].as_str().unwrap().to_owned()
}

fn request(token: &str) -> http::request::Parts {
    http::Request::builder()
        .uri("/api/requests")
        .header(http::header::AUTHORIZATION, format!("Bearer {token}"))
        .body(())
        .unwrap()
        .into_parts()
        .0
}

#[test]
fn per_record_access_on_sqlite() {
    let file = File::new();
    let definition = types::orm::sqlite_definition().unwrap();
    let (owner_id, other_id, named_id, party_id, admin_id, run_id) = (uuid(0xa1), uuid(0xa2), uuid(0xa3), uuid(0xa4), uuid(0xa5), uuid(0xb1));
    let (owner, other, named) = (user(&owner_id), user(&other_id), user(&named_id));
    let system = Who::System;

    // The contracts, written by the system: contract A lists the party.
    let a = file.create(&system, "Contract", json!({"name": "A", "terms": "Pay on the first"})).unwrap();
    let b = file.create(&system, "Contract", json!({"name": "B", "terms": "Pay on the last"})).unwrap();
    let (contract_a, contract_b) = (id(&a), id(&b));
    file.create(&system, "ContractParty", json!({"contractId": contract_a, "party": party_id})).unwrap();
    let unit_a = id(&file.create(&system, "Unit", json!({"name": "A1", "contractId": contract_a})).unwrap());
    let unit_b = id(&file.create(&system, "Unit", json!({"name": "B1", "contractId": contract_b})).unwrap());
    let request_a = id(&file.create(&system, "Request", json!({"unitId": unit_a, "title": "Fix the door"})).unwrap());
    let request_b = id(&file.create(&system, "Request", json!({"unitId": unit_b, "title": "Paint the hall"})).unwrap());
    file.create(
        &system,
        "Role",
        json!({
            "name": "tenant",
            "permissions": ["requests.read", "requests.write", "contracts.read"],
            "reaches": ["Unit:contract", "Request:unit.contract"],
        }),
    )
    .unwrap();

    // A private row never appears to another user in a list, a read or a
    // count, and a write is held as a read is.
    let mine = file.create(&owner, "Folder", json!({"name": "Mine", "owner": owner_id, "audience": "private"})).unwrap();
    let secret = file.create(&owner, "Document", json!({"folderId": mine["id"], "title": "Secret plan"})).unwrap();
    assert_eq!(secret["audience"], "inherit");
    let secret_id = id(&secret);
    file.create(&owner, "Document", json!({"title": "Public plan", "audience": "everyone"})).unwrap();
    assert_eq!(file.titles(&owner, "Document"), ["Public plan", "Secret plan"]);
    assert_eq!(file.titles(&other, "Document"), ["Public plan"]);
    assert!(!file.sees(&other, "Document", &secret_id));
    assert!(file.sees(&owner, "Document", &secret_id));
    assert!(!file.titles(&other, "Folder").contains(&"Mine".to_owned()));
    assert!(matches!(file.update(&other, "Document", &secret_id, json!({"title": "Mine now"})), Err(Error::NotFound(_))));
    let counted = file.tx(&other, |r| r.table("Document")?.count(None)).unwrap();
    assert_eq!(counted, 1);
    // The repositories of no caller read every row, as a job's do.
    assert!(file.sees(&system, "Document", &secret_id));
    case("a private row never appears to another user in a list, a read or a count");

    // A named row is visible to its owner and to the principals it names.
    let shared = file
        .create(&owner, "Document", json!({"title": "Shared notes", "audience": "named", "audienceNamed": [named_id]}))
        .unwrap();
    let shared_id = id(&shared);
    assert!(file.sees(&owner, "Document", &shared_id));
    assert!(file.sees(&named, "Document", &shared_id));
    assert!(!file.sees(&other, "Document", &shared_id));
    assert!(file.titles(&named, "Document").contains(&"Shared notes".to_owned()));
    assert!(!file.titles(&other, "Document").contains(&"Shared notes".to_owned()));
    case("a named row is visible to its owner and to the principals it names");

    // A document inherits its folder's audience, and follows it when the
    // folder's changes: the file's trigger cascades it.
    let team = id(&file.create(&owner, "Folder", json!({"name": "Team", "owner": owner_id})).unwrap());
    let doc = id(&file.create(&owner, "Document", json!({"folderId": team, "title": "Team doc"})).unwrap());
    assert!(file.sees(&other, "Document", &doc));
    file.update(&owner, "Folder", &team, json!({"audience": "private"})).unwrap();
    assert!(!file.sees(&other, "Document", &doc));
    assert!(file.sees(&owner, "Document", &doc));
    file.update(&owner, "Folder", &team, json!({"audience": "named", "audienceNamed": [other_id]})).unwrap();
    assert!(file.sees(&other, "Document", &doc));
    assert!(!file.sees(&named, "Document", &doc));
    // A document that leaves its folder's audience keeps its own.
    file.update(&owner, "Document", &doc, json!({"audience": "everyone"})).unwrap();
    file.update(&owner, "Folder", &team, json!({"audience": "private"})).unwrap();
    assert!(file.sees(&named, "Document", &doc));
    case("a document inherits its folder's audience, and follows it when the folder's changes");

    // An agent run sees what whom it acts for sees, and writes no
    // read-only row.
    let (for_owner, for_other) = (agent(&run_id, &owner_id), agent(&run_id, &other_id));
    assert!(file.sees(&for_owner, "Document", &secret_id));
    assert!(!file.sees(&for_other, "Document", &secret_id));
    assert!(matches!(file.update(&for_owner, "Document", &secret_id, json!({"title": "Edited"})), Err(Error::NotFound(_))));
    let open = id(&file
        .create(&owner, "Document", json!({"title": "Agent may edit", "audience": "everyone", "agentAccess": "followRole"}))
        .unwrap());
    assert_eq!(file.update(&for_owner, "Document", &open, json!({"title": "Edited by a run"})).unwrap()["title"], "Edited by a run");
    let closed = id(&file.create(&owner, "Document", json!({"title": "No agents", "audience": "everyone", "agentAccess": "none"})).unwrap());
    assert!(!file.sees(&for_owner, "Document", &closed));
    assert!(file.sees(&owner, "Document", &closed));
    case("an agent run sees what whom it acts for sees, and writes no read-only row");

    // A party with a grant reaches only the rows its role's paths allow.
    let party = uuid(0xc1);
    let issue = |holder: &str, contract: &str, ends_at: Option<SystemTime>, ends_when: Option<EndsWhen>| {
        file.tx(&system, |r| {
            issue_grant(
                r,
                &IssueGrant {
                    holder: holder.to_owned(),
                    holder_kind: PrincipalKind::Party,
                    role: "tenant".to_owned(),
                    target_type: "Contract".to_owned(),
                    target_id: contract.to_owned(),
                    starts_at: None,
                    ends_at,
                    ends_when,
                },
            )
        })
        .unwrap()
    };
    let issued = issue(&party, &contract_a, None, None);
    assert!(issued.token.starts_with("grant_"));
    let principal = file
        .database
        .read(|connection| authenticate_grant(connection, definition, &issued.token, SystemTime::now()))
        .unwrap();
    assert_eq!((principal.subject.as_str(), principal.kind), (party.as_str(), PrincipalKind::Party));
    let holder = Who::As(Box::new(principal));
    assert_eq!(file.titles(&holder, "Request"), ["Fix the door"]);
    assert!(file.sees(&holder, "Request", &request_a));
    assert!(!file.sees(&holder, "Request", &request_b));
    assert_eq!(file.titles(&holder, "Unit"), ["A1"]);
    assert!(file.sees(&holder, "Contract", &contract_a));
    assert!(!file.sees(&holder, "Contract", &contract_b));
    // A table the role's paths do not reach has no rows for it.
    assert!(file.titles(&holder, "Document").is_empty());
    // A row it makes is one its grant reaches, or no row at all.
    let made = file.create(&holder, "Request", json!({"unitId": unit_a, "title": "Leaking tap"})).unwrap();
    assert_eq!(made["unitId"], Value::from(unit_a.clone()));
    let refused = file.create(&holder, "Request", json!({"unitId": unit_b, "title": "Not mine"}));
    assert!(matches!(refused, Err(Error::AccessRefused(_))), "{refused:?}");
    assert!(!file.titles(&system, "Request").contains(&"Not mine".to_owned()));
    case("a party with a grant reaches only the rows its role's paths allow");

    // An expired or revoked grant is refused on the very next call,
    // through the grants' authenticator over the file, whose calls the
    // access log records.
    let tables = types::orm::sqlite::tables(Arc::clone(&file.database)).with_keys(file.keys.clone());
    let grants = GrantAuthenticator::new(tables.clone()).unwrap();
    let log = access_log_of(&tables).unwrap().expect("the database holds an access log");
    let logged = Logged::new(grants, Arc::new(log));
    let status = |token: &str| match ready(logged.authenticate(&request(token))) {
        Ok(Some(principal)) => {
            ready(logged.called(Some(&principal), "listRequests", &http::Method::GET)).unwrap();
            200
        }
        Ok(None) => 0,
        Err(err) => err.status.as_u16(),
    };
    assert_eq!(status(&issued.token), 200);
    let calls = file.log_of("grantCall");
    assert_eq!(calls.len(), 1, "{calls:?}");
    assert_eq!((calls[0]["principal"].as_str(), calls[0]["principalKind"].as_str()), (Some(party.as_str()), Some("party")));
    assert_eq!(calls[0]["credential"], issued.grant["id"]);
    // Revoked.
    let revoked = issue(&uuid(0xc2), &contract_a, None, None);
    assert_eq!(status(&revoked.token), 200);
    let revoked_id = id(&revoked.grant);
    file.database
        .write(|connection| revoke_grant(connection, definition, &revoked_id, SystemTime::now()))
        .unwrap();
    assert_eq!(status(&revoked.token), 401);
    // Ended by its date.
    let dated = issue(&uuid(0xc3), &contract_a, Some(SystemTime::now() + Duration::from_millis(400)), None);
    assert_eq!(status(&dated.token), 200);
    std::thread::sleep(Duration::from_millis(500));
    assert_eq!(status(&dated.token), 401);
    // Ended by its target reaching a state.
    let closing = issue(&uuid(0xc4), &contract_b, None, Some(EndsWhen { field: "status".to_owned(), state: "closed".to_owned() }));
    assert_eq!(status(&closing.token), 200);
    file.update(&system, "Contract", &contract_b, json!({"status": "closed"})).unwrap();
    assert_eq!(status(&closing.token), 401);
    // An unknown token is refused as an ended one is.
    assert_eq!(status("grant_unknown"), 401);
    // The read of the active grants holds only the first grant still.
    let active = file
        .database
        .read(|connection| active_grants(connection, definition, &ActiveGrantsFilter::default()))
        .unwrap();
    assert_eq!(active.iter().map(|grant| grant["holder"].clone()).collect::<Vec<_>>(), [Value::from(party.clone())]);
    case("an expired or revoked grant is refused on the very next call");

    // A reveal opens the value for a holder of its permission and writes
    // an access-log row, through a table operation the file serves.
    let read: &'static TableOperation = Box::leak(Box::new(
        TableOperation::parse(
            r#"{"kind": "read", "type": "Contract", "name": "getContract", "key": "id", "view": ["id", "name", "terms"], "decrypted": ["terms"], "reveal": {"terms": "contracts.reveal"}}"#,
        )
        .unwrap(),
    ));
    let mut path = TableRequest::default();
    path.path_arg("id", &contract_a).unwrap();
    let view = |principal: Principal| ready(tables.serve(read, Some(principal), path.clone())).unwrap().row().unwrap();
    let masked = view(Principal::new(owner_id.clone(), ["contracts.read"]));
    assert_eq!(masked["terms"], "[secret]");
    assert!(file.log_of("reveal").is_empty());
    let opened = view(Principal::new(admin_id.clone(), ["contracts.read", "contracts.reveal"]));
    assert_eq!(opened["terms"], "Pay on the first");
    let reveals = file.log_of("reveal");
    assert_eq!(reveals.len(), 1, "{reveals:?}");
    assert_eq!(reveals[0]["principal"], Value::from(admin_id.clone()));
    assert_eq!(reveals[0]["principalKind"], "user");
    assert_eq!(reveals[0]["operation"], "getContract");
    assert_eq!(reveals[0]["targetType"], "Contract");
    assert_eq!(reveals[0]["targetId"], Value::from(contract_a.clone()));
    assert_eq!(reveals[0]["field"], "terms");
    case("a reveal opens the value for a holder of its permission and writes an access-log row");

    // The active grants list for an administrator, the target owner and
    // the holder, and are refused to anyone else (#735): the route the
    // generated router serves, run over the file's tables.
    let route = AccessGrantsOperation::new("access.admin", &[("Folder", "owner"), ("Document", "createdBy"), ("Request", "createdBy")]);
    let folder = file.create(&system, "Folder", json!({"name": "Shared out", "owner": owner_id})).unwrap();
    let folder_id = id(&folder);
    let (holder_a, holder_b) = (uuid(0xe1), uuid(0xe2));
    let grant = |holder: &str, target_type: &str, target_id: &str, ends_at: Option<SystemTime>| {
        id(&file
            .tx(&system, |r| {
                issue_grant(
                    r,
                    &IssueGrant {
                        holder: holder.to_owned(),
                        holder_kind: PrincipalKind::Party,
                        role: "tenant".to_owned(),
                        target_type: target_type.to_owned(),
                        target_id: target_id.to_owned(),
                        starts_at: None,
                        ends_at,
                        ends_when: None,
                    },
                )
            })
            .unwrap()
            .grant)
    };
    let on_folder_a = grant(&holder_a, "Folder", &folder_id, None);
    let on_folder_b = grant(&holder_b, "Folder", &folder_id, None);
    let on_contract_a = grant(&holder_a, "Contract", &contract_a, None);
    let ended = grant(&holder_a, "Folder", &folder_id, None);
    file.database.write(|connection| revoke_grant(connection, definition, &ended, SystemTime::now())).unwrap();
    let expired = grant(&holder_a, "Folder", &folder_id, Some(SystemTime::now() - Duration::from_secs(60)));
    let query = |holder: Option<&str>, target_type: Option<&str>, target_id: Option<&str>| AccessGrantsQuery::new(holder.as_ref(), target_type.as_ref(), target_id.as_ref()).unwrap();
    let listed = |principal: Principal, query: AccessGrantsQuery| -> Result<Vec<String>, u16> {
        match ready(tables.active_grants_route(route, Some(principal), query)) {
            Ok(Value::Array(grants)) => {
                assert!(grants.iter().all(|grant| grant.get("tokenHash").is_none() && grant.get("revokedAt").is_none()), "{grants:?}");
                let mut ids: Vec<String> = grants.iter().map(|grant| grant["id"].as_str().unwrap().to_owned()).collect();
                ids.sort();
                Ok(ids)
            }
            Ok(other) => panic!("{other}"),
            Err(err) => Err(err.status.as_u16()),
        }
    };
    let sorted = |mut ids: Vec<String>| -> Result<Vec<String>, u16> {
        ids.sort();
        Ok(ids)
    };
    let administrator = || Principal::new(admin_id.clone(), ["access.admin"]);
    let folder_owner = || Principal::new(owner_id.clone(), Vec::<String>::new());
    let holder_of_a = || Principal::new(holder_a.clone(), Vec::<String>::new()).of_kind(PrincipalKind::Party);
    let someone = || Principal::new(other_id.clone(), Vec::<String>::new());
    // The administrator: every active grant the query keeps.
    assert_eq!(listed(administrator(), query(None, Some("Folder"), Some(&folder_id))), sorted(vec![on_folder_a.clone(), on_folder_b.clone()]));
    assert_eq!(listed(administrator(), query(Some(&holder_a), None, None)), sorted(vec![on_folder_a.clone(), on_contract_a.clone()]));
    let every = listed(administrator(), query(None, None, None)).unwrap();
    assert!([&on_folder_a, &on_folder_b, &on_contract_a].iter().all(|id| every.contains(id)), "{every:?}");
    assert!(!every.contains(&ended) && !every.contains(&expired), "{every:?}");
    // The target's owner: the grants on the row it owns, and no other.
    assert_eq!(listed(folder_owner(), query(None, Some("Folder"), Some(&folder_id))), sorted(vec![on_folder_a.clone(), on_folder_b.clone()]));
    assert_eq!(listed(folder_owner(), query(Some(&holder_b), Some("Folder"), Some(&folder_id))), sorted(vec![on_folder_b.clone()]));
    assert_eq!(listed(folder_owner(), query(None, Some("Contract"), Some(&contract_a))), Err(403));
    assert_eq!(listed(folder_owner(), query(None, None, None)), Err(403));
    // The holder: its own grants, and no other's.
    assert_eq!(listed(holder_of_a(), query(Some(&holder_a), None, None)), sorted(vec![on_folder_a.clone(), on_contract_a.clone()]));
    assert_eq!(listed(holder_of_a(), query(Some(&holder_a), Some("Contract"), None)), sorted(vec![on_contract_a.clone()]));
    assert_eq!(listed(holder_of_a(), query(Some(&holder_b), None, None)), Err(403));
    assert_eq!(listed(holder_of_a(), query(None, Some("Folder"), Some(&folder_id))), Err(403));
    // Anyone else is refused, and a request with no caller is 401.
    assert_eq!(listed(someone(), query(None, Some("Folder"), Some(&folder_id))), Err(403));
    assert_eq!(listed(someone(), query(Some(&holder_a), None, None)), Err(403));
    assert_eq!(ready(tables.active_grants_route(route, None, query(None, None, None))).unwrap_err().status.as_u16(), 401);
    case("the active grants list for an administrator, the target owner and the holder, and are refused to anyone else");
}
