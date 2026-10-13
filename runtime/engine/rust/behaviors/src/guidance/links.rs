//! Links' guidance (`guidance/links.ts`): each link by name, the schema it
//! points at, and whether it is required, pinned to a revision or a
//! release, or holds a list of targets, so a create knows which links it
//! must give and link and unlink which names they take; and the narrowed
//! create parameters: one property per link name, the required ones
//! required, a revision only for a link that pins revisions and a release
//! only for one that pins releases, and a list of at most max targets for a
//! many-valued link, at least one when it is required.

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{
    BehaviorGuidance, DescribeTarget, GuidanceError, OperationGuidance,
};

use super::{list, refusal, said, sentences};
use crate::links::{LinkPin, LinkSpec, LinksConfig};

const ID_PATTERN: &str = "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$";

// described says one link: its name, its schema, and how it holds.
fn described(name: &str, link: &LinkSpec) -> String {
    let mut how = Vec::new();
    if link.required {
        how.push("required".to_owned());
    }
    if let Some(pin) = link.pin {
        how.push(format!("pinned to a {}", pin.as_str()));
    }
    if link.many {
        how.push(format!(
            "a list of at most {}",
            link.max.unwrap_or_default()
        ));
    }
    if how.is_empty() {
        format!("{name} to {}", link.schema)
    } else {
        format!("{name} to {} ({})", link.schema, how.join(", "))
    }
}

// names_where lists the links that satisfy a test, in the config's order.
fn names_where(config: &LinksConfig, test: impl Fn(&LinkSpec) -> bool) -> Vec<&str> {
    config
        .links
        .iter()
        .filter(|(_, link)| test(link))
        .map(|(name, _)| name.as_str())
        .collect()
}

// verb is a verb for one name or several: holds, hold.
fn verb<'v>(names: &[&str], one: &'v str, many: &'v str) -> &'v str {
    if names.len() == 1 {
        one
    } else {
        many
    }
}

// nonempty is a guidance text, or None when it says nothing.
fn nonempty(text: String) -> Option<String> {
    (!text.is_empty()).then_some(text)
}

