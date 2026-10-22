//! What one transaction has read (D58, amended): the stored schemas it
//! looked up and the instance rows it read or wrote, kept on the
//! transaction ([`Tx::memo`]) so that every hook of a call that asks for
//! the same schema or instance again reads it from memory, not from the
//! database.
//!
//! An instance is kept as two rows: its own columns ([`COLUMNS`]), and
//! every behavior column of the version that reads it
//! ([`VersionRuntime::columns`]), which each behavior's `columns().get()`
//! takes its own from. Each statement names its columns: a prepared
//! `SELECT *` fails on Postgres once another engine's publish adds a
//! behavior's columns to the table.
//!
//! The cache is the transaction's alone. The storage drops it when a
//! savepoint rolls back, so nothing a rolled-back write left is read, when
//! the transaction ends, and at each end of an engine call inside a host's
//! transaction, whose own statements run between calls. The engine drops
//! an instance's row at each statement that writes it, and the whole cache
//! at each change to the catalog. Every key names its namespace.
//!
//! Only what no other engine can change while the transaction runs is
//! kept, so a replica's writes are seen exactly when they would be without
//! the cache: the catalog, which every transaction reads under the
//! catalog's lock (at least shared) or in one snapshot, and the rows of a
//! namespace the transaction holds still ([`Tx::stable`]).

use std::collections::{HashMap, HashSet};
use std::sync::{Arc, Mutex, PoisonError};

use superschematic_engine_storage::{quote_identifier, Row, SqlValue, Tx};

use crate::errors::Result;
use crate::instances::COLUMNS;
use crate::registry::{SchemaRecord, VersionRuntime, Which};

/// A stored schema by the namespace it was looked up from, its name and
/// which document of it.
type SchemaKey = (String, String, Which);

/// An instance by namespace, schema and id.
type InstanceKey = (String, String, String);

/// The cache one transaction keeps.
#[derive(Default)]
pub(crate) struct Reads {
    schemas: Mutex<HashMap<SchemaKey, Option<Arc<SchemaRecord>>>>,
    /// Each instance's own columns, or `None` for one that does not exist.
    instances: Mutex<HashMap<InstanceKey, Option<Row>>>,
    /// Each instance's behavior columns, as the version that read them
    /// names them (the row's columns).
    columns: Mutex<HashMap<InstanceKey, Row>>,
}

impl Reads {
    /// The document of a schema as looked up from `namespace`, loading it
    /// with `load` when the transaction has not.
    pub fn schema(
        tx: &Tx<'_>,
        namespace: &str,
        name: &str,
        which: Which,
        load: impl FnOnce() -> Result<Option<SchemaRecord>>,
    ) -> Result<Option<Arc<SchemaRecord>>> {
        let reads = tx.memo::<Reads>();
        let key = (namespace.to_owned(), name.to_owned(), which);
        if let Some(found) = lock(&reads.schemas).get(&key) {
            return Ok(found.clone());
        }
        let found = load()?.map(Arc::new);
        lock(&reads.schemas).insert(key, found.clone());
        Ok(found)
    }

    /// Drops everything the transaction kept: after a change to the
    /// catalog, which may change a schema's documents, the columns
    /// behaviors keep on the instances and the instances' own fields.
    pub fn forget_all(tx: &Tx<'_>) {
        tx.forget();
    }

    /// An instance's own columns, or `None` when the namespace has none of
    /// that id.
    pub fn own(tx: &Tx<'_>, namespace: &str, schema: &str, id: &str) -> Result<Option<Row>> {
        let load = || {
            Ok(tx.get(
                format!(
                    "SELECT {COLUMNS} FROM engine_instances WHERE namespace = ? AND schema = ? AND id = ?"
                ),
                &key_params(namespace, schema, id),
            )?)
        };
        if !tx.stable(namespace) {
            return load();
        }
        let reads = tx.memo::<Reads>();
        let key = key_of(namespace, schema, id);
        if let Some(found) = lock(&reads.instances).get(&key) {
            return Ok(found.clone());
        }
        let found = load()?;
        lock(&reads.instances).insert(key, found.clone());
        Ok(found)
    }

    /// An instance's behavior columns that a version composes, by the
    /// database's names, or `None` when the namespace has no such
    /// instance.
    pub fn columns(
        tx: &Tx<'_>,
        namespace: &str,
        schema: &str,
        id: &str,
        runtime: &VersionRuntime,
    ) -> Result<Option<Row>> {
        let load = || {
            Ok(tx.get(
                format!(
                    "SELECT {} FROM engine_instances WHERE namespace = ? AND schema = ? AND id = ?",
                    runtime
                        .columns
                        .iter()
                        .map(|column| quote_identifier(column))
                        .collect::<Vec<_>>()
                        .join(", ")
                ),
                &key_params(namespace, schema, id),
            )?)
        };
        if !tx.stable(namespace) {
            return load();
        }
        let reads = tx.memo::<Reads>();
        let key = key_of(namespace, schema, id);
        if let Some(row) = lock(&reads.columns).get(&key) {
            if row.columns() == runtime.columns.as_slice() {
                return Ok(Some(row.clone()));
            }
        }
        let found = load()?;
        if let Some(row) = &found {
            lock(&reads.columns).insert(key, row.clone());
        }
        Ok(found)
    }

