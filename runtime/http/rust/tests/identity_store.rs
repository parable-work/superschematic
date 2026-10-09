//! The identity store over the fixture's tables (`runtime/http/testdata/identity`),
//! on SQLite always and on the Postgres `SUPERSCHEMATIC_IDENTITY_TEST_DATABASE_URL`
//! names with the `identity-postgres` feature: the cases of the Go store's
//! tests, so both stores read and write the tables alike.

mod support;

use std::time::Duration;

use serde_json::Value;
use superschematic_http_runtime::identity::{
    hash_token, Dialect, NewSession, NewUser, Session, SessionRecord, SqlStore, SqlValue, Store,
    StoreError, User,
};
use support::{at, databases, descriptor, fixture, sqlite, sqlite_with, TestDb};

const MISSING: &str = "00000000-0000-4000-8000-000000000000";

async fn create_user(s: &SqlStore, login: &str, name: &str) -> User {
    s.create_user(NewUser {
        login: login.to_owned(),
        name: name.to_owned(),
        password_hash: format!("hash-of-{login}"),
        at: at(Duration::ZERO),
    })
    .await
    .unwrap_or_else(|err| panic!("create {login}: {err}"))
}

async fn session(s: &SqlStore, user: &User, token_hash: &str) -> Session {
    s.create_session(NewSession {
        user_id: user.id.clone(),
        token_hash: token_hash.to_owned(),
        created_at: at(Duration::ZERO),
        expires_at: at(Duration::from_secs(3600)),
    })
    .await
    .unwrap()
}

async fn find_session(s: &SqlStore, token_hash: &str) -> SessionRecord {
    s.find_session(token_hash)
        .await
        .unwrap_or_else(|err| panic!("find session {token_hash}: {err}"))
}

async fn password_hash(s: &SqlStore, login: &str) -> String {
    s.find_login(login).await.unwrap().password_hash
}

fn hash_of(n: u32) -> String {
    hash_token(&format!("token-{n}"))
}

fn strings(values: &[&str]) -> Vec<String> {
    values.iter().map(ToString::to_string).collect()
}

/// A statement in the database's own placeholders.
fn raw(db: &TestDb, sqlite: &str, postgres: &str) -> String {
    match db.client.dialect() {
        Dialect::Sqlite => sqlite.to_owned(),
        Dialect::Postgres => postgres.to_owned(),
    }
}

/// How a store answered: `ok`, or its refusal by name.
fn outcome<T>(result: &Result<T, StoreError>) -> &'static str {
    match result {
        Ok(_) => "ok",
        Err(StoreError::NotFound) => "not found",
        Err(StoreError::LoginTaken) => "login taken",
        Err(StoreError::RoleNameTaken) => "role name taken",
        Err(StoreError::NoRoles) => "no roles",
        Err(StoreError::InvalidLogin { .. }) => "invalid login",
        Err(StoreError::InvalidName { .. }) => "invalid name",
        Err(_) => "failed",
    }
}

fn new_user(login: &str, name: &str) -> NewUser {
    NewUser {
        login: login.to_owned(),
        name: name.to_owned(),
        password_hash: "x".to_owned(),
        at: at(Duration::ZERO),
    }
}

/// A user is created with their credential, keyed by the database, found by
/// a login in any case, and listed by login.
async fn users(s: &SqlStore) {
    let alice = create_user(s, " Alice@Example.COM ", "Alice").await;
    assert_eq!(
        (alice.login.as_str(), alice.name.as_str(), alice.disabled),
        ("alice@example.com", "Alice", false)
    );
    assert!(
        !alice.id.is_empty() && !alice.id.contains('-'),
        "the user's id {:?} is not the base62 form of its Identity.UUID",
        alice.id
    );
    let bob = create_user(s, "bob@example.com", "").await;
    assert_eq!(bob.name, "bob@example.com", "a user without a name");
    users_refused(s).await;

    let rec = s.find_login("ALICE@EXAMPLE.com").await.unwrap();
    assert_eq!(rec.user.id, alice.id);
    assert_eq!(rec.password_hash, "hash-of- Alice@Example.COM ");
    assert_eq!(
        outcome(&s.find_login("carol@example.com").await),
        "not found"
    );
    assert_eq!(outcome(&s.find_login("carol").await), "invalid login");
    let bob_login = s.find_credential(&bob.id).await.unwrap().user.login;
    assert_eq!(bob_login, "bob@example.com");

    let got = s.get_user(&alice.id).await.unwrap();
    assert_eq!((&got.id, &got.login), (&alice.id, &alice.login));
    assert!(got.roles.is_empty());
    for id in ["not a key", MISSING, "1"] {
        assert_eq!(
            outcome(&s.get_user(id).await),
            "not found",
            "get_user({id:?})"
        );
    }
    let users = s.list_users().await.unwrap();
    let ids: Vec<&str> = users.iter().map(|u| u.id.as_str()).collect();
    assert_eq!(ids, [alice.id.as_str(), bob.id.as_str()], "alice, then bob");
}

