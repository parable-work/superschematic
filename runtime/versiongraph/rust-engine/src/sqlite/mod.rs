//! The SQLite adapter (D32): the fixed layout ([`crate::fixed`]) on SQLite,
//! under the names it has had since D32, with the SQLite-only parts: the
//! version and JSON check, SQLite's result codes, and the default client
//! over rusqlite ([`Rusqlite`], the `rusqlite` feature, off by default).
//! [`Adapter`] is [`FixedLayout`] and [`SqliteStorage`] is [`FixedStorage`];
//! [`Adapter::new`] reads a descriptor for the layout on SQLite.

#[cfg(feature = "rusqlite")]
mod binding;
#[cfg(all(test, feature = "rusqlite"))]
mod write_parity;

#[cfg(feature = "rusqlite")]
pub use binding::Rusqlite;

pub use crate::fixed::{
    default_table_name, micros_to_date_time, Client, ClientError, Clock, Conn, Options, SqlValue,
    TableName, MAX_MICROS, TABLES,
};
use crate::fixed::{failed, Dialect, FixedLayout, FixedStorage};
use crate::Error;

/// The SQLite adapter: the fixed layout's statements for one graph.
/// [`Adapter::new`] reads a descriptor for the layout on SQLite.
pub type Adapter = FixedLayout;

/// An [`Adapter`] bound to a [`Client`]: the graph's
/// [`crate::storage::Storage`].
pub type SqliteStorage<C> = FixedStorage<C>;

/// `SQLITE_BUSY`: another connection holds the lock past the busy timeout.
pub const SQLITE_BUSY: i32 = 5;

/// `SQLITE_CONSTRAINT_UNIQUE`: a unique index refused a row.
pub const SQLITE_CONSTRAINT_UNIQUE: i32 = 2067;

/// The oldest SQLite the layout and the statements run on, as
/// `(major, minor, patch)`: 3.37.0, which added `STRICT` tables;
/// `RETURNING` needs 3.35.0. The statements also read lists through
/// `json_each` and `json_extract`, which SQLite builds in from 3.38.0 and
/// 3.37 has in builds with JSON1, and which a later build can still leave
/// out, so the adapter checks that they work as well ([`JSON_PROBE`]).
pub const MIN_SQLITE_VERSION: (u32, u32, u32) = (3, 37, 0);

/// A statement that uses the JSON functions the adapter's statements use,
/// and returns 1 where they work: a SQLite built without them refuses it.
pub const JSON_PROBE: &str = "SELECT json_extract(p.value, '$[0]') FROM json_each('[[1]]') AS p";

/// The statements that create the layout on SQLite, each table and index
/// under the name `table_name` gives it ([`default_table_name`] for the
/// default names): one statement each, `CREATE TABLE IF NOT EXISTS` or
/// `CREATE [UNIQUE] INDEX IF NOT EXISTS`, with no trigger and no
/// transaction control, for a caller that runs its own migrations.
/// [`FixedLayout::create_tables`] runs them; [`crate::fixed::layout`] gives
/// them in either dialect.
pub fn layout(table_name: &dyn Fn(&str) -> String) -> Result<Vec<String>, Error> {
    crate::fixed::layout(Dialect::Sqlite, table_name)
}

/// Parses SQLite's version text (`3.46.1`) as `(major, minor, patch)`.
fn parse_version(text: &str) -> Option<(u32, u32, u32)> {
    let mut parts = text.trim().split('.');
    let major = parts.next()?.parse().ok()?;
    let minor = parts.next()?.parse().ok()?;
    let patch = match parts.next() {
        Some(patch) => patch.parse().ok()?,
        None => 0,
    };
    Some((major, minor, patch))
}

/// Refuses a SQLite older than [`MIN_SQLITE_VERSION`], with a clear error.
pub(crate) async fn check_version<C: Client + ?Sized>(client: &C) -> Result<(), Error> {
    let rows = client
        .exec("SELECT sqlite_version()")
        .await
        .map_err(|e| failed(Dialect::Sqlite, "read the SQLite version", e))?;
    let text = match rows.first().and_then(|row| row.first()) {
        Some(SqlValue::Text(text)) => text.clone(),
        other => {
            return Err(Error::Invalid(format!(
                "sqlite: sqlite_version() returned {other:?}, not a version"
            )))
        }
    };
    let Some(version) = parse_version(&text) else {
        return Err(Error::Invalid(format!(
            "sqlite: sqlite_version() returned {text:?}, not a version"
        )));
    };
    if version < MIN_SQLITE_VERSION {
        let (major, minor, patch) = MIN_SQLITE_VERSION;
        return Err(Error::Invalid(format!(
            "sqlite: the connection runs SQLite {text}, and the adapter needs {major}.{minor}.{patch} or later \
             (STRICT tables and RETURNING)"
        )));
    }
    let probed = client.exec(JSON_PROBE).await;
    if !matches!(probed.as_deref(), Ok([row]) if row.as_slice() == [SqlValue::Int(1)]) {
        let why = match probed {
            Ok(rows) => format!("it returned {rows:?}"),
            Err(error) => error.to_string(),
        };
        return Err(Error::Invalid(format!(
            "sqlite: the connection's SQLite {text} lacks json_each and json_extract, which the adapter's \
             statements use (built in from 3.38.0, and in 3.37 with JSON1): {why}"
        )));
    }
    Ok(())
}

