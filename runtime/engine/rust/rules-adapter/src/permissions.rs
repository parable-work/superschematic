//! The core's permissions read (D200, `ReadRequest::Permissions`): what an
//! Approvals requirement with `recheckPermission` reads of the subjects
//! who signed earlier, answered from the layout's identity tables as the
//! identity runtime's store reads a principal's roles.

use std::collections::{BTreeMap, HashMap};

use serde_json::Value;

use crate::convert::{base62, hyphenate, internal, uuid_value, Bind, Cell, UUID_TYPES};
use crate::layout::Permissions;
use crate::run::{quote, Dialect, Store};
use crate::Error;

/// The permissions each subject a user row holds has now: those of every
/// role a grant gives the user, each once, and none while the user's
/// credential is disabled or pending. A subject no user row holds, or that
/// is no key of the user table, is left out, and the core holds it to no
/// permission; so is every subject of a layout that names no identity
/// tables, whose database the adapter cannot answer for.
pub(crate) async fn read_permissions<S: Store>(
    store: &S,
    from: Option<&Permissions>,
    subjects: &[String],
    agents: &[String],
) -> Result<BTreeMap<String, Vec<String>>, Error> {
    let mut out = read_agent_permissions(store, from, agents).await?;
    let Some(from) = from else {
        return Ok(out);
    };
    let dialect = store.dialect();
    let uuid = UUID_TYPES.contains(&from.user.type_name.as_str());
    // Each subject by the key its user row would hold, as the adapter
    // matches a row's key: a UUID's base62 wire form, any other key's text.
    let mut by_subject = Vec::new();
    let mut values = Vec::new();
    for subject in subjects {
        let (bound, matched) = if uuid {
            let Some(n) = uuid_value(subject) else {
                continue;
            };
            (hyphenate(n), base62(n))
        } else {
            (subject.clone(), subject.clone())
        };
        by_subject.push((subject.clone(), matched));
        values.push(Bind::Text(bound));
    }
    if values.is_empty() {
        return Ok(out);
    }
    let placeholders: Vec<String> = (1..=values.len())
        .map(|n| placeholder(dialect, n))
        .collect();
    let user = quote(&from.user.column);
    let text = format!(
        "SELECT {}, {} FROM {} AS u LEFT JOIN {} AS c ON c.{} = u.{user} LEFT JOIN {} AS g ON g.{} = u.{user} AND c.{} IS NULL AND c.{} IS NULL LEFT JOIN {} AS r ON r.{} = g.{} WHERE u.{user} IN ({}) ORDER BY u.{user}, r.{}",
        selected(dialect, &format!("u.{user}")),
        selected(dialect, &format!("r.{}", quote(&from.role_permissions))),
        quote(&from.users),
        quote(&from.credentials),
        quote(&from.credential_user),
        quote(&from.grants),
        quote(&from.grant_user),
        quote(&from.disabled_at),
        quote(&from.pending_at),
        quote(&from.roles),
        quote(&from.role_key),
        quote(&from.grant_role),
        placeholders.join(", "),
        quote(&from.role_key),
    );
    let mut held: HashMap<String, Vec<String>> = HashMap::new();
    for row in store.query(&text, &values).await? {
        let mut cells = row.into_iter();
        let key = key_of(cells.next().unwrap_or(Cell::Null), uuid).ok_or_else(|| {
            internal(format!(
                "{}'s key in a permissions read is no key",
                from.users
            ))
        })?;
        let permissions = held.entry(key).or_default();
        for permission in permissions_of(cells.next().unwrap_or(Cell::Null), from)? {
            if !permissions.contains(&permission) {
                permissions.push(permission);
            }
        }
    }
    for (subject, matched) in by_subject {
        if let Some(permissions) = held.get(&matched) {
            out.insert(subject, permissions.clone());
        }
    }
    Ok(out)
}

