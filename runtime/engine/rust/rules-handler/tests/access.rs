//! The access policy as data (D58's form, D117): the handler asks it
//! before define, publish and each instance request, and before a
//! behavior's operation, `write` or `read` as the operation's declaration
//! says, naming it; beside it the behaviors' own permissions still hold.

mod common;

use common::{object, Host};
use serde_json::{json, Map, Value};
use superschematic_rules::{Caller, Code, Core};
use superschematic_rules_handler::{
    serve, Context, Declarations, Handler, MemoryStore, Policy, Request,
};

struct Guarded {
    core: Core,
    declarations: Declarations,
    policy: Policy,
    store: MemoryStore,
    n: u64,
}

impl Guarded {
    fn new(policy: Value) -> Self {
        Guarded {
            core: superschematic_rules_behaviors::core(),
            declarations: Declarations::ported(),
            policy: Policy::from_json(&policy).expect("the policy reads"),
            store: MemoryStore::new(),
            n: 0,
        }
    }

    fn as_(
        &mut self,
        caller: &Caller,
        request: &Request,
    ) -> Result<Value, superschematic_rules::Refusal> {
        self.n += 1;
        let context = Context {
            caller: caller.clone(),
            now: 1_000_000,
            request: format!("r{}", self.n),
            id: u128::from(self.n) << 64 | 0x4000_8000_0000_0000,
        };
        let handler = Handler::new(&self.core, &self.declarations).with_policy(&self.policy);
        serve(&handler, &mut self.store, request, &context)
    }
}

fn task() -> Value {
    json!({"name": "Task",
    "fields": [{"name": "title", "typeRef": {"name": "string"}}],
    "behaviors": [
        {"name": "Workflow", "field": "status", "config": {"states": ["todo", "done"], "transitions": [{"from": "todo", "to": "done"}]}},
        {"name": "Dependencies", "config": {"schemas": ["Task"]}}
    ]})
}

fn operation(id: &str, use_id: &str, name: &str, params: Value) -> Request {
    Request::Operation {
        type_name: "Task".into(),
        id: id.into(),
        use_id: use_id.into(),
        operation: name.into(),
        params,
        preconditions: Map::new(),
    }
}

#[test]
fn each_request_asks_the_policy_before_anything_is_read() {
    let mut host = Guarded::new(json!({"rules": [
        {"subject": "mallory", "allow": false},
        {"action": ["define", "publish"], "require": "schemas.{action}"},
        {"action": "write", "operation": "transition", "require": "{schema}.move"},
        {"action": "read", "allow": true},
        {"action": "write", "require": "{schema}.write"}
    ]}));
    let owner = Caller::new("owner", ["schemas", "Task.write", "Task.move"]);
    let reader = Caller::new("reader", Vec::<String>::new());
    let mover = Caller::new("mover", ["Task.move"]);
    let mallory = Caller::new("mallory", ["schemas", "Task"]);

    let refused = host
        .as_(&reader, &Request::Define { document: task() })
        .unwrap_err();
    assert_eq!(refused.code, Code::Forbidden);
    assert_eq!(
        refused.message,
        "reader may not define Task in namespace default"
    );
    host.as_(&owner, &Request::Define { document: task() })
        .expect("defined");
    let publish = Request::Publish {
        type_name: "Task".into(),
        discard: false,
    };
    assert_eq!(
        host.as_(&reader, &publish).unwrap_err().code,
        Code::Forbidden
    );
    host.as_(&owner, &publish).expect("published");

    let create = |id: &str| Request::Create {
        type_name: "Task".into(),
        data: object(json!({"id": id, "title": id})),
        params: Map::new(),
    };
    assert_eq!(
        host.as_(&reader, &create("t1")).unwrap_err().message,
        "reader may not write Task in namespace default"
    );
    host.as_(&owner, &create("t1")).expect("t1");
    host.as_(&owner, &create("t2")).expect("t2");
    // A read of a row that does not exist, and of a type that does not,
    // is refused alike to a caller who may not read it.
    for request in [
        Request::Get {
            type_name: "Task".into(),
            id: "missing".into(),
        },
        Request::Get {
            type_name: "Unknown".into(),
            id: "t1".into(),
        },
    ] {
        assert_eq!(
            host.as_(&mallory, &request).unwrap_err().code,
            Code::Forbidden
        );
    }
    assert_eq!(
        host.as_(
            &reader,
            &Request::Get {
                type_name: "Unknown".into(),
                id: "t1".into()
            }
        )
        .unwrap_err()
        .code,
        Code::NotFound
    );

    // An operation that writes asks write and names itself; one that only
    // reads asks read.
    let transition = operation("t1", "status", "transition", json!({"to": "done"}));
    assert_eq!(
        host.as_(&reader, &transition).unwrap_err().message,
        "reader may not call transition (write) on Task in namespace default"
    );
    let block = operation("t1", "Dependencies", "addBlocker", json!({"id": "t2"}));
    assert_eq!(
        host.as_(&mover, &block).unwrap_err().message,
        "mover may not call addBlocker (write) on Task in namespace default"
    );
    host.as_(&owner, &block).expect("blocked");
    let listed = host
        .as_(
            &reader,
            &operation("t1", "Dependencies", "listBlockers", json!({})),
        )
        .expect("a read anyone may make");
    assert_eq!(
        listed["items"].as_array().map(Vec::len),
        Some(1),
        "{listed}"
    );
    let moved = host.as_(&mover, &transition);
    // The policy admitted it; the behaviors' own rules still decide.
    assert_ne!(
        moved.as_ref().err().map(|refusal| refusal.code),
        Some(Code::Forbidden)
    );

    // The runner's requests ask nothing, and a caller needs a subject.
    let nobody = Caller::new("", Vec::<String>::new());
    assert_eq!(
        host.as_(&nobody, &create("t3")).unwrap_err().code,
        Code::InvalidArgument
    );
    let mut standing = Caller::standing_in("service:worker");
    standing.permissions.push("Task.write".into());
    assert_eq!(
        host.as_(&standing, &create("t3")).unwrap_err().code,
        Code::InvalidArgument
    );
}

#[test]
fn the_permission_shorthand_asks_for_the_types_action() {
    let mut host = Guarded::new(json!("permission"));
    let owner = Caller::new("owner", ["Task"]);
    host.as_(&owner, &Request::Define { document: task() })
        .expect("defined");
    host.as_(
        &owner,
        &Request::Publish {
            type_name: "Task".into(),
            discard: false,
        },
    )
    .expect("published");
    let reader = Caller::new("reader", ["Task.read"]);
    let list = Request::List {
        type_name: "Task".into(),
        filter: Value::Null,
        sort: Vec::new(),
        limit: None,
        cursor: None,
    };
    host.as_(&reader, &list).expect("listed");
    let describe = Request::Describe {
        type_name: "Task".into(),
    };
    host.as_(&reader, &describe).expect("described");
    let delete = Request::Delete {
        type_name: "Task".into(),
        id: "t1".into(),
        preconditions: Map::new(),
    };
    assert_eq!(
        host.as_(&reader, &delete).unwrap_err().code,
        Code::Forbidden
    );
    // A handler with no policy allows every caller everything.
    let mut open = Host::new();
    open.publish(task());
}