pub(crate) fn guidance(config: &LinksConfig, target: &DescribeTarget) -> BehaviorGuidance {
    let names = config.names();
    let many = names_where(config, |link| link.many);
    let single = names_where(config, |link| !link.many);
    let required = names_where(config, |link| link.required);
    let required_one = names_where(config, |link| link.required && !link.many);
    let required_many = names_where(config, |link| link.required && link.many);
    let optional_one = names_where(config, |link| !link.many && !link.required);
    let revisions = names_where(config, |link| link.pin == Some(LinkPin::Revision));
    let releases = names_where(config, |link| link.pin == Some(LinkPin::Release));
    let pinned = revisions.len() + releases.len();
    let mut no_pin: Vec<GuidanceError> = Vec::new();
    if !revisions.is_empty() {
        no_pin.push(refusal(
            "no_revision",
            None,
            "Wait until the target has a revision, or link a target that has one.",
        ));
    }
    if !releases.is_empty() {
        no_pin.push(refusal(
            "no_release",
            None,
            "Wait until the target has been released, or link a target that has.",
        ));
    }
    let records = |links: &[&str], what: &str| {
        (!links.is_empty()).then(|| {
            format!(
                "{} record{} the target's latest {what}, or the one given.",
                list(links, "and"),
                if links.len() == 1 { "s" } else { "" }
            )
        })
    };
    let own = |names: &[&str]| -> Vec<String> {
        names
            .iter()
            .filter(|name| {
                config
                    .get(name)
                    .is_some_and(|link| link.schema == target.schema)
            })
            .map(|name| (*name).to_owned())
            .collect()
    };
    let self_one = own(&required_one);
    let self_many = own(&required_many);
    let type_name = &target.type_name;
    let summary = sentences(&[
        Some(format!(
            "Each {type_name} links to other instances by name: {}.",
            list(
                &config
                    .links
                    .iter()
                    .map(|(name, link)| described(name, link))
                    .collect::<Vec<_>>(),
                "and"
            )
        )),
        (!required_one.is_empty()).then(|| {
            format!(
                "Every create gives {}; a required link is moved, never unlinked, and the instance it points at cannot be deleted.",
                list(&required_one, "and")
            )
        }),
        (!many.is_empty()).then(|| {
            format!(
                "{} {} a list of targets in behaviors.Links.targets, in the order they were linked: link adds one and unlink removes the one it names.",
                list(&many, "and"),
                verb(&many, "holds", "hold")
            )
        }),
        (!required_many.is_empty()).then(|| {
            format!(
                "Every create gives at least one target of {}, which {} one: the last target can be neither unlinked nor deleted.",
                list(&required_many, "and"),
                verb(&required_many, "keeps", "keep")
            )
        }),
        (!revisions.is_empty()).then(|| {
            "A link pinned to a revision records the target's revision; behaviors.Links.targets gives the target's latest beside it and says stale once the target has a newer one.".to_owned()
        }),
        (!releases.is_empty()).then(|| {
            "A link pinned to a release records the target's release; behaviors.Links.targets gives the target's latest beside it and says stale once the target has released again.".to_owned()
        }),
    ]);
    let mut link_errors = no_pin.clone();
    if !many.is_empty() {
        link_errors.push(refusal("too_many", None, "Unlink a target first."));
    }
    let mut unlink_errors = Vec::new();
    if !required.is_empty() {
        unlink_errors.push(refusal(
            "required_link",
            None,
            &sentences(&[
                (!required_one.is_empty()).then(|| "Move the link with link instead.".to_owned()),
                (!required_many.is_empty())
                    .then(|| "Link another target before unlinking the last.".to_owned()),
            ]),
        ));
    }
    if !many.is_empty() {
        unlink_errors.push(refusal(
            "not_linked",
            None,
            "Read behaviors.Links.targets for the targets the link holds.",
        ));
    }
    let mut give = format!(
        "behaviors.Links gives the links from the create, by name ({}): each the target's id",
        list(&names, "and")
    );
    if !revisions.is_empty() {
        give.push_str(&format!(
            ", or {{ id, revision }} for {}",
            list(&revisions, "and")
        ));
    }
    if !releases.is_empty() {
        give.push_str(&format!(
            ", or {{ id, release }} for {}",
            list(&releases, "and")
        ));
    }
    give.push('.');
    let mut operations = vec![
        (
            "link".to_owned(),
            said(
                Some(sentences(&[
                    (!single.is_empty()).then(|| {
                        format!(
                            "Use to point {} at an instance of its schema, moving it when it is set.",
                            list(&single, "or")
                        )
                    }),
                    (!many.is_empty()).then(|| {
                        format!(
                            "Use to add a target to {}, or to pin again one it holds.",
                            list(&many, "or")
                        )
                    }),
                    records(&revisions, "revision"),
                    records(&releases, "release"),
                ])),
                Some("Do not use at create; give the links in behaviors.Links instead.".to_owned()),
                Some("Returns the link: its name, schema, id and the revision or release a pinned link records.".to_owned()),
                link_errors,
            ),
        ),
        (
            "unlink".to_owned(),
            said(
                nonempty(sentences(&[
                    (!optional_one.is_empty())
                        .then(|| format!("Use to clear {}.", list(&optional_one, "or"))),
                    (!many.is_empty()).then(|| {
                        format!(
                            "Use with the target's id to remove one target of {}.",
                            list(&many, "or")
                        )
                    }),
                ])),
                nonempty(sentences(&[
                    (!required_one.is_empty()).then(|| {
                        format!(
                            "Do not use on {}: a required link is moved with link, never unlinked.",
                            list(&required_one, "or")
                        )
                    }),
                    (!required_many.is_empty()).then(|| {
                        format!(
                            "Do not remove the last target of {}: a required link keeps one.",
                            list(&required_many, "or")
                        )
                    }),
                ])),
                Some("Returns the link as it was.".to_owned()),
                unlink_errors,
            ),
        ),
        (
            "listLinked".to_owned(),
            OperationGuidance {
                use_when: Some(sentences(&[
                    Some(format!(
                        "Use to find the {type_name} instances whose link points at a target."
                    )),
                    (pinned > 0).then(|| {
                        "With stale true, only the pinned ones the target has moved past."
                            .to_owned()
                    }),
                ])),
                success: Some(
                    "Returns items and next; pass next as cursor for the page after, until it is null."
                        .to_owned(),
                ),
                ..OperationGuidance::default()
            },
        ),
        (
            "create".to_owned(),
            OperationGuidance {
                use_when: Some(sentences(&[
                    Some(give),
                    (!many.is_empty()).then(|| {
                        format!(
                            "Give {}, each target once.",
                            list(
                                &many
                                    .iter()
                                    .map(|name| format!(
                                        "{name} a list of up to {}",
                                        config
                                            .get(name)
                                            .and_then(|link| link.max)
                                            .unwrap_or_default()
                                    ))
                                    .collect::<Vec<_>>(),
                                "and"
                            )
                        )
                    }),
                    (!required.is_empty()).then(|| {
                        format!(
                            "{} {} required.",
                            list(&required, "and"),
                            if required.len() == 1 { "is" } else { "are" }
                        )
                    }),
                ])),
                errors: no_pin,
                ..OperationGuidance::default()
            },
        ),
    ];
    if !self_one.is_empty() || !self_many.is_empty() {
        operations.push((
            "delete".to_owned(),
            OperationGuidance {
                do_not_use_when: Some(sentences(&[
                    (!self_one.is_empty()).then(|| {
                        format!(
                            "Do not delete an instance that the {} link of another {type_name} points at.",
                            list(&self_one, "or")
                        )
                    }),
                    (!self_many.is_empty()).then(|| {
                        format!(
                            "Do not delete the last target of the {} link of another {type_name}.",
                            list(&self_many, "or")
                        )
                    }),
                ])),
                errors: vec![refusal(
                    "required_target",
                    None,
                    "Move the required links that point at it first.",
                )],
                ..OperationGuidance::default()
            },
        ));
    }
    BehaviorGuidance {
        summary,
        operations,
    }
}