/// A taken login, one the scalar refuses and a name the name scalar refuses
/// are told apart, and none leaves a user behind.
async fn users_refused(s: &SqlStore) {
    let again = s.create_user(new_user("ALICE@example.com", "Again")).await;
    assert_eq!(outcome(&again), "login taken");
    let invalid = s.create_user(new_user("not an email", "")).await;
    assert_eq!(outcome(&invalid), "invalid login");
    // The fixture's display name is an Identity.Name, a VARCHAR(80): a
    // longer one is refused by its scalar before any write.
    let long = s
        .create_user(new_user("dave@example.com", &"d".repeat(81)))
        .await;
    assert_eq!(outcome(&long), "invalid name");
    assert_eq!(
        outcome(&s.find_login("dave@example.com").await),
        "not found"
    );
}

/// A session is found by its token hash with its user, touched once per
/// interval, and revoked alone or with the user's others.
async fn sessions(db: &TestDb, s: &SqlStore) {
    let alice = create_user(s, "alice@example.com", "Alice").await;
    let one = session(s, &alice, &hash_of(1)).await;
    let two = session(s, &alice, &hash_of(2)).await;
    let three = session(s, &alice, &hash_of(3)).await;

    let rec = find_session(s, &hash_of(1)).await;
    assert_eq!(rec.session.id, one.id);
    assert!(!one.id.contains('-'), "{}", one.id);
    assert_eq!(
        (
            rec.user.id.as_str(),
            rec.user.name.as_str(),
            rec.user.login.as_str()
        ),
        (alice.id.as_str(), "Alice", "alice@example.com")
    );
    assert_eq!(
        (rec.session.created_at, rec.session.expires_at),
        (at(Duration::ZERO), at(Duration::from_secs(3600)))
    );
    assert_eq!(
        (rec.session.last_seen_at, rec.session.revoked_at),
        (None, None)
    );
    assert_eq!(outcome(&s.find_session(&hash_of(9)).await), "not found");

    touches(s, &one).await;
    revocations(s, &alice, &one, &two, &three).await;
    if db.client.dialect() == Dialect::Sqlite {
        let rows = db
            .client
            .query(
                r#"SELECT "created_at" FROM "session" WHERE "token_hash" = ?"#,
                &[hash_of(1).into()],
            )
            .await
            .unwrap();
        assert_eq!(
            rows[0][0],
            SqlValue::Text("2026-10-08T12:00:00.000Z".to_owned()),
            "SQLite keeps a time as text to the millisecond"
        );
    }
}

/// The first touch writes lastSeenAt; one within the interval does not; one
/// after it does.
async fn touches(s: &SqlStore, one: &Session) {
    for (seconds, want) in [(1, 1), (30, 1), (120, 120)] {
        let when = at(Duration::from_secs(seconds));
        s.touch_session(&one.id, when, when - Duration::from_secs(60))
            .await
            .unwrap();
        let seen = find_session(s, &hash_of(1)).await.session.last_seen_at;
        assert_eq!(
            seen,
            Some(at(Duration::from_secs(want))),
            "a touch at {seconds}s"
        );
    }
}