    /// The rows of the instances of a schema in a namespace that have the
    /// ids given, each once and in no order: the ones the transaction has
    /// read from memory, the rest in one statement that reads their
    /// behavior columns too, which it keeps.
    pub fn instances(
        tx: &Tx<'_>,
        namespace: &str,
        schema: &str,
        ids: &[&str],
        runtime: &VersionRuntime,
    ) -> Result<Vec<Row>> {
        let reads = tx.stable(namespace).then(|| tx.memo::<Reads>());
        let mut seen = HashSet::new();
        let mut found = Vec::new();
        let mut missing: Vec<&str> = Vec::new();
        {
            let kept = reads.as_ref().map(|reads| lock(&reads.instances));
            for &id in ids {
                if !seen.insert(id) {
                    continue;
                }
                match kept
                    .as_ref()
                    .and_then(|kept| kept.get(&key_of(namespace, schema, id)))
                {
                    Some(Some(row)) => found.push(row.clone()),
                    Some(None) => {}
                    None => missing.push(id),
                }
            }
        }
        if missing.is_empty() {
            return Ok(found);
        }
        let mut params = vec![SqlValue::from(namespace), SqlValue::from(schema)];
        params.extend(missing.iter().map(|id| SqlValue::from(*id)));
        let rows = tx.all(
            format!(
                "SELECT {} FROM engine_instances WHERE namespace = ? AND schema = ? AND id IN ({})",
                with_columns(runtime),
                vec!["?"; missing.len()].join(", ")
            ),
            &params,
        )?;
        if reads.is_some() {
            Reads::keep(tx, namespace, schema, &rows, runtime)?;
            let reads = tx.memo::<Reads>();
            let mut kept = lock(&reads.instances);
            for id in missing {
                kept.entry(key_of(namespace, schema, id)).or_insert(None);
            }
        }
        found.extend(rows);
        Ok(found)
    }

    /// What a statement that reads instances of a schema selects: their
    /// own columns, then every behavior column the version composes.
    pub fn select(runtime: &VersionRuntime) -> String {
        with_columns(runtime)
    }

    /// Keeps rows of instances of a schema in a namespace that a statement
    /// read with [`Reads::select`]'s columns: a list's page, whose
    /// behaviors' readers then read each instance from memory.
    pub fn keep(
        tx: &Tx<'_>,
        namespace: &str,
        schema: &str,
        rows: &[Row],
        runtime: &VersionRuntime,
    ) -> Result<()> {
        if !tx.stable(namespace) {
            return Ok(());
        }
        let reads = tx.memo::<Reads>();
        let names: Arc<[String]> = Arc::from(runtime.columns.as_slice());
        let mut instances = lock(&reads.instances);
        let mut columns = lock(&reads.columns);
        for row in rows {
            let key = key_of(namespace, schema, row.text("id")?);
            let values: Option<Vec<SqlValue>> =
                names.iter().map(|name| row.get(name).cloned()).collect();
            if let Some(values) = values {
                columns.insert(key.clone(), Row::new(Arc::clone(&names), values));
            }
            instances.insert(key, Some(row.clone()));
        }
        Ok(())
    }

    /// Drops what the transaction kept of an instance, which a statement
    /// is about to write.
    pub fn forget_instance(tx: &Tx<'_>, namespace: &str, schema: &str, id: &str) {
        let reads = tx.memo::<Reads>();
        let key = key_of(namespace, schema, id);
        lock(&reads.instances).remove(&key);
        lock(&reads.columns).remove(&key);
    }
}

fn key_of(namespace: &str, schema: &str, id: &str) -> InstanceKey {
    (namespace.to_owned(), schema.to_owned(), id.to_owned())
}

fn key_params(namespace: &str, schema: &str, id: &str) -> [SqlValue; 3] {
    [
        SqlValue::from(namespace),
        SqlValue::from(schema),
        SqlValue::from(id),
    ]
}

fn with_columns(runtime: &VersionRuntime) -> String {
    let mut select = COLUMNS.to_owned();
    for column in &runtime.columns {
        select.push_str(", ");
        select.push_str(&quote_identifier(column));
    }
    select
}

fn lock<T>(mutex: &Mutex<T>) -> std::sync::MutexGuard<'_, T> {
    mutex.lock().unwrap_or_else(PoisonError::into_inner)
}
