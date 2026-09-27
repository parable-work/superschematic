//! `merge`: a three-way merge of two trees against their common base, per
//! entity and then per conflict unit.

use std::collections::{BTreeMap, BTreeSet, HashMap};

use serde::{Deserialize, Deserializer, Serialize};
use serde_json::{Map, Value};

use crate::descriptor::{Graph, Kind, Unit};
use crate::diff::by_key;
use crate::error::Error;
use crate::tree::{self, Row, Tree};

/// Which input a resolution takes a unit's value from.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Take {
    Base,
    Ours,
    Theirs,
}

/// A caller's settlement of one conflict, matched by kind, entity key and
/// unit path. It either takes one input's value (`take`) or gives the value
/// (`value`); a unit absent on the taken side is removed.
#[derive(Debug, Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Resolution {
    pub kind: String,
    #[serde(rename = "entityKey")]
    pub entity_key: String,
    pub path: String,
    #[serde(default)]
    pub take: Option<Take>,
    #[serde(default, deserialize_with = "present")]
    pub value: Option<Value>,
}

/// `"value": null` is a value (JSON null); only a missing member is `None`.
fn present<'de, D: Deserializer<'de>>(deserializer: D) -> Result<Option<Value>, D::Error> {
    Value::deserialize(deserializer).map(Some)
}

/// One unresolved conflict. `path` is a JSON Pointer into the row: `""` is
/// the whole entity (an edit against a delete), `/<column>` an atomic column,
/// `/<column>/<key>` a key of a keyed column, and a JSON Schema column's units
/// are `/<column>/properties/<name>/...`, `/<column>/required/<name>` (a
/// boolean membership) and `/<column>/<keyword>`. `base`, `ours` and `theirs`
/// are the unit's values, each left out when the unit is absent on that side.
#[derive(Debug, Clone, Serialize)]
pub struct Conflict {
    pub kind: String,
    #[serde(rename = "entityKey")]
    pub entity_key: String,
    pub path: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub base: Option<Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub ours: Option<Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub theirs: Option<Value>,
    #[serde(rename = "oursAuthor", skip_serializing_if = "Option::is_none")]
    pub ours_author: Option<Value>,
    #[serde(rename = "theirsAuthor", skip_serializing_if = "Option::is_none")]
    pub theirs_author: Option<Value>,
}

/// Which input an entity's result came from: `ours` when it equals ours
/// (nothing to write onto ours), `theirs` when it equals theirs, `merged` when
/// it equals neither, and `conflict` when a conflict is left.
#[derive(Debug, Clone, Serialize)]
pub struct Outcome {
    pub kind: String,
    #[serde(rename = "entityKey")]
    pub entity_key: String,
    pub side: &'static str,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub deleted: bool,
}

/// The resolutions of one merge, each marked when a conflict uses it.
struct Resolutions {
    by_unit: HashMap<(String, String, String), (Resolution, bool)>,
}

impl Resolutions {
    fn new(graph: &Graph, list: Vec<Resolution>) -> Result<Self, Error> {
        let mut by_unit = HashMap::new();
        for (i, resolution) in list.into_iter().enumerate() {
            let at = format!("resolutions[{i}]");
            if graph.kind_index(&resolution.kind).is_none() {
                return Err(Error::resolution(format!(
                    "{at}: kind {:?} is not in the descriptor",
                    resolution.kind
                )));
            }
            match (&resolution.take, &resolution.value) {
                (Some(_), None) => {}
                (None, Some(_)) if !resolution.path.is_empty() => {}
                (None, Some(_)) => {
                    return Err(Error::resolution(format!(
                        "{at}: a whole-entity conflict (path \"\") is settled with take, not value"
                    )))
                }
                _ => {
                    return Err(Error::resolution(format!(
                        "{at}: give exactly one of take and value"
                    )))
                }
            }
            let unit = (
                resolution.kind.clone(),
                resolution.entity_key.clone(),
                resolution.path.clone(),
            );
            if by_unit.insert(unit, (resolution, false)).is_some() {
                return Err(Error::resolution(format!(
                    "{at}: a second resolution for the same kind, entity key and path"
                )));
            }
        }
        Ok(Self { by_unit })
    }