/// A session is revoked once; a user's sessions all but the one kept.
async fn revocations(s: &SqlStore, alice: &User, one: &Session, two: &Session, three: &Session) {
    for seconds in [180, 240] {
        s.revoke_session(&one.id, at(Duration::from_secs(seconds)))
            .await
            .unwrap();
    }
    let revoked = find_session(s, &hash_of(1)).await.session.revoked_at;
    assert_eq!(
        revoked,
        Some(at(Duration::from_secs(180))),
        "the first revocation's"
    );
    s.revoke_user_sessions(&alice.id, Some(&three.id), at(Duration::from_secs(300)))
        .await
        .unwrap();
    let second = find_session(s, &hash_of(2)).await.session.revoked_at;
    assert!(second.is_some(), "kept {}", two.id);
    let third = find_session(s, &hash_of(3)).await.session.revoked_at;
    assert!(third.is_none(), "revoked the session it keeps");
}

/// Setting a password revokes the user's sessions but the one kept, and a
/// rehash replaces only the hash it read.
async fn credentials(db: &TestDb, s: &SqlStore) {
    let alice = create_user(s, "alice@example.com", "Alice").await;
    let keep = session(s, &alice, &hash_of(1)).await;
    session(s, &alice, &hash_of(2)).await;

    let at_minute = at(Duration::from_secs(60));
    s.set_password(&alice.id, "new-hash", at_minute, Some(&keep.id))
        .await
        .unwrap();
    assert_eq!(password_hash(s, "alice@example.com").await, "new-hash");
    let kept = find_session(s, &hash_of(1)).await.session.revoked_at;
    let other = find_session(s, &hash_of(2)).await.session.revoked_at;
    assert_eq!((kept.is_some(), other.is_some()), (false, true));

    s.rehash_password(&alice.id, "stale-hash", "rehashed")
        .await
        .unwrap();
    let stale = password_hash(s, "alice@example.com").await;
    assert_eq!(stale, "new-hash", "a rehash from a stale hash");
    s.rehash_password(&alice.id, "new-hash", "rehashed")
        .await
        .unwrap();
    assert_eq!(password_hash(s, "alice@example.com").await, "rehashed");

    disabling(db, s, &alice).await;
    let missing = s.set_password(MISSING, "h", at(Duration::ZERO), None).await;
    assert_eq!(outcome(&missing), "not found");
    let missing = s.set_disabled(MISSING, true, at(Duration::ZERO)).await;
    assert_eq!(outcome(&missing), "not found");
}

/// Disabling a user revokes their sessions in the same transaction, and a
/// user the project wrote without a credential is disabled all the same.
async fn disabling(db: &TestDb, s: &SqlStore, alice: &User) {
    s.set_disabled(&alice.id, true, at(Duration::from_secs(120)))
        .await
        .unwrap();
    let rec = find_session(s, &hash_of(1)).await;
    assert!(
        rec.user.disabled && rec.session.revoked_at.is_some(),
        "{rec:?}"
    );
    s.set_disabled(&alice.id, false, at(Duration::from_secs(180)))
        .await
        .unwrap();
    assert!(!s.get_user(&alice.id).await.unwrap().disabled);

    let insert = raw(
        db,
        r#"INSERT INTO "user" ("email", "display_name") VALUES (?, ?) RETURNING "id""#,
        r#"INSERT INTO "user" ("email", "display_name") VALUES ($1, $2) RETURNING "id""#,
    );
    db.client
        .query(&insert, &["bare@example.com".into(), "Bare".into()])
        .await
        .unwrap();
    let bare = s.find_login("bare@example.com").await.unwrap();
    assert!(
        bare.password_hash.is_empty() && !bare.user.disabled,
        "{bare:?}"
    );
    s.set_disabled(&bare.user.id, true, at(Duration::from_secs(60)))
        .await
        .unwrap();
    let bare = s.find_login("bare@example.com").await.unwrap();
    assert!(
        bare.user.disabled && bare.password_hash.is_empty(),
        "{bare:?}"
    );
}

