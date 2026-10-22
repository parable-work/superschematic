//! A server's deploy (D58): the schema documents of its engine database's
//! engine document defined and published, as the engine's own principal,
//! before the server reports the engine ready. A document whose live
//! version has its hash is left as it is, so a server that starts again,
//! or a second instance, writes nothing; a changed one becomes its next
//! version if the compatibility rule allows it. A document whose
//! behaviors' configs name another document deploys after it, and one
//! refused as an invalid schema is tried again after the others (D58,
//! amended). The C ABI's `deploy` call
//! (the Go binding's `Deploy`) and a Rust server's entrypoint run this.

use std::borrow::Cow;

use serde_json::Value;

use crate::errors::{EngineErrorCode, Error, Result};
use crate::{json, DefineOptions, Engine, Principal, SchemaInput, SchemaTarget};

/// What a deploy did with one document.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Deployed {
    /// The schema's name, and the namespace that holds it.
    pub name: String,
    pub namespace: String,
    /// Its live version after the deploy.
    pub version: i64,
    /// Whether the deploy defined a draft, and whether publishing it
    /// minted a version: both false when the live version had the
    /// document's hash.
    pub defined: bool,
    pub published: bool,
}

impl Deployed {
    /// The deploy's outcome as the C ABI writes it: `{ name, namespace,
    /// version, defined, published }`.
    pub fn to_json(&self) -> Value {
        let mut out = serde_json::Map::new();
        out.insert("name".into(), Value::from(self.name.as_str()));
        out.insert("namespace".into(), Value::from(self.namespace.as_str()));
        out.insert("version".into(), Value::from(self.version));
        out.insert("defined".into(), Value::Bool(self.defined));
        out.insert("published".into(), Value::Bool(self.published));
        Value::Object(out)
    }
}

impl Engine {
    /// Defines and publishes each of `documents`, a schema document or its
    /// JSON text, in `namespace` (the default namespace when `None`), as
    /// the engine's own principal: a document whose live version has its
    /// hash is left as it is.
    ///
    /// A behavior's config may name another schema whose live version it
    /// reads (Rollups' source, a Holds supply, a Payments `refundOf` link's
    /// target), and an engine document lists its schemas in its types'
    /// name order. So a deploy first orders the documents so that one comes
    /// after the others its behaviors' configs name (a string in a config
    /// equal to another document's name; documents that name each other
    /// keep the order given), and then deploys in passes: a document whose
    /// define or publish is refused as an invalid schema, which is how a
    /// config's refusal reaches it, is tried again in the next pass. A pass
    /// that deploys none of the documents left returns the first refusal of
    /// that pass, in the dependency order, so a document that cannot deploy
    /// fails with its own refusal rather than a dependent's. Any other
    /// error stops the deploy at once. There are at most as many passes as
    /// documents. A draft a refused publish left is published again, not
    /// defined again, so a retry writes no second draft. The result lists
    /// each document in the order given.
    pub fn deploy(&self, documents: &[Value], namespace: Option<&str>) -> Result<Vec<Deployed>> {
        let parsed = documents
            .iter()
            .map(parse_document)
            .collect::<Result<Vec<_>>>()?;
        let mut deployed: Vec<Option<Deployed>> = vec![None; documents.len()];
        let mut left = dependency_order(&parsed);
        while !left.is_empty() {
            let mut refused = None;
            let mut again = Vec::new();
            for &index in &left {
                match self.deploy_one(&documents[index], &parsed[index], namespace) {
                    Ok(one) => deployed[index] = Some(one),
                    Err(error) if retried(&error) => {
                        refused.get_or_insert(error);
                        again.push(index);
                    }
                    Err(error) => return Err(error),
                }
            }
            if again.len() == left.len() {
                if let Some(error) = refused {
                    return Err(error);
                }
            }
            left = again;
        }
        Ok(deployed.into_iter().flatten().collect())
    }