    fn take(&mut self, kind: &str, key: &str, path: &str) -> Option<&Resolution> {
        let unit = (kind.to_owned(), key.to_owned(), path.to_owned());
        let (resolution, used) = self.by_unit.get_mut(&unit)?;
        *used = true;
        Some(resolution)
    }

    fn unused(&self) -> Option<&Resolution> {
        let mut unused: Vec<&Resolution> = self
            .by_unit
            .values()
            .filter(|(_, used)| !used)
            .map(|(resolution, _)| resolution)
            .collect();
        unused.sort_by(|a, b| {
            (&a.kind, &a.entity_key, &a.path).cmp(&(&b.kind, &b.entity_key, &b.path))
        });
        unused.first().copied()
    }
}

/// One side's state of one entity.
#[derive(Clone, Copy)]
enum State<'a> {
    Live(&'a Row),
    /// Deleted, by a tombstone row or by absence.
    Deleted(Option<&'a Row>),
}

impl<'a> State<'a> {
    fn of(rows: &BTreeMap<&str, &'a Row>, key: &str) -> Self {
        match rows.get(key) {
            Some(row) if !row.deleted => State::Live(row),
            Some(row) => State::Deleted(Some(row)),
            None => State::Deleted(None),
        }
    }

    fn same(&self, other: &Self, kind: &Kind) -> bool {
        match (self, other) {
            (State::Deleted(_), State::Deleted(_)) => true,
            (State::Live(a), State::Live(b)) => a.content(kind) == b.content(kind),
            _ => false,
        }
    }

    fn live(&self) -> Option<&'a Row> {
        match self {
            State::Live(row) => Some(row),
            State::Deleted(_) => None,
        }
    }

    /// The row this state contributes to a tree: the live row, or the
    /// tombstone when it has one.
    fn row(&self) -> Option<&'a Row> {
        match self {
            State::Live(row) => Some(row),
            State::Deleted(row) => *row,
        }
    }

    fn author(&self, kind: &Kind) -> Option<Value> {
        self.row().and_then(|row| row.author(kind))
    }
}

/// The merged result of one entity.
enum Merged<'a> {
    State(State<'a>),
    Row(Row),
}

/// Merge `ours` and `theirs` against `base`. See the crate README for the
/// rules; in short, per entity: one-sided changes take that side, equal
/// changes agree, an edit against a delete conflicts, and two different edits
/// merge per conflict unit, where a unit changed differently on both sides
/// conflicts. `resolutions` settle conflicts by unit path.
pub fn merge(
    graph: &Graph,
    base: &Tree,
    ours: &Tree,
    theirs: &Tree,
    resolutions: Vec<Resolution>,
) -> Result<Value, Error> {
    let mut resolutions = Resolutions::new(graph, resolutions)?;
    let mut conflicts: Vec<Conflict> = Vec::new();
    let mut outcomes: Vec<Outcome> = Vec::new();
    let mut owned: Vec<Vec<Row>> = vec![Vec::new(); graph.kinds.len()];
    let mut kept: Vec<Vec<&Row>> = vec![Vec::new(); graph.kinds.len()];

    for (index, kind) in graph.kinds.iter().enumerate() {
        let b_rows = by_key(&base.kinds[index]);
        let o_rows = by_key(&ours.kinds[index]);
        let t_rows = by_key(&theirs.kinds[index]);
        let keys: BTreeSet<&str> = b_rows
            .keys()
            .chain(o_rows.keys())
            .chain(t_rows.keys())
            .copied()
            .collect();
        for key in keys {
            let (b, o, t) = (
                State::of(&b_rows, key),
                State::of(&o_rows, key),
                State::of(&t_rows, key),
            );
            let mut entity = EntityMerge {
                kind,
                key,
                resolutions: &mut resolutions,
                conflicts: Vec::new(),
            };
            let merged = entity.merge(b, o, t)?;
            let mut found = entity.conflicts;
            if !found.is_empty() {
                found.sort_by(|a, b| a.path.cmp(&b.path));
                for conflict in &mut found {
                    conflict.ours_author = o.author(kind);
                    conflict.theirs_author = t.author(kind);
                }
                conflicts.extend(found);
                outcomes.push(Outcome {
                    kind: kind.name.clone(),
                    entity_key: key.to_owned(),
                    side: "conflict",
                    deleted: false,
                });
                continue;
            }
            let (side, deleted) = match &merged {
                Merged::State(state) => {
                    let side = if state.same(&o, kind) {
                        "ours"
                    } else if state.same(&t, kind) {
                        "theirs"
                    } else {
                        "merged"
                    };
                    (side, state.live().is_none())
                }
                Merged::Row(_) => ("merged", false),
            };
            outcomes.push(Outcome {
                kind: kind.name.clone(),
                entity_key: key.to_owned(),
                side,
                deleted,
            });
            match merged {
                Merged::State(state) => kept[index].extend(state.row()),
                Merged::Row(row) => owned[index].push(row),
            }
        }
    }

    if let Some(resolution) = resolutions.unused() {
        return Err(Error::new(
            "unmatched_resolution",
            format!(
                "no conflict of {} {:?} at path {:?}; resolve the conflicts the last merge returned",
                resolution.kind, resolution.entity_key, resolution.path
            ),
        ));
    }

    for (index, rows) in owned.iter().enumerate() {
        kept[index].extend(rows.iter());
    }
    Ok(serde_json::json!({
        "merged": tree::render(graph, kept),
        "conflicts": conflicts,
        "entities": outcomes,
    }))
}

