//! Parties (D157, D193): the principal a `@party` row is, and whether a
//! party's channel identity still stands. The TypeScript runtime's
//! `parties.ts`.
//!
//! A party is a row of a `@party` table, a principal of kind party (D156)
//! that acts only through the grants it holds: the rows of the
//! `@accessGrants` table whose holder is its key. [`party_principal`]
//! resolves it the way the grant authenticator resolves a grant's token,
//! with every active grant's reach (or one grant's), and the permissions of
//! those grants' roles.
//!
//! An identity whose party's grants have all ended since it was verified
//! has lapsed ([`identity_lapsed`]): a phone number is reassigned, an
//! address changes hands, and a message from it is no longer the party's
//! until it is verified again. It is decided from the rows, with no stamp
//! to keep: the party holds no active grant, and some grant of it did not
//! end, by its date or its revocation, before the identity was verified. A
//! grant that ended by its target's state counts as ended after, since the
//! row does not say when.

use std::time::SystemTime;

use deadpool_postgres::GenericClient;
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

use super::postgres::Repositories;
use super::{Definition, Error, FindOptions, Values, ACCESS_GRANTS, ACCESS_ROLES};
use crate::access::{active_grants, grant_ended, reaches_of, ActiveGrantsFilter};
use crate::auth::{GrantReach, Principal, PrincipalKind};
use crate::civil::parse_rfc3339;
use crate::uuid62::parse_uuid;

/// A party: its table, by its type's name, and its row's key.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct PartyRef {
    pub table: String,
    pub key: String,
}

impl PartyRef {
    /// The party whose row of `table` has the key `key`.
    pub fn new(table: impl Into<String>, key: impl Into<String>) -> Self {
        Self {
            table: table.into(),
            key: key.into(),
        }
    }
}

/// The `@party` tables of `definition`, by their types' names.
pub fn party_tables_of(definition: &Definition) -> Vec<&str> {
    definition
        .tables
        .iter()
        .filter(|table| table.party.is_some())
        .map(|table| table.name.as_str())
        .collect()
}

/// Whether `a` and `b` are the same id, whatever form each UUID came in:
/// hyphenated or base62.
pub fn same_uuid(a: &str, b: &str) -> bool {
    match parse_uuid(a) {
        Some(x) => parse_uuid(b) == Some(x),
        None => a == b,
    }
}

fn text(row: &Values, member: &str) -> String {
    match row.get(member) {
        Some(Value::String(text)) => text.clone(),
        Some(Value::Null) | None => String::new(),
        Some(other) => other.to_string(),
    }
}

fn strings(row: Option<&Values>, member: &str) -> Vec<String> {
    row.and_then(|row| row.get(member))
        .and_then(Value::as_array)
        .map(|items| {
            items
                .iter()
                .filter_map(Value::as_str)
                .map(str::to_owned)
                .collect()
        })
        .unwrap_or_default()
}

/// The role named `name`, a row of the UserRole table, read as the system;
/// none for a database without one, or no such role.
pub(crate) async fn role_named<C: GenericClient + Sync>(
    client: &C,
    definition: &Definition,
    name: &str,
) -> Result<Option<Values>, Error> {
    let Some(roles) = definition.access_table(ACCESS_ROLES) else {
        return Ok(None);
    };
    let system = Repositories::new(client, definition)?.as_system();
    let mut filter = Values::new();
    filter.insert("name".to_owned(), json!({ "eq": name }));
    system
        .table(&roles.name)?
        .find_one(Some(&filter), &FindOptions::default())
        .await
}