    // deploy_one defines and publishes one document, or leaves it when the
    // live version has its hash. A draft with its hash, which a publish a
    // pass before refused left, is published without a second define.
    fn deploy_one(
        &self,
        given: &Value,
        parsed: &Parsed<'_>,
        namespace: Option<&str>,
    ) -> Result<Deployed> {
        let target = || SchemaTarget { namespace };
        let system = Principal::system();
        let name = parsed.name.clone();
        if let Some(live) = self.schemas().live(&system, &name, target())? {
            if parsed.hash.as_deref() == Some(live.hash.as_str()) {
                return Ok(Deployed {
                    name,
                    namespace: live.namespace,
                    version: live.version.unwrap_or_default(),
                    defined: false,
                    published: false,
                });
            }
        }
        let drafted = match (
            self.schemas().draft(&system, &name, target())?,
            &parsed.hash,
        ) {
            (Some(draft), Some(hash)) => draft.hash == *hash,
            _ => false,
        };
        if !drafted {
            let input = match given {
                Value::String(text) => SchemaInput::Text(Cow::Borrowed(text)),
                other => SchemaInput::Document(Cow::Borrowed(other)),
            };
            self.schemas().define(
                &system,
                input,
                DefineOptions {
                    namespace,
                    source: None,
                },
            )?;
        }
        let published = self.schemas().publish(&system, &name, target())?;
        Ok(Deployed {
            name,
            namespace: published.namespace,
            version: published.version,
            defined: true,
            published: published.published,
        })
    }
}

// A document as deploy reads it: its name, its hash, and the names its
// behaviors' configs hold.
struct Parsed<'a> {
    name: String,
    hash: Option<String>,
    document: Cow<'a, Value>,
}

// parse_document reads a document, or its JSON text, for its name and hash.
fn parse_document(given: &Value) -> Result<Parsed<'_>> {
    let document: Cow<'_, Value> =
        match given {
            Value::String(text) => Cow::Owned(serde_json::from_str(text).map_err(|error| {
                Error::usage(format!("a schema document's text is JSON: {error}"))
            })?),
            other => Cow::Borrowed(other),
        };
    let name = document
        .get("name")
        .and_then(Value::as_str)
        .ok_or_else(|| Error::usage("deploy: a schema document names its schema (name)"))?
        .to_owned();
    let hash = json::canonical_json(&document)
        .map(|canonical| json::sha256_hex(&canonical))
        .ok();
    Ok(Parsed {
        name,
        hash,
        document,
    })
}

// retried is whether a pass tries a refused document again: a refusal as an
// invalid schema, which a behavior's config refusal is, as one naming a
// schema not yet live is.
fn retried(error: &Error) -> bool {
    match error {
        Error::Engine(refusal) => refusal.code() == EngineErrorCode::InvalidSchema,
        Error::Config(_) => true,
        _ => false,
    }
}