/// SQLite's extended result code of an error the adapter or its client
/// returned (`SQLITE_BUSY` is 5), when SQLite gave one.
pub fn result_code(error: &Error) -> Option<i32> {
    match error {
        Error::Storage(error) => error.downcast_ref::<ClientError>().and_then(|e| e.code),
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use std::sync::Arc;

    use async_trait::async_trait;
    use serde_json::Value;

    use super::*;

    fn fixture() -> Value {
        let path = concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../testdata/fixture/recipe.json"
        );
        serde_json::from_str(&std::fs::read_to_string(path).expect("read the fixture"))
            .expect("the fixture is JSON")
    }

    fn graph() -> Options {
        Options {
            graph: "recipe".to_owned(),
            ..Options::default()
        }
    }

    /// How a test client runs the JSON probe: as a build whose JSON
    /// functions work, as one without them, or returning other rows.
    #[derive(Clone)]
    enum Json {
        Works,
        Missing,
        Returns(Vec<Vec<SqlValue>>),
    }

    /// A client that reports one SQLite version, runs the JSON probe as
    /// `Json` says, and runs nothing else.
    struct Version(&'static str, Json);

    #[async_trait]
    impl Client for Version {
        async fn begin(&self) -> Result<Box<dyn Conn + '_>, ClientError> {
            Err(ClientError::new("no transactions here"))
        }

        async fn exec(&self, sql: &str) -> Result<Vec<Vec<SqlValue>>, ClientError> {
            match (sql, &self.1) {
                ("SELECT sqlite_version()", _) => Ok(vec![vec![SqlValue::Text(self.0.to_owned())]]),
                (JSON_PROBE, Json::Works) => Ok(vec![vec![SqlValue::Int(1)]]),
                (JSON_PROBE, Json::Missing) => Err(ClientError {
                    code: Some(1),
                    source: "no such function: json_each".into(),
                }),
                (JSON_PROBE, Json::Returns(rows)) => Ok(rows.clone()),
                (other, _) => panic!("ran {other}"),
            }
        }
    }

    /// The adapter refuses a SQLite older than the layout and its statements
    /// need, before it runs anything, with a clear error: one before
    /// 3.37.0, compared as numbers, and one of any version whose JSON
    /// functions fail the probe or return anything but its one row of 1. It
    /// takes 3.37.0 and later with them.
    #[tokio::test]
    async fn the_adapter_refuses_an_older_sqlite_and_one_without_json() {
        let adapter = Arc::new(Adapter::new(&fixture().to_string(), graph()).expect("adapter"));
        let older = "and the adapter needs 3.37.0 or later";
        let lacks = "lacks json_each and json_extract";
        let cases = [
            ("3.36.0", Json::Works, older),
            ("3.9.0", Json::Works, older),
            ("2.8.17", Json::Works, older),
            ("3.37.2", Json::Missing, lacks),
            ("3.46.1", Json::Missing, lacks),
            ("3.46.1", Json::Returns(vec![vec![SqlValue::Int(2)]]), lacks),
            ("3.46.1", Json::Returns(vec![vec![SqlValue::Null]]), lacks),
            (
                "3.46.1",
                Json::Returns(vec![vec![SqlValue::Text("1".to_owned())]]),
                lacks,
            ),
            ("3.46.1", Json::Returns(Vec::new()), lacks),
            (
                "3.46.1",
                Json::Returns(vec![vec![SqlValue::Int(1)], vec![SqlValue::Int(1)]]),
                lacks,
            ),
            (
                "3.46.1",
                Json::Returns(vec![vec![SqlValue::Int(1), SqlValue::Int(1)]]),
                lacks,
            ),
        ];
        for (version, json, refuse) in cases {
            let error = check_version(&Version(version, json.clone()))
                .await
                .expect_err(version);
            assert!(error.to_string().contains(refuse), "{version}: {error}");
            assert!(error.to_string().contains(version), "{version}: {error}");
            let refused = adapter
                .create_tables(&Version(version, json.clone()))
                .await
                .expect_err(version);
            assert!(refused.to_string().contains(refuse), "{refused}");
            let refused = adapter.storage(Version(version, json)).await.map(|_| ());
            assert!(
                refused
                    .as_ref()
                    .is_err_and(|e| e.to_string().contains(refuse)),
                "{refused:?}"
            );
        }
        for version in ["3.37.0", "3.37.2", "3.38", "3.46.1", "3.100.0", "4.0.0"] {
            check_version(&Version(version, Json::Works))
                .await
                .expect(version);
        }
        for garbage in ["", "three", "3.x.0"] {
            let error = check_version(&Version(garbage, Json::Works))
                .await
                .expect_err(garbage);
            assert!(
                error.to_string().contains("not a version"),
                "{garbage:?}: {error}"
            );
        }
    }
}