/// The merge of one entity: its kind, its key, the shared resolutions, and
/// the conflicts found so far.
struct EntityMerge<'m> {
    kind: &'m Kind,
    key: &'m str,
    resolutions: &'m mut Resolutions,
    conflicts: Vec<Conflict>,
}

impl EntityMerge<'_> {
    fn merge<'a>(&mut self, b: State<'a>, o: State<'a>, t: State<'a>) -> Result<Merged<'a>, Error> {
        let kind = self.kind;
        if b.same(&o, kind) {
            return Ok(Merged::State(t));
        }
        if b.same(&t, kind) || o.same(&t, kind) {
            return Ok(Merged::State(o));
        }
        let (Some(ours), Some(theirs)) = (o.live(), t.live()) else {
            return self.edit_against_delete(b, o, t);
        };
        let base = b.live();
        let (bc, oc, tc) = (
            base.map(|row| row.content(kind)).unwrap_or_default(),
            ours.content(kind),
            theirs.content(kind),
        );
        let columns: BTreeSet<&String> = bc.keys().chain(oc.keys()).chain(tc.keys()).collect();
        let mut content: BTreeMap<String, Value> = BTreeMap::new();
        for column in columns {
            let path = pointer("", column);
            let (vb, vo, vt) = (bc.get(column), oc.get(column), tc.get(column));
            let value = match kind.unit(column) {
                Unit::Atomic => self.unit(path, vb, vo, vt)?,
                Unit::Keyed => self.keyed(path, vb, vo, vt)?,
                Unit::JsonSchema => self.schema(path, vb, vo, vt)?,
            };
            if let Some(value) = value {
                content.insert(column.clone(), value);
            }
        }
        if content == oc {
            return Ok(Merged::State(o));
        }
        if content == tc {
            return Ok(Merged::State(t));
        }
        let mut value = ours.value.clone();
        value.retain(|column, _| !kind.is_content(column));
        value.extend(content);
        // A resolution's value can give the order or parent key column a
        // value no input row had; the merged row is checked like an input.
        let row = tree::parse_row(kind, &format!("merged.{}", kind.name), Value::Object(value))?;
        if !row.order_in_range {
            return Err(Error::new(
                "order_out_of_range",
                tree::order_message("merged", kind, &row),
            ));
        }
        Ok(Merged::Row(row))
    }

    /// One side edited the entity and the other deleted it. The conflict is
    /// the whole entity (path `""`), settled only by taking a side.
    fn edit_against_delete<'a>(
        &mut self,
        b: State<'a>,
        o: State<'a>,
        t: State<'a>,
    ) -> Result<Merged<'a>, Error> {
        let (name, key) = (&self.kind.name, self.key);
        if let Some(resolution) = self.resolutions.take(name, key, "") {
            return Ok(Merged::State(match resolution.take {
                Some(Take::Base) => b,
                Some(Take::Theirs) => t,
                _ => o,
            }));
        }
        let full = |state: State| state.live().map(|row| Value::Object(row.value.clone()));
        self.conflicts
            .push(self.conflict(String::new(), full(b), full(o), full(t)));
        Ok(Merged::State(o))
    }

    fn conflict(
        &self,
        path: String,
        base: Option<Value>,
        ours: Option<Value>,
        theirs: Option<Value>,
    ) -> Conflict {
        Conflict {
            kind: self.kind.name.clone(),
            entity_key: self.key.to_owned(),
            path,
            base,
            ours,
            theirs,
            ours_author: None,
            theirs_author: None,
        }
    }

    /// An atomic unit: equal changes agree, a one-sided change wins, and two
    /// different changes conflict unless a resolution names this path. A
    /// conflicted unit returns its base value; the entity is left out anyway.
    fn unit(
        &mut self,
        path: String,
        b: Option<&Value>,
        o: Option<&Value>,
        t: Option<&Value>,
    ) -> Result<Option<Value>, Error> {
        if let Some(value) = three_way(b, o, t) {
            return Ok(value.cloned());
        }
        if let Some(resolution) = self.resolutions.take(&self.kind.name, self.key, &path) {
            return Ok(match (resolution.take, &resolution.value) {
                (Some(Take::Base), _) => b.cloned(),
                (Some(Take::Ours), _) => o.cloned(),
                (Some(Take::Theirs), _) => t.cloned(),
                (None, value) => value.clone(),
            });
        }
        self.conflicts
            .push(self.conflict(path, b.cloned(), o.cloned(), t.cloned()));
        Ok(b.cloned())
    }

    /// A keyed column: when both sides changed it and both are objects, each
    /// top-level key is an atomic unit. Otherwise the column is one unit.
    fn keyed(
        &mut self,
        path: String,
        b: Option<&Value>,
        o: Option<&Value>,
        t: Option<&Value>,
    ) -> Result<Option<Value>, Error> {
        if let Some(value) = three_way(b, o, t) {
            return Ok(value.cloned());
        }
        let empty = Map::new();
        let (Some(bm), Some(om), Some(tm)) = (base_object(b, &empty), object(o), object(t)) else {
            return self.unit(path, b, o, t);
        };
        let keys: BTreeSet<&String> = bm.keys().chain(om.keys()).chain(tm.keys()).collect();
        let mut out = Map::new();
        for key in keys {
            if let Some(value) =
                self.unit(pointer(&path, key), bm.get(key), om.get(key), tm.get(key))?
            {
                out.insert(key.clone(), value);
            }
        }
        Ok(Some(Value::Object(out)))
    }

    /// A JSON Schema node. When both sides changed it and both are objects:
    /// each entry of `properties` merges as a node of its own, each name's
    /// membership in `required` is a boolean unit, and every other keyword is
    /// atomic. A node one side removed, or one whose `type` or `$ref` one side
    /// changed, is one unit, so either conflicts with an edit under it.
    fn schema(
        &mut self,
        path: String,
        b: Option<&Value>,
        o: Option<&Value>,
        t: Option<&Value>,
    ) -> Result<Option<Value>, Error> {
        if let Some(value) = three_way(b, o, t) {
            return Ok(value.cloned());
        }
        let empty = Map::new();
        let (Some(bm), Some(om), Some(tm)) = (base_object(b, &empty), object(o), object(t)) else {
            return self.unit(path, b, o, t);
        };
        if object(b).is_some() && (retyped(bm, om) || retyped(bm, tm)) {
            return self.unit(path, b, o, t);
        }
        let keywords: BTreeSet<&String> = bm.keys().chain(om.keys()).chain(tm.keys()).collect();
        let mut out = Map::new();
        for keyword in keywords {
            let (kb, ko, kt) = (bm.get(keyword), om.get(keyword), tm.get(keyword));
            let at = pointer(&path, keyword);
            let value = if let Some(value) = three_way(kb, ko, kt) {
                value.cloned()
            } else if keyword == "properties" {
                self.properties(at, kb, ko, kt)?
            } else if keyword == "required" {
                self.required(at, kb, ko, kt)?
            } else {
                self.unit(at, kb, ko, kt)?
            };
            if let Some(value) = value {
                out.insert(keyword.clone(), value);
            }
        }
        Ok(Some(Value::Object(out)))
    }

    fn properties(
        &mut self,
        path: String,
        b: Option<&Value>,
        o: Option<&Value>,
        t: Option<&Value>,
    ) -> Result<Option<Value>, Error> {
        let empty = Map::new();
        let (Some(bm), Some(om), Some(tm)) = (
            base_object(b, &empty),
            base_object(o, &empty),
            base_object(t, &empty),
        ) else {
            return self.unit(path, b, o, t);
        };
        let names: BTreeSet<&String> = bm.keys().chain(om.keys()).chain(tm.keys()).collect();
        let mut out = Map::new();
        for name in names {
            if let Some(value) = self.schema(
                pointer(&path, name),
                bm.get(name),
                om.get(name),
                tm.get(name),
            )? {
                out.insert(name.clone(), value);
            }
        }
        let keep = !out.is_empty() || (o.is_some() && t.is_some());
        Ok(keep.then_some(Value::Object(out)))
    }

    /// `required` as one boolean unit per name. A boolean three-way merge
    /// always settles, so membership never conflicts.
    fn required(
        &mut self,
        path: String,
        b: Option<&Value>,
        o: Option<&Value>,
        t: Option<&Value>,
    ) -> Result<Option<Value>, Error> {
        let (Some(rb), Some(ro), Some(rt)) = (names(b), names(o), names(t)) else {
            return self.unit(path, b, o, t);
        };
        let (mut list, mut seen) = (Vec::new(), BTreeSet::new());
        // Ours' order, then names only theirs added, in theirs' order.
        for name in ro.iter().chain(&rt).chain(&rb).copied() {
            let (mb, mo, mt) = (rb.contains(&name), ro.contains(&name), rt.contains(&name));
            let member = if mb == mo { mt } else { mo };
            if member && seen.insert(name) {
                list.push(Value::String(name.to_owned()));
            }
        }
        let keep = !list.is_empty() || (o.is_some() && t.is_some());
        Ok(keep.then_some(Value::Array(list)))
    }
}