// dependency_order lists the documents' indexes so that each comes after
// the other documents its behaviors' configs name, keeping the order given
// otherwise; documents that name each other, directly or not, keep the
// order given among themselves.
fn dependency_order(parsed: &[Parsed<'_>]) -> Vec<usize> {
    let names: Vec<&str> = parsed.iter().map(|one| one.name.as_str()).collect();
    let needs: Vec<Vec<usize>> = parsed
        .iter()
        .enumerate()
        .map(|(index, one)| {
            let mut named = Vec::new();
            for behavior in behavior_configs(&one.document) {
                collect_names(behavior, &names, &mut named);
            }
            named.sort_unstable();
            named.dedup();
            named.retain(|&other| other != index);
            named
        })
        .collect();
    let mut placed = vec![false; parsed.len()];
    let mut order = Vec::with_capacity(parsed.len());
    while order.len() < parsed.len() {
        // The first document, in the order given, whose named documents are
        // all placed; when none is (a cycle), the first not placed.
        let next = (0..parsed.len())
            .find(|&index| !placed[index] && needs[index].iter().all(|&other| placed[other]))
            .or_else(|| (0..parsed.len()).find(|&index| !placed[index]))
            .expect("a document not placed");
        placed[next] = true;
        order.push(next);
    }
    order
}

// behavior_configs is every behavior config of every type of a document.
fn behavior_configs(document: &Value) -> impl Iterator<Item = &Value> {
    document
        .get("types")
        .and_then(Value::as_object)
        .into_iter()
        .flat_map(|types| types.values())
        .filter_map(|ty| ty.get("behaviors").and_then(Value::as_array))
        .flatten()
        .filter_map(|behavior| behavior.get("config"))
}

// collect_names adds the index of each document whose name a string in
// value is.
fn collect_names(value: &Value, names: &[&str], out: &mut Vec<usize>) {
    match value {
        Value::String(text) => {
            out.extend(
                names
                    .iter()
                    .enumerate()
                    .filter(|(_, name)| **name == text.as_str())
                    .map(|(index, _)| index),
            );
        }
        Value::Array(items) => items
            .iter()
            .for_each(|item| collect_names(item, names, out)),
        Value::Object(members) => members
            .values()
            .for_each(|member| collect_names(member, names, out)),
        _ => {}
    }
}

#[cfg(all(test, feature = "sqlite"))]
mod tests {
    use serde_json::json;

    use crate::{Engine, EngineOptions, Policy};

    fn note(fields: &[&str]) -> serde_json::Value {
        let fields: Vec<_> = fields
            .iter()
            .map(|field| json!({"name": field, "typeRef": {"name": "string"}}))
            .collect();
        json!({
            "kind": "General",
            "name": "Note",
            "types": {"Note": {"name": "Note", "role": "EmbeddedStruct", "fields": fields}}
        })
    }

    #[test]
    fn a_deploy_publishes_once_and_a_changed_document_becomes_the_next_version() {
        let dir = std::env::temp_dir().join(format!(
            "engine-deploy-{}-{:?}",
            std::process::id(),
            std::thread::current().id()
        ));
        std::fs::create_dir_all(&dir).expect("a directory");
        let mut options = EngineOptions::sqlite(dir.join("engine.db"));
        options.policy = Some(Policy::allow_all());
        let engine = Engine::open(options).expect("open");
        let first = engine.deploy(&[note(&["body"])], None).expect("deploy");
        assert_eq!(
            (first[0].version, first[0].defined, first[0].published),
            (1, true, true)
        );
        let again = engine
            .deploy(
                &[serde_json::Value::from(note(&["body"]).to_string())],
                None,
            )
            .expect("deploy again");
        assert_eq!(
            (again[0].version, again[0].defined, again[0].published),
            (1, false, false)
        );
        let changed = engine
            .deploy(&[note(&["body", "title"])], None)
            .expect("deploy a change");
        assert_eq!(changed[0].version, 2);
        assert_eq!(
            changed[0].to_json(),
            json!({"name": "Note", "namespace": "default", "version": 2, "defined": true, "published": true})
        );
        let refused = engine.deploy(&[json!({"kind": "General"})], None);
        assert!(refused.is_err());
        drop(engine);
        let _ = std::fs::remove_dir_all(&dir);
    }
}

#[cfg(test)]
mod order_tests {
    use serde_json::{json, Value};

    use super::{dependency_order, parse_document};

    fn document(name: &str, names: &[&str]) -> Value {
        json!({
            "kind": "General",
            "name": name,
            "types": {name: {"name": name, "behaviors": [{"name": "test.Names", "config": {"schemas": names}}]}}
        })
    }

    fn order(documents: &[Value]) -> Vec<usize> {
        let parsed: Vec<_> = documents
            .iter()
            .map(|one| parse_document(one).expect("a document"))
            .collect();
        dependency_order(&parsed)
    }

    #[test]
    fn a_document_comes_after_the_documents_its_configs_name() {
        // Checkout names Inventory and Counter; Refund names Checkout.
        let documents = [
            document("Checkout", &["Inventory", "Counter"]),
            document("Counter", &[]),
            document("Inventory", &["Inventory"]),
            document("Refund", &["Checkout", "Elsewhere"]),
        ];
        assert_eq!(order(&documents), [1, 2, 0, 3]);
    }

    #[test]
    fn documents_that_name_each_other_keep_the_order_given() {
        let documents = [
            document("A", &["B"]),
            document("B", &["A"]),
            document("C", &[]),
        ];
        assert_eq!(order(&documents), [2, 0, 1]);
        assert_eq!(order(&[]), Vec::<usize>::new());
    }
}
