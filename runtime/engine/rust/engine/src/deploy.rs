//! A server's deploy (D58): the schema documents of its engine database's
//! engine document defined and published, as the engine's own principal,
//! before the server reports the engine ready. A document whose live
//! version has its hash is left as it is, so a server that starts again,
//! or a second instance, writes nothing; a changed one becomes its next
//! version if the compatibility rule allows it. A document refused is
//! tried again after the others, so a config that names another schema
//! deploys after it (D58, amended). The C ABI's `deploy` call
//! (the Go binding's `Deploy`) and a Rust server's entrypoint run this.

use std::borrow::Cow;

use serde_json::Value;

use crate::errors::{Error, Result};
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
    /// hash is left as it is. A behavior's config may name another schema
    /// whose live version it reads (Rollups' source, a Holds supply, a
    /// Payments `refundOf` link's target), which an engine document lists
    /// in its types' name order, so a document the engine refuses is tried
    /// again after the others: each pass deploys what it can, in the order
    /// given, and a pass that deploys none of the documents left returns
    /// the first refusal of that pass, every other document deployed. The
    /// result lists each document in the order given.
    pub fn deploy(&self, documents: &[Value], namespace: Option<&str>) -> Result<Vec<Deployed>> {
        let mut deployed: Vec<Option<Deployed>> = vec![None; documents.len()];
        let mut left: Vec<usize> = (0..documents.len()).collect();
        while !left.is_empty() {
            let mut refused = None;
            let mut again = Vec::new();
            for &index in &left {
                match self.deploy_one(&documents[index], namespace) {
                    Ok(one) => deployed[index] = Some(one),
                    Err(error @ (Error::Engine(_) | Error::Config(_))) => {
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
    // live version has its hash.
    fn deploy_one(&self, given: &Value, namespace: Option<&str>) -> Result<Deployed> {
        let target = || SchemaTarget { namespace };
        let system = Principal::system();
        let document: Cow<'_, Value> = match given {
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
        if let Some(live) = self.schemas().live(&system, &name, target())? {
            if hash.as_deref() == Some(live.hash.as_str()) {
                return Ok(Deployed {
                    name,
                    namespace: live.namespace,
                    version: live.version.unwrap_or_default(),
                    defined: false,
                    published: false,
                });
            }
        }
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
