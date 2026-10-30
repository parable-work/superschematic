//! A type the core runs, described as data (D103): its name and its
//! behavior uses in list order. A use is a behavior's name, the config the
//! schema gives it and where it is anchored: on the whole type, which a
//! schema writes `implements X<Type, Config>`, or on one field, whose type
//! is the behavior (`status: Workflow<{ ... }>`). One type may carry a
//! behavior once on the type and once on each of any number of fields.
//!
//! A use has an id, by which the core addresses its owned fields, its
//! sub-tables, its operations and its create parameters: the behavior's
//! name for a type anchor, the field's name for a field anchor. The ids of
//! a type's uses are distinct; the core refuses a type whose ids repeat.

use serde::{Deserialize, Deserializer, Serialize};
use serde_json::{Map, Value};

/// Where a use is anchored.
#[derive(Clone, Debug, Default, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub enum Anchor {
    /// The whole type: `implements X<Type, Config>`, at most once per
    /// behavior.
    #[default]
    Type,
    /// One field, by its name, whose type is the behavior.
    Field(String),
}

/// One behavior on a type: its name, its config and its anchor.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct UseSpec {
    /// The behavior's declared name (`Workflow`).
    pub behavior: String,
    /// The config as the schema gives it; `None` when it gives none, which
    /// the core checks as `{}` and refuses for a behavior that needs a
    /// config (one that refuses `{}` for its shape). A config given as
    /// JSON null is given.
    #[serde(
        default,
        deserialize_with = "given",
        skip_serializing_if = "Option::is_none"
    )]
    pub config: Option<Value>,
    #[serde(default)]
    pub anchor: Anchor,
}

// given reads a config member that is present, null included, as given;
// an absent one is the field's default, None.
fn given<'de, D: Deserializer<'de>>(deserializer: D) -> Result<Option<Value>, D::Error> {
    Value::deserialize(deserializer).map(Some)
}

impl UseSpec {
    /// A use anchored on the whole type, with its config or `None`.
    pub fn on_type(behavior: impl Into<String>, config: impl Into<Option<Value>>) -> Self {
        UseSpec {
            behavior: behavior.into(),
            config: config.into(),
            anchor: Anchor::Type,
        }
    }

    /// A use anchored on one field, with its config or `None`.
    pub fn on_field(
        field: impl Into<String>,
        behavior: impl Into<String>,
        config: impl Into<Option<Value>>,
    ) -> Self {
        UseSpec {
            behavior: behavior.into(),
            config: config.into(),
            anchor: Anchor::Field(field.into()),
        }
    }

    /// The config as the core checks it: the one given, or `{}`.
    pub fn config_or_empty(&self) -> Value {
        self.config
            .clone()
            .unwrap_or_else(|| Value::Object(Map::new()))
    }

    /// The use's id: the behavior's name for a type anchor, the field's
    /// name for a field anchor.
    pub fn id(&self) -> &str {
        match &self.anchor {
            Anchor::Type => &self.behavior,
            Anchor::Field(field) => field,
        }
    }

    /// The field a field anchor names; `None` for a type anchor.
    pub fn field(&self) -> Option<&str> {
        match &self.anchor {
            Anchor::Type => None,
            Anchor::Field(field) => Some(field),
        }
    }
}

/// A type the core runs: its name and its uses in list order. Guards,
/// validation, initializers and after-change hooks run in this order.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TypeSpec {
    pub name: String,
    #[serde(default)]
    pub uses: Vec<UseSpec>,
}

impl TypeSpec {
    /// A type with no uses yet.
    pub fn new(name: impl Into<String>) -> Self {
        TypeSpec {
            name: name.into(),
            uses: Vec::new(),
        }
    }

    /// The type with a use added after the ones before it.
    pub fn with(mut self, entry: UseSpec) -> Self {
        self.uses.push(entry);
        self
    }

    /// The use with an id, and its position in the list.
    pub fn use_of(&self, id: &str) -> Option<(usize, &UseSpec)> {
        self.uses
            .iter()
            .enumerate()
            .find(|(_, entry)| entry.id() == id)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn a_use_is_named_by_its_behavior_or_its_field() {
        let spec = TypeSpec::new("Order")
            .with(UseSpec::on_type("Workflow", json!({})))
            .with(UseSpec::on_field("fulfilment", "Workflow", json!({})));
        assert_eq!(spec.uses[0].id(), "Workflow");
        assert_eq!(spec.uses[1].id(), "fulfilment");
        assert_eq!(spec.uses[1].field(), Some("fulfilment"));
        assert_eq!(spec.use_of("fulfilment").map(|(at, _)| at), Some(1));
        assert!(spec.use_of("status").is_none());
    }

    #[test]
    fn a_spec_reads_from_json_with_its_defaults() {
        let spec: TypeSpec = serde_json::from_value(json!({
            "name": "Order",
            "uses": [
                {"behavior": "Workflow", "config": {"states": ["a"]}},
                {"behavior": "Workflow", "anchor": {"field": "status"}},
                {"behavior": "Workflow", "config": null, "anchor": {"field": "phase"}}
            ]
        }))
        .unwrap();
        // A config given as null is given; an absent one is not.
        assert_eq!(spec.uses[2].config, Some(Value::Null));
        assert_eq!(
            serde_json::to_value(&spec.uses[1]).unwrap(),
            json!({"behavior": "Workflow", "anchor": {"field": "status"}})
        );
        assert_eq!(spec.uses[0].anchor, Anchor::Type);
        assert_eq!(spec.uses[1].config, None);
        assert_eq!(spec.uses[1].config_or_empty(), json!({}));
        assert_eq!(spec.uses[1].id(), "status");
        assert_eq!(
            serde_json::to_value(&spec.uses[1].anchor).unwrap(),
            json!({"field": "status"})
        );
        assert_eq!(serde_json::to_value(Anchor::Type).unwrap(), json!("type"));
    }
}
