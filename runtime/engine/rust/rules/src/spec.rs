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
//!
//! A type also lists its own fields, each by name with its IR type and
//! whether its values are instants ([`FieldSpec`]): what a config that
//! names a field is held to (a Timers deadline that starts from a field's
//! time), since a config that names a field is typed by the type it sits
//! on (D103). A host that gives none gives a type with no own fields. An
//! own field that holds the key of a row of another type, a to-one
//! relation's column (`projectId` of `project: Relation<Project>`), names
//! the relation and its target ([`RelationSpec`]): what the core follows
//! from a write of the row to the listeners of the type it points at
//! (Reactions' allTerminal over `task.project`).

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

/// One own field of a type: its name, its IR type's name, whether its
/// values are instants, and the to-one relation whose key it holds.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct FieldSpec {
    pub name: String,
    /// Its IR type's name (`string`, `Temporal.DateTime`).
    #[serde(rename = "type")]
    pub type_name: String,
    /// Whether its values are instants: epoch milliseconds from a table's
    /// adapter, or the ISO 8601 text an engine schema's date-time holds.
    #[serde(default, skip_serializing_if = "is_false")]
    pub date_time: bool,
    /// The to-one relation whose target's key the field holds; `None` for
    /// a field that holds no key.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub relation: Option<RelationSpec>,
}

/// A to-one relation an own field holds the key of, as the core's spec
/// names it: the relation field's name and its target type.
#[derive(Clone, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RelationSpec {
    /// The relation field's name (`project`).
    pub name: String,
    /// The type it points at (`Project`).
    #[serde(rename = "type")]
    pub type_name: String,
}

fn is_false(flag: &bool) -> bool {
    !*flag
}

impl FieldSpec {
    /// A field that holds no instant.
    pub fn new(name: impl Into<String>, type_name: impl Into<String>) -> Self {
        FieldSpec {
            name: name.into(),
            type_name: type_name.into(),
            date_time: false,
            relation: None,
        }
    }

    /// A field that holds the key of a to-one relation's target: its name
    /// and IR type (`projectId`, `Identity.UUID`), the relation's name and
    /// the type it points at (`project`, `Project`).
    pub fn relation(
        name: impl Into<String>,
        type_name: impl Into<String>,
        relation_name: impl Into<String>,
        target: impl Into<String>,
    ) -> Self {
        FieldSpec {
            relation: Some(RelationSpec {
                name: relation_name.into(),
                type_name: target.into(),
            }),
            ..FieldSpec::new(name, type_name)
        }
    }

    /// A field whose values are instants.
    pub fn date_time(name: impl Into<String>, type_name: impl Into<String>) -> Self {
        FieldSpec {
            date_time: true,
            ..FieldSpec::new(name, type_name)
        }
    }
}

/// A type the core runs: its name, its uses in list order and its own
/// fields. Guards, validation, initializers and after-change hooks run in
/// the uses' order.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TypeSpec {
    pub name: String,
    #[serde(default)]
    pub uses: Vec<UseSpec>,
    /// The type's own fields, by name: what a config that names a field is
    /// held to (a Timers field start). Empty when the host gives none.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub fields: Vec<FieldSpec>,
}

impl TypeSpec {
    /// A type with no uses and no own fields yet.
    pub fn new(name: impl Into<String>) -> Self {
        TypeSpec {
            name: name.into(),
            uses: Vec::new(),
            fields: Vec::new(),
        }
    }

    /// The type with a use added after the ones before it.
    pub fn with(mut self, entry: UseSpec) -> Self {
        self.uses.push(entry);
        self
    }

    /// The type with an own field added after the ones before it.
    pub fn with_field(mut self, field: FieldSpec) -> Self {
        self.fields.push(field);
        self
    }

    /// The own field with a name.
    pub fn field(&self, name: &str) -> Option<&FieldSpec> {
        self.fields.iter().find(|field| field.name == name)
    }

    /// The own field that holds the key of the to-one relation with a name
    /// (`projectId` for `project`).
    pub fn relation(&self, name: &str) -> Option<&FieldSpec> {
        self.fields.iter().find(|field| {
            field
                .relation
                .as_ref()
                .is_some_and(|relation| relation.name == name)
        })
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
        assert!(spec.fields.is_empty());
    }

    #[test]
    fn a_specs_own_fields_read_and_write_as_json() {
        let spec = TypeSpec::new("Ticket")
            .with_field(FieldSpec::new("title", "string"))
            .with_field(FieldSpec::date_time("visitAt", "Temporal.DateTime"));
        let written = serde_json::to_value(&spec).unwrap();
        assert_eq!(
            written,
            json!({
                "name": "Ticket",
                "uses": [],
                "fields": [
                    {"name": "title", "type": "string"},
                    {"name": "visitAt", "type": "Temporal.DateTime", "dateTime": true}
                ]
            })
        );
        assert_eq!(serde_json::from_value::<TypeSpec>(written).unwrap(), spec);
        assert_eq!(
            spec.field("visitAt").map(|field| field.date_time),
            Some(true)
        );
        assert!(spec.field("when").is_none());
        // A spec with no own fields writes none.
        assert_eq!(
            serde_json::to_value(TypeSpec::new("Note")).unwrap(),
            json!({"name": "Note", "uses": []})
        );
    }

    #[test]
    fn a_relations_field_names_the_relation_and_its_target() {
        let spec = TypeSpec::new("Task")
            .with_field(FieldSpec::new("title", "string"))
            .with_field(FieldSpec::relation(
                "projectId",
                "Identity.UUID",
                "project",
                "Project",
            ));
        let written = serde_json::to_value(&spec).unwrap();
        assert_eq!(
            written["fields"][1],
            json!({"name": "projectId", "type": "Identity.UUID", "relation": {"name": "project", "type": "Project"}})
        );
        assert_eq!(serde_json::from_value::<TypeSpec>(written).unwrap(), spec);
        assert_eq!(
            spec.relation("project").map(|field| field.name.as_str()),
            Some("projectId")
        );
        assert!(spec.relation("title").is_none());
        assert!(spec.relation("projectId").is_none());
    }
}