/// The permissions of each of `agents` (#625): those of the role its
/// definition row names, by the roles' name column. An Agent no definition
/// row holds, or that is no key of the definitions table, is left out, and
/// the core holds it to none; so is every Agent of a layout that names no
/// agents table.
async fn read_agent_permissions<S: Store>(
    store: &S,
    from: Option<&Permissions>,
    agents: &[String],
) -> Result<BTreeMap<String, Vec<String>>, Error> {
    let mut out = BTreeMap::new();
    let Some(from) = from else {
        return Ok(out);
    };
    let (Some(table), Some(key), Some(role), Some(name)) = (
        from.agents.as_ref(),
        from.agent.as_ref(),
        from.agent_role.as_ref(),
        from.role_name.as_ref(),
    ) else {
        return Ok(out);
    };
    let dialect = store.dialect();
    let uuid = UUID_TYPES.contains(&key.type_name.as_str());
    let mut by_agent = Vec::new();
    let mut values = Vec::new();
    for agent in agents {
        let (bound, matched) = if uuid {
            let Some(n) = uuid_value(agent) else {
                continue;
            };
            (hyphenate(n), base62(n))
        } else {
            (agent.clone(), agent.clone())
        };
        by_agent.push((agent.clone(), matched));
        values.push(Bind::Text(bound));
    }
    if values.is_empty() {
        return Ok(out);
    }
    let placeholders: Vec<String> = (1..=values.len())
        .map(|n| placeholder(dialect, n))
        .collect();
    let column = quote(&key.column);
    let text = format!(
        "SELECT {}, {} FROM {} AS a LEFT JOIN {} AS r ON r.{} = a.{} WHERE a.{column} IN ({}) ORDER BY a.{column}",
        selected(dialect, &format!("a.{column}")),
        selected(dialect, &format!("r.{}", quote(&from.role_permissions))),
        quote(table),
        quote(&from.roles),
        quote(name),
        quote(role),
        placeholders.join(", "),
    );
    let mut held: HashMap<String, Vec<String>> = HashMap::new();
    for row in store.query(&text, &values).await? {
        let mut cells = row.into_iter();
        let key = key_of(cells.next().unwrap_or(Cell::Null), uuid)
            .ok_or_else(|| internal(format!("{table}'s key in a permissions read is no key")))?;
        let permissions = held.entry(key).or_default();
        for permission in permissions_of(cells.next().unwrap_or(Cell::Null), from)? {
            if !permissions.contains(&permission) {
                permissions.push(permission);
            }
        }
    }
    for (agent, matched) in by_agent {
        if let Some(permissions) = held.get(&matched) {
            out.insert(agent, permissions.clone());
        }
    }
    Ok(out)
}

/// The nth placeholder in a dialect: `$n` on Postgres, `?n` on SQLite.
fn placeholder(dialect: Dialect, n: usize) -> String {
    match dialect {
        Dialect::Postgres => format!("${n}"),
        Dialect::Sqlite => format!("?{n}"),
    }
}

/// An expression as a read selects it: Postgres's `to_jsonb` of it, as
/// the adapter's row reads select a column; SQLite's as it is stored.
fn selected(dialect: Dialect, expression: &str) -> String {
    match dialect {
        Dialect::Postgres => format!("to_jsonb({expression})"),
        Dialect::Sqlite => expression.to_owned(),
    }
}

/// A user key a cell holds, as subjects are matched by it.
fn key_of(cell: Cell, uuid: bool) -> Option<String> {
    let text = match cell {
        Cell::Text(text) | Cell::Json(Value::String(text)) => text,
        Cell::Int(n) if !uuid => n.to_string(),
        Cell::Json(Value::Number(n)) if !uuid => n.to_string(),
        Cell::Blob(bytes) if uuid => {
            let raw = <[u8; 16]>::try_from(bytes.as_slice()).ok()?;
            return Some(base62(u128::from_be_bytes(raw)));
        }
        _ => return None,
    };
    if uuid {
        uuid_value(&text).map(base62)
    } else {
        Some(text)
    }
}

/// A role's permissions a cell holds: Postgres's `to_jsonb` of a `TEXT[]`,
/// SQLite's JSON text of one; none for a user who holds no role.
fn permissions_of(cell: Cell, from: &Permissions) -> Result<Vec<String>, Error> {
    let list = match cell {
        Cell::Null | Cell::Json(Value::Null) => return Ok(Vec::new()),
        Cell::Json(value) => value,
        Cell::Text(text) => serde_json::from_str(&text).unwrap_or(Value::Null),
        _ => Value::Null,
    };
    let Value::Array(items) = list else {
        return Err(internal(format!(
            "{}.{} is no list of permissions",
            from.roles, from.role_permissions
        ))
        .into());
    };
    Ok(items
        .into_iter()
        .filter_map(|item| match item {
            Value::String(permission) => Some(permission),
            _ => None,
        })
        .collect())
}
