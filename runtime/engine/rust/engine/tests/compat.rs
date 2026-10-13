//! The compatibility rule, case by case, as `test/compat.test.ts` has them:
//! each case publishes the base schema, changes a copy, and defines and
//! publishes it as the next version, on SQLite and, with the variable set,
//! on Postgres.

mod support;

use serde_json::{json, Value};

use superschematic_engine::{
    DefineOptions, Engine, EngineErrorCode, PublishResult, Result, SchemaChange, SchemaTarget,
};
use support::{alice, refusal, schema_document, targets, Target};

const ANY: SchemaTarget<'static> = SchemaTarget { namespace: None };

fn base() -> Value {
    schema_document(
        "Order",
        json!([
            {"name": "title", "typeRef": {"name": "string"}, "required": true, "validateMinLength": 2, "validateMaxLength": 40},
            {"name": "note", "typeRef": {"name": "string"}, "validatePattern": "^[a-z ]*$", "description": "For the packer."},
            {"name": "quantity", "typeRef": {"name": "number"}, "validateMin": 1, "validateMax": 100},
            {"name": "tags", "typeRef": {"name": "string", "isArray": true}, "validateListMin": 1, "validateListMax": 5},
            {"name": "status", "typeRef": {"name": "OrderStatus"}},
            {"name": "lines", "typeRef": {"name": "OrderLine", "isArray": true}},
            {"name": "code", "typeRef": {"name": "Local.Code"}},
            {"name": "placedBy", "typeRef": {"name": "string"}, "jsonTag": "placed_by"}
        ]),
        json!({
            "enums": {
                "OrderStatus": {"name": "OrderStatus", "values": [{"name": "OPEN", "serializedAs": "open"}, {"name": "SHIPPED", "serializedAs": "shipped"}]},
                "Colour": {"name": "Colour", "values": [{"name": "RED"}, {"name": "BLUE"}]}
            },
            "types": {
                "OrderLine": {
                    "name": "OrderLine",
                    "role": "EmbeddedStruct",
                    "fields": [
                        {"name": "sku", "typeRef": {"name": "string"}, "required": true},
                        {"name": "count", "typeRef": {"name": "number"}}
                    ]
                },
                "Sketch": {"name": "Sketch", "role": "EmbeddedStruct", "fields": [{"name": "colour", "typeRef": {"name": "Colour"}}]}
            },
            "scalars": {
                "Local.Code": {"name": "Local.Code", "languagePrimitive": "string", "pattern": "^[A-Z]+$", "minLength": 1, "maxLength": 8}
            }
        }),
    )
}

fn field<'d>(document: &'d mut Value, type_name: &str, name: &str) -> &'d mut Value {
    document["types"][type_name]["fields"]
        .as_array_mut()
        .unwrap()
        .iter_mut()
        .find(|field| field["name"] == name)
        .unwrap_or_else(|| panic!("{type_name}.{name}"))
}

fn fields<'d>(document: &'d mut Value, type_name: &str) -> &'d mut Vec<Value> {
    document["types"][type_name]["fields"]
        .as_array_mut()
        .unwrap()
}

fn remove(value: &mut Value, key: &str) {
    value.as_object_mut().unwrap().remove(key);
}

// next_version publishes the base and returns the define and publish of
// the changed copy.
fn next_version(target: &Target, change: &dyn Fn(&mut Value)) -> (Engine, Value) {
    let engine = target.open();
    engine
        .schemas()
        .define(&alice(), base(), DefineOptions::default())
        .unwrap();
    engine.schemas().publish(&alice(), "Order", ANY).unwrap();
    let mut next = base();
    change(&mut next);
    (engine, next)
}

fn define_and_publish(engine: &Engine, next: &Value) -> Result<PublishResult> {
    engine
        .schemas()
        .define(&alice(), next, DefineOptions::default())?;
    engine.schemas().publish(&alice(), "Order", ANY)
}

type Change = Box<dyn Fn(&mut Value)>;