/// The value of a three-way merge when it has one without a conflict.
fn three_way<'v>(
    b: Option<&'v Value>,
    o: Option<&'v Value>,
    t: Option<&'v Value>,
) -> Option<Option<&'v Value>> {
    if o == t || b == t {
        Some(o)
    } else if b == o {
        Some(t)
    } else {
        None
    }
}

fn object(value: Option<&Value>) -> Option<&Map<String, Value>> {
    match value {
        Some(Value::Object(map)) => Some(map),
        _ => None,
    }
}

/// A base value as an object: an absent or null base is an empty object.
fn base_object<'v>(
    value: Option<&'v Value>,
    empty: &'v Map<String, Value>,
) -> Option<&'v Map<String, Value>> {
    match value {
        None | Some(Value::Null) => Some(empty),
        Some(Value::Object(map)) => Some(map),
        _ => None,
    }
}

/// A `required` list as names; absent is empty, anything but a list of
/// strings is `None`.
fn names(value: Option<&Value>) -> Option<Vec<&str>> {
    match value {
        None => Some(Vec::new()),
        Some(Value::Array(items)) => items.iter().map(Value::as_str).collect(),
        _ => None,
    }
}

fn retyped(base: &Map<String, Value>, side: &Map<String, Value>) -> bool {
    base.get("type") != side.get("type") || base.get("$ref") != side.get("$ref")
}

/// Append one JSON Pointer reference token (RFC 6901) to `path`.
fn pointer(path: &str, token: &str) -> String {
    format!("{path}/{}", token.replace('~', "~0").replace('/', "~1"))
}
