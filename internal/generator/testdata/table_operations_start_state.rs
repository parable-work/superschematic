//! The generated Rust server of fixture-workflow-view-api (D109, D116): its
//! router, with no socket under it, serving placeOrder, a create whose body
//! names the state its row starts in, from the Rust repositories of
//! fixture-workflow-db, whose Order is written through the behaviors' core
//! (D103). The cases are the TypeScript server's
//! (start_state_runtime.test.ts), on a SQLite file with the crate's
//! `sqlite` feature and on Postgres without it.

use std::sync::Arc;

use api::database_types::orm::{Caller, Error, FindOptions, Values, WriteOptions};
use api::runtime::{ApiError, Authenticator, Principal, RequestContext};
use api::{build_router, Handlers, Implementations, OrderViewsGetOrderViewArgs, OrderViewsImplementation};
use async_trait::async_trait;
use axum::body::Body;
use axum::Router;
use http::{Method, Request, StatusCode};
use http_body_util::BodyExt;
use serde_json::{json, Value};
use tower::ServiceExt;

/// The scenario's authenticator: Authorization: Bearer <subject> is that
/// subject, holding no permission; no header, no caller.
struct Bearer;

#[async_trait]
impl Authenticator for Bearer {
    async fn authenticate(&self, request: &http::request::Parts) -> Result<Option<Principal>, ApiError> {
        let subject = request
            .headers
            .get(http::header::AUTHORIZATION)
            .and_then(|value| value.to_str().ok())
            .and_then(|value| value.strip_prefix("Bearer "))
            .filter(|subject| !subject.is_empty());
        Ok(subject.map(|subject| Principal::new(subject, Vec::<String>::new())))
    }
}

/// The orders `filter` finds, as their table holds them.
fn found(rows: Result<api::database_types::orm::Found<Values>, Error>) -> Vec<Value> {
    rows.expect("read the orders")
        .rows
        .into_iter()
        .map(Value::Object)
        .collect()
}

fn titled_filter(title: &str) -> Values {
    match json!({"title": {"eq": title}}) {
        Value::Object(filter) => filter,
        _ => unreachable!(),
    }
}

/// Who ships an order: a caller who holds the permission the Workflow's
/// transition names.
fn kim() -> Caller {
    Caller {
        subject: "kim".to_owned(),
        permissions: vec!["orders.ship".to_owned()],
        stands_in: false,
    }
}

/// The database on a SQLite file of the test's own, holding the build's
/// SQLite DDL.
#[cfg(feature = "sqlite")]
mod store {
    use super::*;
    use api::database_types::orm::sqlite::Repositories;
    use api::runtime::orm::sqlite::{rusqlite, Database};

    pub type Db = Arc<Database>;