fn compatible() -> Vec<(&'static str, Change)> {
    vec![
        (
            "a new optional field",
            Box::new(|doc: &mut Value| {
                fields(doc, "Order")
                    .push(json!({"name": "giftWrap", "typeRef": {"name": "boolean"}}))
            }),
        ),
        (
            "a new optional field on a nested type",
            Box::new(|doc: &mut Value| {
                fields(doc, "OrderLine")
                    .push(json!({"name": "note", "typeRef": {"name": "string"}}))
            }),
        ),
        (
            "a new enum value",
            Box::new(|doc: &mut Value| {
                doc["enums"]["OrderStatus"]["values"]
                    .as_array_mut()
                    .unwrap()
                    .push(json!({"name": "CANCELLED", "serializedAs": "cancelled"}))
            }),
        ),
        (
            "a lower minLength",
            Box::new(|doc: &mut Value| {
                field(doc, "Order", "title")["validateMinLength"] = json!(1)
            }),
        ),
        (
            "a dropped minLength",
            Box::new(|doc: &mut Value| remove(field(doc, "Order", "title"), "validateMinLength")),
        ),
        (
            "a higher maxLength",
            Box::new(|doc: &mut Value| {
                field(doc, "Order", "title")["validateMaxLength"] = json!(80)
            }),
        ),
        (
            "a dropped maxLength",
            Box::new(|doc: &mut Value| remove(field(doc, "Order", "title"), "validateMaxLength")),
        ),
        (
            "a lower min",
            Box::new(|doc: &mut Value| field(doc, "Order", "quantity")["validateMin"] = json!(0)),
        ),
        (
            "a higher max",
            Box::new(|doc: &mut Value| {
                field(doc, "Order", "quantity")["validateMax"] = json!(1000)
            }),
        ),
        (
            "a lower listMin",
            Box::new(|doc: &mut Value| field(doc, "Order", "tags")["validateListMin"] = json!(0)),
        ),
        (
            "a higher listMax",
            Box::new(|doc: &mut Value| field(doc, "Order", "tags")["validateListMax"] = json!(10)),
        ),
        (
            "a required field made optional",
            Box::new(|doc: &mut Value| remove(field(doc, "Order", "title"), "required")),
        ),
        (
            "a dropped pattern",
            Box::new(|doc: &mut Value| remove(field(doc, "Order", "note"), "validatePattern")),
        ),
        (
            "documentation, defaults and display metadata",
            Box::new(|doc: &mut Value| {
                doc["description"] = json!("Orders placed in the shop.");
                let note = field(doc, "Order", "note").as_object_mut().unwrap();
                note.insert("description".into(), json!("For whoever packs it."));
                note.insert("comment".into(), json!("free text"));
                note.insert("title".into(), json!("Note"));
                note.insert("placeholder".into(), json!("Leave at the door"));
                field(doc, "Order", "quantity")["default"] = json!("1");
            }),
        ),
        (
            "a change to a type no field reaches",
            Box::new(|doc: &mut Value| doc["types"]["Sketch"]["fields"] = json!([])),
        ),
        (
            "a change to an enum no field reaches",
            Box::new(|doc: &mut Value| {
                doc["enums"]["Colour"]["values"]
                    .as_array_mut()
                    .unwrap()
                    .pop();
            }),
        ),
        (
            "a wider scalar",
            Box::new(|doc: &mut Value| {
                let code = &mut doc["scalars"]["Local.Code"];
                code["maxLength"] = json!(16);
                code["minLength"] = json!(0);
            }),
        ),
    ]
}

#[test]
fn changes_every_stored_instance_still_satisfies() {
    for (name, change) in compatible() {
        for target in targets(name) {
            let (engine, next) = next_version(&target, &change);
            assert_eq!(
                define_and_publish(&engine, &next).unwrap(),
                PublishResult {
                    namespace: "default".into(),
                    name: "Order".into(),
                    version: 2,
                    published: true
                },
                "{name}"
            );
        }
    }
}