/// Roles keep their permissions in order, list by name and refuse a taken
/// name.
async fn roles(s: &SqlStore) {
    assert!(s.has_roles(), "the fixture has a UserRole table");
    let writer = s
        .create_role("writer", &strings(&["orders.write", "orders.read"]))
        .await
        .unwrap();
    assert!(!writer.id.contains('-'), "{}", writer.id);
    let admin = s.create_role("admin", &[]).await.unwrap();
    assert_eq!(
        outcome(&s.create_role("writer", &[]).await),
        "role name taken"
    );
    let got = s.get_role(&writer.id).await.unwrap();
    assert_eq!(got.name, "writer");
    assert_eq!(got.permissions, ["orders.write", "orders.read"]);
    let roles = s.list_roles().await.unwrap();
    let names: Vec<&str> = roles.iter().map(|r| r.name.as_str()).collect();
    assert_eq!(names, ["admin", "writer"]);
    assert!(roles[0].permissions.is_empty());

    let taken = s.update_role(&admin.id, "writer", &[]).await;
    assert_eq!(outcome(&taken), "role name taken");
    let permissions = strings(&["identity", "orders"]);
    let updated = s
        .update_role(&admin.id, "administrator", &permissions)
        .await
        .unwrap();
    assert_eq!(
        (updated.name.as_str(), updated.permissions),
        ("administrator", permissions)
    );
    s.update_role(&writer.id, "writer", &strings(&["orders.write"]))
        .await
        .expect("an update keeping the role's own name");
    grants(s, &writer.id, &admin.id).await;
}

/// Grants are idempotent, a user's roles list by name, and deleting a role
/// deletes its grants; a missing user or role is not found.
async fn grants(s: &SqlStore, writer: &str, admin: &str) {
    let alice = create_user(s, "alice@example.com", "Alice").await;
    let bob = create_user(s, "bob@example.com", "Bob").await;
    for (user, role) in [
        (&alice.id, writer),
        (&alice.id, admin),
        (&alice.id, admin),
        (&bob.id, writer),
    ] {
        s.grant_role(user, role, at(Duration::ZERO)).await.unwrap();
    }
    let names = |roles: Vec<superschematic_http_runtime::identity::Role>| -> Vec<String> {
        roles.into_iter().map(|r| r.name).collect()
    };
    assert_eq!(
        names(s.user_roles(&alice.id).await.unwrap()),
        ["administrator", "writer"]
    );
    assert_eq!(s.get_user(&alice.id).await.unwrap().roles.len(), 2);
    let users = s.list_users().await.unwrap();
    assert_eq!(names(users[0].roles.clone()), ["administrator", "writer"]);
    assert_eq!(names(users[1].roles.clone()), ["writer"]);

    s.revoke_role(&alice.id, writer).await.unwrap();
    s.revoke_role(&alice.id, writer)
        .await
        .expect("revoking a role not held");
    assert_eq!(
        names(s.user_roles(&alice.id).await.unwrap()),
        ["administrator"]
    );
    s.delete_role(writer).await.unwrap();
    assert!(s.user_roles(&bob.id).await.unwrap().is_empty());
    assert_eq!(outcome(&s.delete_role(writer).await), "not found");

    missing_rows(s, &alice.id, admin).await;
}

/// A missing user or role, or a malformed id, is not found.
async fn missing_rows(s: &SqlStore, alice: &str, admin: &str) {
    let at0 = at(Duration::ZERO);
    let missing: Vec<(&str, Result<(), StoreError>)> = vec![
        ("get_role", s.get_role(MISSING).await.map(drop)),
        (
            "update_role",
            s.update_role(MISSING, "x", &[]).await.map(drop),
        ),
        (
            "grant_role a missing role",
            s.grant_role(alice, MISSING, at0).await,
        ),
        (
            "grant_role a missing user",
            s.grant_role(MISSING, admin, at0).await,
        ),
        ("revoke_role", s.revoke_role(MISSING, admin).await),
        (
            "grant_role a malformed id",
            s.grant_role(alice, "not a key", at0).await,
        ),
    ];
    for (name, result) in missing {
        assert_eq!(outcome(&result), "not found", "{name}: {result:?}");
    }
}

/// A transaction a dropped future left open is rolled back before the next
/// statement, so its writes do not land and the client goes on working.
async fn abandoned_transaction(db: &TestDb, s: &SqlStore) {
    let alice = create_user(s, "alice@example.com", "Alice").await;
    {
        let mut tx = db.client.begin().await.unwrap();
        let update = raw(
            db,
            r#"UPDATE "user" SET "display_name" = ? WHERE "email" = ?"#,
            r#"UPDATE "user" SET "display_name" = $1 WHERE "email" = $2"#,
        );
        tx.execute(&update, &["Dropped".into(), "alice@example.com".into()])
            .await
            .unwrap();
        // The transaction is dropped with neither a commit nor a rollback.
    }
    assert_eq!(s.get_user(&alice.id).await.unwrap().name, "Alice");
    create_user(s, "bob@example.com", "Bob").await;
}