    pub async fn open() -> Db {
        let dir = std::env::temp_dir().join(format!("start-state-sqlite-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).expect("create the directory");
        let path = dir.join("orders.sqlite");
        rusqlite::Connection::open(&path)
            .and_then(|connection| connection.execute_batch(include_str!("../create.sql")))
            .expect("create the tables");
        Arc::new(Database::open(&path).expect("open the file"))
    }

    pub async fn close(db: Db) {
        let dir = db.path().parent().map(std::path::Path::to_path_buf);
        drop(db);
        if let Some(dir) = dir {
            let _ = std::fs::remove_dir_all(dir);
        }
    }

    pub async fn order(db: &Db, key: Value) -> Result<Values, ApiError> {
        db.read_async(move |connection| {
            Ok::<_, ApiError>(Repositories::new(connection)?.order()?.untyped().get_one(&key)?)
        })
        .await
    }

    pub async fn titled(db: &Db, title: &str) -> Vec<Value> {
        let filter = titled_filter(title);
        db.read_async(move |connection| {
            let orm = Repositories::new(connection)?;
            Ok::<_, Error>(found(orm.order()?.untyped().find_many(Some(&filter), &FindOptions::default())))
        })
        .await
        .expect("read the orders")
    }

    pub async fn ship(db: &Db, key: Value) -> Result<Value, Error> {
        db.write_async(move |connection| {
            let orm = Repositories::new(connection)?.with_caller(kim());
            orm.order()?.untyped().operation(
                "status",
                "transition",
                &key,
                &json!({"to": "shipped"}),
                &WriteOptions::default(),
            )
        })
        .await
    }
}

/// The database in a schema of the test's own on the Postgres
/// SUPERSCHEMATIC_RULES_TEST_DATABASE_URL names, holding the build's
/// Postgres DDL.
#[cfg(not(feature = "sqlite"))]
mod store {
    use super::*;
    use api::database_types::orm::postgres::Repositories;
    use api::runtime::postgres::{self, Pool};
    use api::runtime::stackconfig::Database;

    pub struct Db {
        pub pool: Pool,
        admin: Pool,
        schema: String,
    }

    pub async fn open() -> Db {
        let url = std::env::var("SUPERSCHEMATIC_RULES_TEST_DATABASE_URL").expect("the database's URL");
        let schema = format!("start_state_rust_{}", std::process::id());
        let admin = postgres::connect(&Database::Url(url.clone())).expect("the admin pool");
        let client = admin.get().await.expect("connect");
        client
            .batch_execute(&format!("CREATE SCHEMA {schema}"))
            .await
            .expect("create the schema");
        let separator = if url.contains('?') { '&' } else { '?' };
        let scoped = format!("{url}{separator}options=-c%20search_path%3D{schema}%2Cpublic");
        let pool = postgres::connect(&Database::Url(scoped)).expect("the pool");
        pool.get()
            .await
            .expect("connect")
            .batch_execute(include_str!("../create.sql"))
            .await
            .expect("create the tables");
        Db { pool, admin, schema }
    }

    pub async fn close(db: Db) {
        let client = db.admin.get().await.expect("connect");
        drop(db.pool);
        client
            .batch_execute(&format!("DROP SCHEMA {} CASCADE", db.schema))
            .await
            .expect("drop the schema");
    }

    pub async fn order(db: &Db, key: Value) -> Result<Values, ApiError> {
        let client = db
            .pool
            .get()
            .await
            .map_err(|err| ApiError::service_unavailable(err.to_string()))?;
        Ok(Repositories::new(&client)?.order()?.untyped().get_one(&key).await?)
    }

    pub async fn titled(db: &Db, title: &str) -> Vec<Value> {
        let client = db.pool.get().await.expect("connect");
        let orm = Repositories::new(&client).expect("the repositories");
        let orders = orm.order().expect("the orders");
        found(
            orders
                .untyped()
                .find_many(Some(&titled_filter(title)), &FindOptions::default())
                .await,
        )
    }

    pub async fn ship(db: &Db, key: Value) -> Result<Value, Error> {
        let mut client = db.pool.get().await.expect("connect");
        let transaction = client.transaction().await.expect("begin");
        let shipped = {
            let orm = Repositories::new(&transaction)?.with_caller(kim());
            orm.order()?
                .untyped()
                .operation(
                    "status",
                    "transition",
                    &key,
                    &json!({"to": "shipped"}),
                    &WriteOptions::default(),
                )
                .await?
        };
        transaction.commit().await.expect("commit");
        Ok(shipped)
    }
}

/// The project's implementation of getOrderView, the one operation no
/// generated handler serves: the order, read through the repositories.
struct OrderViews {
    db: Arc<store::Db>,
}

#[async_trait]
impl OrderViewsImplementation for OrderViews {
    async fn get_order_view(
        &self,
        _ctx: RequestContext,
        args: OrderViewsGetOrderViewArgs,
    ) -> Result<api::types::OrderView, ApiError> {
        let key = serde_json::to_value(&args.id).map_err(|err| ApiError::internal(err.to_string()))?;
        let row = store::order(&self.db, key).await?;
        serde_json::from_value(Value::Object(row)).map_err(|err| ApiError::internal(err.to_string()))
    }
}

struct Reply {
    status: StatusCode,
    body: Value,
}

/// Places an order as `subject`, or as no one.
async fn place(router: &Router, body: Value, subject: Option<&str>) -> Reply {
    call(router, Method::POST, "/orders", Some(body), subject).await
}

async fn call(router: &Router, method: Method, path: &str, body: Option<Value>, subject: Option<&str>) -> Reply {
    let mut request = Request::builder().method(method).uri(format!("/api{path}"));
    if let Some(subject) = subject {
        request = request.header(http::header::AUTHORIZATION, format!("Bearer {subject}"));
    }
    let body = match body {
        Some(body) => {
            request = request.header(http::header::CONTENT_TYPE, "application/json");
            Body::from(body.to_string())
        }
        None => Body::empty(),
    };
    let response = router
        .clone()
        .oneshot(request.body(body).expect("a request"))
        .await
        .expect("the router answers");
    let status = response.status();
    let bytes = response.into_body().collect().await.expect("a body").to_bytes();
    let body = if bytes.is_empty() {
        Value::Null
    } else {
        serde_json::from_slice(&bytes).expect("a JSON body")
    };
    Reply { status, body }
}

#[tokio::test(flavor = "multi_thread")]
async fn a_create_names_the_state_its_row_starts_in() {
    let db = Arc::new(store::open().await);
    let handlers = Handlers {
        order_views: Arc::new(OrderViews { db: Arc::clone(&db) }),
        authenticator: Arc::new(Bearer),
    };
    #[cfg(feature = "sqlite")]
    let router = build_router(Implementations::new(handlers, Arc::clone(&*db)));
    #[cfg(not(feature = "sqlite"))]
    let router = build_router(Implementations::new(handlers, db.pool.clone()));
    let sam = Some("sam");

    // A create that names no state leaves the row in the Workflow's
    // initial state: each is a create and nothing more, the first version
    // of its row.
    for body in [
        json!({"title": "Plain"}),
        json!({"title": "Plain", "status": null}),
        json!({"title": "Plain", "status": "draft"}),
    ] {
        let answer = place(&router, body, sam).await;
        assert_eq!(answer.status, StatusCode::OK, "{}", answer.body);
        let data = &answer.body["data"];
        assert!(data["id"].is_string(), "{data}");
        assert_eq!(data["title"], "Plain");
        assert_eq!(data["status"], "draft");
    }
    let plain = store::titled(&db, "Plain").await;
    assert_eq!(plain.len(), 3);
    for row in &plain {
        assert_eq!(row["status"], "draft");
        assert_eq!(row["_version"], 1);
    }

    // A create that names a state moves the row there by the Workflow's
    // transition, in the create's transaction: the row is the one the
    // answer shows, created, then moved, its second version.
    let answer = place(&router, json!({"title": "Prepaid", "status": "paid"}), sam).await;
    assert_eq!(answer.status, StatusCode::OK, "{}", answer.body);
    let data = answer.body["data"].as_object().expect("the order's view");
    assert_eq!(data["title"], "Prepaid");
    assert_eq!(data["status"], "paid");
    assert!(data["statusEnteredAt"].is_string(), "{data:?}");
    let mut members: Vec<&str> = data.keys().map(String::as_str).collect();
    members.sort_unstable();
    assert_eq!(members, ["id", "status", "statusEnteredAt", "title"]);
    let prepaid = store::titled(&db, "Prepaid").await;
    assert_eq!(prepaid.len(), 1);
    assert_eq!(prepaid[0]["id"], data["id"]);
    assert_eq!(prepaid[0]["status"], "paid");
    assert_eq!(prepaid[0]["_version"], 2);
    // The implementation's read of it, through the repositories.
    let id = data["id"].as_str().expect("the order's key");
    let read = call(&router, Method::GET, &format!("/orders/{id}"), None, sam).await;
    assert_eq!(read.status, StatusCode::OK, "{}", read.body);
    assert_eq!(read.body["data"]["status"], "paid");
    // And it moves on from there as any paid order does, for a caller who
    // holds the permission the transition names.
    let shipped = store::ship(&db, data["id"].clone()).await.expect("ship the order");
    assert_eq!(shipped["to"], "shipped");

    // A state the initial one does not lead to is the transition's
    // refusal, and no row is kept.
    let answer = place(&router, json!({"title": "Too far", "status": "shipped"}), sam).await;
    assert_eq!(answer.status, StatusCode::CONFLICT, "{}", answer.body);
    assert_eq!(answer.body["status"], 409);
    assert_eq!(answer.body["code"], "vetoed");
    let detail = answer.body["detail"].as_str().unwrap_or_default();
    assert!(
        detail.contains("no transition leads from draft to shipped; from draft it can move to paid"),
        "{}",
        answer.body
    );
    assert_eq!(answer.body["details"]["vetoCode"], "transition_not_allowed");
    assert!(store::titled(&db, "Too far").await.is_empty());

    // A state the Workflow does not have is refused, and no row is kept.
    let answer = place(&router, json!({"title": "Nowhere", "status": "lost"}), sam).await;
    assert!(answer.status.is_client_error(), "{}", answer.body);
    assert!(answer.body.to_string().contains("lost"), "{}", answer.body);
    assert!(store::titled(&db, "Nowhere").await.is_empty());

    // The route's guard holds.
    let answer = place(&router, json!({"title": "Anonymous", "status": "paid"}), None).await;
    assert_eq!(answer.status, StatusCode::UNAUTHORIZED, "{}", answer.body);
    assert!(store::titled(&db, "Anonymous").await.is_empty());

    drop(router);
    if let Ok(db) = Arc::try_unwrap(db) {
        store::close(db).await;
    }
}