fn incompatible() -> Vec<(&'static str, Change, Vec<&'static str>)> {
    vec![
        (
            "a removed field",
            Box::new(|doc: &mut Value| fields(doc, "Order").retain(|f| f["name"] != "note")),
            vec!["field Order.note is removed"],
        ),
        (
            "a renamed field",
            Box::new(|doc: &mut Value| field(doc, "Order", "note")["name"] = json!("remark")),
            vec!["field Order.note is removed"],
        ),
        (
            "a new JSON key",
            Box::new(|doc: &mut Value| remove(field(doc, "Order", "placedBy"), "jsonTag")),
            vec!["field Order.placedBy changes its JSON key from placed_by to placedBy"],
        ),
        (
            "a changed type",
            Box::new(|doc: &mut Value| {
                field(doc, "Order", "quantity")["typeRef"] = json!({"name": "string"})
            }),
            vec!["field Order.quantity changes type from number to string"],
        ),
        (
            "a single value made a list",
            Box::new(|doc: &mut Value| {
                field(doc, "Order", "title")["typeRef"] = json!({"name": "string", "isArray": true})
            }),
            vec!["field Order.title changes type from string to string[]"],
        ),
        (
            "a list made a list of lists",
            Box::new(|doc: &mut Value| {
                field(doc, "Order", "tags")["typeRef"] =
                    json!({"name": "string", "isArray": true, "isArrayOfArrays": true})
            }),
            vec!["field Order.tags changes type from string[] to string[][]"],
        ),
        (
            "an enum field retyped, with its enum removed",
            Box::new(|doc: &mut Value| {
                field(doc, "Order", "status")["typeRef"] = json!({"name": "string"});
                remove(&mut doc["enums"], "OrderStatus");
            }),
            vec!["field Order.status changes type from OrderStatus to string"],
        ),
        (
            "an optional field made required",
            Box::new(|doc: &mut Value| field(doc, "Order", "note")["required"] = json!(true)),
            vec!["field Order.note becomes required"],
        ),
        (
            "a new required field",
            Box::new(|doc: &mut Value| {
                fields(doc, "Order").push(
                    json!({"name": "currency", "typeRef": {"name": "string"}, "required": true}),
                )
            }),
            vec!["field Order.currency is added as required"],
        ),
        (
            "a higher minLength",
            Box::new(|doc: &mut Value| {
                field(doc, "Order", "title")["validateMinLength"] = json!(3)
            }),
            vec!["field Order.title raises minLength from 2 to 3"],
        ),
        (
            "a new minLength",
            Box::new(|doc: &mut Value| field(doc, "Order", "note")["validateMinLength"] = json!(1)),
            vec!["field Order.note gains minLength 1"],
        ),
        (
            "a lower maxLength",
            Box::new(|doc: &mut Value| {
                field(doc, "Order", "title")["validateMaxLength"] = json!(20)
            }),
            vec!["field Order.title lowers maxLength from 40 to 20"],
        ),
        (
            "a new maxLength",
            Box::new(|doc: &mut Value| {
                field(doc, "Order", "note")["validateMaxLength"] = json!(10)
            }),
            vec!["field Order.note gains maxLength 10"],
        ),
        (
            "a higher min",
            Box::new(|doc: &mut Value| field(doc, "Order", "quantity")["validateMin"] = json!(2)),
            vec!["field Order.quantity raises min from 1 to 2"],
        ),
        (
            "a lower max",
            Box::new(|doc: &mut Value| field(doc, "Order", "quantity")["validateMax"] = json!(50)),
            vec!["field Order.quantity lowers max from 100 to 50"],
        ),
        (
            "a new min",
            Box::new(|doc: &mut Value| field(doc, "OrderLine", "count")["validateMin"] = json!(1)),
            vec!["field OrderLine.count gains min 1"],
        ),
        (
            "a higher listMin",
            Box::new(|doc: &mut Value| field(doc, "Order", "tags")["validateListMin"] = json!(2)),
            vec!["field Order.tags raises listMin from 1 to 2"],
        ),
        (
            "a lower listMax",
            Box::new(|doc: &mut Value| field(doc, "Order", "tags")["validateListMax"] = json!(3)),
            vec!["field Order.tags lowers listMax from 5 to 3"],
        ),
        (
            "a changed pattern",
            Box::new(|doc: &mut Value| {
                field(doc, "Order", "note")["validatePattern"] = json!("^[a-z]*$")
            }),
            vec!["field Order.note changes its pattern"],
        ),
        (
            "a new pattern",
            Box::new(|doc: &mut Value| {
                field(doc, "Order", "title")["validatePattern"] = json!("^[A-Z]")
            }),
            vec!["field Order.title gains a pattern"],
        ),
        (
            "a removed enum value",
            Box::new(|doc: &mut Value| {
                doc["enums"]["OrderStatus"]["values"]
                    .as_array_mut()
                    .unwrap()
                    .pop();
            }),
            vec!["enum OrderStatus drops value shipped"],
        ),
        (
            "a renamed enum value",
            Box::new(|doc: &mut Value| {
                doc["enums"]["OrderStatus"]["values"][0]["serializedAs"] = json!("opened")
            }),
            vec!["enum OrderStatus drops value open"],
        ),
        (
            "a removed nested field",
            Box::new(|doc: &mut Value| {
                fields(doc, "OrderLine").pop();
            }),
            vec!["field OrderLine.count is removed"],
        ),
        (
            "a nested field made required",
            Box::new(|doc: &mut Value| field(doc, "OrderLine", "count")["required"] = json!(true)),
            vec!["field OrderLine.count becomes required"],
        ),
        (
            "a scalar with a changed pattern",
            Box::new(|doc: &mut Value| {
                doc["scalars"]["Local.Code"]["pattern"] = json!("^[A-Z]{2,}$")
            }),
            vec!["scalar Local.Code changes its pattern"],
        ),
        (
            "a scalar with a higher minLength",
            Box::new(|doc: &mut Value| doc["scalars"]["Local.Code"]["minLength"] = json!(2)),
            vec!["scalar Local.Code raises minLength from 1 to 2"],
        ),
        (
            "a scalar with a lower maxLength",
            Box::new(|doc: &mut Value| doc["scalars"]["Local.Code"]["maxLength"] = json!(4)),
            vec!["scalar Local.Code lowers maxLength from 8 to 4"],
        ),
        (
            "a scalar with reserved words",
            Box::new(|doc: &mut Value| {
                doc["scalars"]["Local.Code"]["reservedWords"] = json!(["NONE"])
            }),
            vec!["scalar Local.Code reserves NONE"],
        ),
        (
            "a scalar with another primitive",
            Box::new(|doc: &mut Value| {
                let code = &mut doc["scalars"]["Local.Code"];
                code["languagePrimitive"] = json!("number");
                for key in ["pattern", "minLength", "maxLength"] {
                    remove(code, key);
                }
            }),
            vec!["scalar Local.Code changes primitive from String to Float"],
        ),
        (
            "several changes, named together",
            Box::new(|doc: &mut Value| {
                fields(doc, "Order").retain(|f| f["name"] != "note");
                field(doc, "Order", "title")["validateMinLength"] = json!(5);
                doc["enums"]["OrderStatus"]["values"]
                    .as_array_mut()
                    .unwrap()
                    .remove(0);
            }),
            vec![
                "field Order.title raises minLength from 2 to 5",
                "field Order.note is removed",
                "enum OrderStatus drops value open",
            ],
        ),
    ]
}