// target_schema is the schema of one target a create gives a link: its id,
// or an object with its id and the pin to record.
fn target_schema(link: &LinkSpec, description: String) -> Value {
    let mut properties = Map::new();
    properties.insert(
        "id".into(),
        json!({"description": "The target's id.", "type": "string", "pattern": ID_PATTERN}),
    );
    if let Some(pin) = link.pin {
        properties.insert(
            pin.as_str().into(),
            json!({
                "description": format!("The target's {} to record, one it has had.", pin.as_str()),
                "type": "integer",
                "minimum": 1
            }),
        );
    }
    json!({
        "description": description,
        "type": ["string", "object"],
        "pattern": ID_PATTERN,
        "additionalProperties": false,
        "required": ["id"],
        "properties": properties
    })
}

/// Links' create parameters under its config (`linksCreateParams`): a
/// property per link name, the required ones required, a revision only for
/// a link that pins revisions and a release only for one that pins
/// releases, and for a many-valued link a list of at most max targets, at
/// least one when it is required.
pub(crate) fn create_params(config: &LinksConfig) -> Value {
    let mut properties = Map::new();
    for (name, link) in &config.links {
        let what = match link.pin {
            None => "its id".to_owned(),
            Some(pin) => format!(
                "its id, or an object with its id and the {} to record, its latest when absent",
                pin.as_str()
            ),
        };
        let property = if link.many {
            let mut list = Map::new();
            list.insert(
                "description".into(),
                Value::from(format!(
                    "The {} instances {name} points at, in order, each once: {what}.",
                    link.schema
                )),
            );
            list.insert("type".into(), Value::from("array"));
            if link.required {
                list.insert("minItems".into(), Value::from(1));
            }
            list.insert("maxItems".into(), link.max.map_or(Value::Null, Value::from));
            list.insert(
                "items".into(),
                target_schema(
                    link,
                    format!("One {} instance {name} points at: {what}.", link.schema),
                ),
            );
            Value::Object(list)
        } else {
            target_schema(
                link,
                format!("The {} instance {name} points at: {what}.", link.schema),
            )
        };
        properties.insert(name.clone(), property);
    }
    let required: Vec<Value> = config
        .links
        .iter()
        .filter(|(_, link)| link.required)
        .map(|(name, _)| Value::from(name.as_str()))
        .collect();
    let mut out = Map::new();
    out.insert(
        "description".into(),
        Value::from(
            "The links the instance holds from its create, by name, each held to link's rules.",
        ),
    );
    out.insert("type".into(), Value::from("object"));
    out.insert("additionalProperties".into(), Value::Bool(false));
    out.insert("properties".into(), Value::Object(properties));
    if !required.is_empty() {
        out.insert("required".into(), Value::Array(required));
    }
    Value::Object(out)
}