/// The principal `party` is (D157): kind party, its subject the row's key,
/// the reach of each of its active grants at `now` (or of the one grant
/// `grant` names, when it is active), and the permissions of those grants'
/// roles, each once in the order the grants give them. A party with no
/// active grant is a principal that reaches nothing.
pub async fn party_principal<C: GenericClient + Sync>(
    client: &C,
    definition: &Definition,
    party: &PartyRef,
    grant: Option<&str>,
    now: SystemTime,
) -> Result<Principal, Error> {
    let active = active_grants(
        client,
        definition,
        &ActiveGrantsFilter {
            holder: Some(party.key.clone()),
            now: Some(now),
            ..ActiveGrantsFilter::default()
        },
    )
    .await?;
    let mut permissions: Vec<String> = Vec::new();
    let mut grants = Vec::new();
    for row in active {
        let id = text(&row, "id");
        if grant.is_some_and(|grant| !same_uuid(&id, grant)) {
            continue;
        }
        let role = role_named(client, definition, &text(&row, "role")).await?;
        for permission in strings(role.as_ref(), "permissions") {
            if !permissions.contains(&permission) {
                permissions.push(permission);
            }
        }
        let reaches = strings(role.as_ref(), "reaches");
        grants.push(GrantReach {
            id,
            target_type: text(&row, "targetType"),
            target_id: text(&row, "targetId"),
            reaches: reaches_of(reaches.iter().map(String::as_str)),
        });
    }
    Ok(Principal::new(party.key.clone(), permissions)
        .of_kind(PrincipalKind::Party)
        .with_grants(grants))
}

fn time(row: &Values, member: &str) -> Option<SystemTime> {
    row.get(member)
        .and_then(Value::as_str)
        .and_then(parse_rfc3339)
}

/// Whether `identity`, a row of the channel identities, has lapsed at `now`
/// (D157): it is a party's, the party holds no active grant, and some grant
/// of the party did not end before the identity was verified (its
/// `verifiedAt`, else its `createdAt`). A user's identity never lapses, and
/// neither does a party's that has held no grant since it was verified.
pub async fn identity_lapsed<C: GenericClient + Sync>(
    client: &C,
    definition: &Definition,
    identity: &Values,
    now: SystemTime,
) -> Result<bool, Error> {
    if identity.get("principalKind").and_then(Value::as_str) != Some("party") {
        return Ok(false);
    }
    let Some(grants) = definition.access_table(ACCESS_GRANTS) else {
        return Ok(false);
    };
    let system = Repositories::new(client, definition)?.as_system();
    let mut filter = Values::new();
    filter.insert(
        "holder".to_owned(),
        json!({ "eq": identity.get("principal").cloned().unwrap_or(Value::Null) }),
    );
    let rows = system
        .table(&grants.name)?
        .find_many(
            Some(&filter),
            &FindOptions {
                limit: Some(1000),
                skip_count: true,
                ..FindOptions::default()
            },
        )
        .await?
        .rows;
    if rows.is_empty() {
        return Ok(false);
    }
    let verified = time(identity, "verifiedAt")
        .or_else(|| time(identity, "createdAt"))
        .unwrap_or(SystemTime::UNIX_EPOCH);
    let mut ended_since = false;
    for grant in &rows {
        if grant_ended(client, definition, grant, now).await?.is_none() {
            // An active grant: the identity stands.
            return Ok(false);
        }
        // A grant not started yet is no active one, and it lapses nothing.
        if time(grant, "startsAt").is_some_and(|starts| starts > now) {
            continue;
        }
        let end = [time(grant, "endsAt"), time(grant, "revokedAt")]
            .into_iter()
            .flatten()
            .filter(|at| *at <= now)
            .min();
        // A grant that ended by its target's state has no end the row
        // records: it counts as ended after the identity was verified.
        if end.is_none_or(|end| end > verified) {
            ended_since = true;
        }
    }
    Ok(ended_since)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn same_uuid_reads_either_form() {
        let hyphenated = "6a1f4d6e-58a2-4c47-9d11-2f8b1c3d4e5f";
        let base62 = crate::uuid62::uuid_to_base62(parse_uuid(hyphenated).unwrap());
        assert!(same_uuid(hyphenated, &base62));
        assert!(same_uuid(&base62, hyphenated));
        assert!(!same_uuid(
            hyphenated,
            "6a1f4d6e-58a2-4c47-9d11-2f8b1c3d4e50"
        ));
        assert!(same_uuid("not-a-uuid", "not-a-uuid"));
        assert!(!same_uuid("not-a-uuid", "other"));
    }
}