#[test]
fn changes_a_stored_instance_may_not_satisfy() {
    for (name, change, messages) in incompatible() {
        for target in targets(name) {
            let (engine, next) = next_version(&target, &change);
            let error = refusal(define_and_publish(&engine, &next));
            assert_eq!(error.code(), EngineErrorCode::IncompatibleChange, "{name}");
            assert_eq!(error.name(), "IncompatibleChangeError", "{name}");
            let found: Vec<&str> = error
                .changes()
                .iter()
                .map(|change| change.message.as_str())
                .collect();
            assert_eq!(found, messages, "{name}");
            for message in &messages {
                assert!(
                    error.message().contains(message),
                    "{name}: {}",
                    error.message()
                );
            }
            assert!(
                error
                    .message()
                    .ends_with("publish any other change under a new schema name"),
                "{name}: {}",
                error.message()
            );
        }
    }
}

#[test]
fn a_refused_version_leaves_the_live_version_and_the_draft_as_they_were() {
    for target in targets("a_refused_version_leaves_the_live_version_and_the_draft_as_they_were") {
        let engine = target.open();
        engine
            .schemas()
            .define(&alice(), base(), DefineOptions::default())
            .unwrap();
        engine.schemas().publish(&alice(), "Order", ANY).unwrap();
        let mut compatible = base();
        compatible["description"] = json!("kept");
        engine
            .schemas()
            .define(&alice(), &compatible, DefineOptions::default())
            .unwrap();
        let mut incompatible = base();
        field(&mut incompatible, "Order", "note")["required"] = json!(true);
        assert_eq!(
            refusal(
                engine
                    .schemas()
                    .define(&alice(), &incompatible, DefineOptions::default())
            )
            .code(),
            EngineErrorCode::IncompatibleChange
        );
        assert_eq!(
            engine
                .schemas()
                .live(&alice(), "Order", ANY)
                .unwrap()
                .unwrap()
                .version,
            Some(1)
        );
        assert_eq!(
            engine
                .schemas()
                .draft(&alice(), "Order", ANY)
                .unwrap()
                .unwrap()
                .document["description"],
            "kept"
        );
    }
}

#[test]
fn a_new_instance_type_is_refused() {
    for target in targets("a_new_instance_type_is_refused") {
        let engine = target.open();
        let catalog = |type_name: &str| {
            json!({
                "kind": "General",
                "name": "Catalog",
                "types": {type_name: {"name": type_name, "role": "EmbeddedStruct", "fields": [{"name": "title", "typeRef": {"name": "string"}}]}}
            })
        };
        engine
            .schemas()
            .define(&alice(), catalog("Item"), DefineOptions::default())
            .unwrap();
        engine.schemas().publish(&alice(), "Catalog", ANY).unwrap();
        let error = refusal(engine.schemas().define(
            &alice(),
            catalog("Product"),
            DefineOptions::default(),
        ));
        assert_eq!(
            error.changes(),
            [SchemaChange::new(
                "Item",
                "the instance type changes from Item to Product"
            )]
        );
    }
}