/// Each case on a fresh store over each database.
#[tokio::test]
async fn the_store_on_each_database() {
    for case in [
        "users",
        "sessions",
        "credentials",
        "roles",
        "abandoned_transaction",
    ] {
        for db in databases().await {
            eprintln!("{case} on {}", db.name);
            let s = db.store(&descriptor(|_| {}));
            match case {
                "users" => users(&s).await,
                "sessions" => sessions(&db, &s).await,
                "credentials" => credentials(&db, &s).await,
                "roles" => roles(&s).await,
                _ => abandoned_transaction(&db, &s).await,
            }
            db.close().await;
        }
    }
}

/// A descriptor without a role table gives a store whose users hold no
/// roles and whose role methods say there are none.
#[tokio::test]
async fn without_roles() {
    let db = sqlite();
    let s = db.store(&descriptor(|d| {
        let d = d.as_object_mut().unwrap();
        d.remove("role");
        d.remove("roleGrant");
    }));
    let alice = create_user(&s, "alice@example.com", "Alice").await;
    assert!(!s.has_roles());
    assert!(s.user_roles(&alice.id).await.unwrap().is_empty());
    assert!(s.get_user(&alice.id).await.unwrap().roles.is_empty());
    assert!(matches!(s.list_roles().await, Err(StoreError::NoRoles)));
    assert!(matches!(
        s.grant_role(&alice.id, &alice.id, at(Duration::ZERO)).await,
        Err(StoreError::NoRoles)
    ));
}

/// A store is not built over a descriptor it cannot read.
#[tokio::test]
async fn refused_descriptors() {
    let db = sqlite();
    let cases: Vec<(&str, String)> = vec![
        (
            "another version",
            descriptor(|d| d["version"] = Value::from(2)),
        ),
        (
            "an empty name",
            descriptor(|d| d["session"]["table"] = Value::from("")),
        ),
        (
            "a NUL in a name",
            descriptor(|d| d["user"]["table"] = Value::from("us\0er")),
        ),
        (
            "an unknown member",
            descriptor(|d| d["extra"] = serde_json::json!({})),
        ),
        (
            "a role without its grant",
            descriptor(|d| {
                d.as_object_mut().unwrap().remove("roleGrant");
            }),
        ),
        (
            "an unknown login scalar",
            descriptor(|d| d["user"]["loginScalar"] = Value::from("Contact.Pager")),
        ),
        (
            "no name scalar",
            descriptor(|d| {
                d["user"].as_object_mut().unwrap().remove("nameScalar");
            }),
        ),
        (
            "no role key scalar",
            descriptor(|d| {
                d["role"].as_object_mut().unwrap().remove("keyScalar");
            }),
        ),
        ("not JSON", "{".to_owned()),
    ];
    for (name, descriptor) in cases {
        assert!(
            SqlStore::new(db.client.clone(), &descriptor, support::catalog()).is_err(),
            "{name}: the store was built"
        );
    }
}

/// When the User trait names no name, the login is the name, and a user is
/// created with the login column alone.
#[tokio::test]
async fn name_is_login() {
    let ddl = fixture("sqlite/create.sql").replacen(
        r#""display_name" TEXT NOT NULL"#,
        r#""display_name" TEXT"#,
        1,
    );
    let db = sqlite_with(&ddl);
    let s = db.store(&descriptor(|d| {
        d["user"]["columns"]["name"] = Value::from("email");
    }));
    let user = create_user(&s, "Alice@Example.com", "Ignored").await;
    assert_eq!(user.name, "alice@example.com");
    let rows = db
        .client
        .query(r#"SELECT "display_name" FROM "user""#, &[])
        .await
        .unwrap();
    assert_eq!(
        rows[0][0],
        SqlValue::Null,
        "the store wrote a column the descriptor does not name"
    );
    session(&s, &user, &hash_of(1)).await;
    assert_eq!(
        find_session(&s, &hash_of(1)).await.user.name,
        "alice@example.com"
    );
}
