//! A host over the in-memory store for the handler's tests: a clock, a
//! request id and a UUID per request, and the runner.

#![allow(dead_code)]

use serde_json::{Map, Value};
use superschematic_rules::{Caller, Code, Core, Refusal};
use superschematic_rules_handler::{
    run_due, serve, Context, Declarations, Handler, MemoryStore, RanDue, Request,
};

pub struct Host {
    pub core: Core,
    pub declarations: Declarations,
    pub store: MemoryStore,
    pub now: i64,
    requests: u64,
}

pub fn object(value: Value) -> Map<String, Value> {
    value.as_object().cloned().expect("an object")
}

impl Host {
    pub fn new() -> Self {
        Host {
            core: superschematic_rules_behaviors::core(),
            declarations: Declarations::ported(),
            store: MemoryStore::new(),
            now: 1_000_000,
            requests: 0,
        }
    }

    fn context(&mut self, caller: &Caller) -> Context {
        self.requests += 1;
        Context {
            caller: caller.clone(),
            now: self.now,
            request: format!("r{}", self.requests),
            id: u128::from(self.requests) << 64 | 0x4000_8000_0000_0000,
        }
    }

    pub fn as_(&mut self, caller: &Caller, request: &Request) -> Result<Value, Refusal> {
        let context = self.context(caller);
        let handler = Handler::new(&self.core, &self.declarations);
        serve(&handler, &mut self.store, request, &context)
    }

    pub fn run(&mut self, request: &Request) -> Result<Value, Refusal> {
        self.as_(&admin(), request)
    }

    pub fn ok(&mut self, request: &Request) -> Value {
        self.run(request)
            .unwrap_or_else(|refusal| panic!("{request:?}: {}", refusal.message))
    }

    pub fn refused(&mut self, request: &Request) -> Refusal {
        match self.run(request) {
            Ok(value) => panic!("{request:?} answered {value}"),
            Err(refusal) => refusal,
        }
    }

    /// Defines and publishes a document.
    pub fn publish(&mut self, document: Value) -> Value {
        let defined = self.ok(&Request::Define { document });
        let type_name = defined["type"].as_str().expect("a type").to_owned();
        self.ok(&Request::Publish {
            type_name,
            discard: false,
        })
    }

    pub fn create(&mut self, type_name: &str, data: Value) -> Result<Value, Refusal> {
        self.run(&Request::Create {
            type_name: type_name.into(),
            data: object(data),
            params: Map::new(),
        })
    }

    pub fn update(&mut self, type_name: &str, id: &str, patch: Value) -> Result<Value, Refusal> {
        self.run(&Request::Update {
            type_name: type_name.into(),
            id: id.into(),
            patch: object(patch),
            preconditions: Map::new(),
        })
    }

    pub fn get(&mut self, type_name: &str, id: &str) -> Result<Value, Refusal> {
        self.run(&Request::Get {
            type_name: type_name.into(),
            id: id.into(),
        })
    }

    pub fn list(&mut self, type_name: &str, filter: Value) -> Vec<String> {
        let page = self.ok(&Request::List {
            type_name: type_name.into(),
            filter,
            sort: Vec::new(),
            limit: None,
            cursor: None,
        });
        page["items"]
            .as_array()
            .expect("items")
            .iter()
            .map(|item| item["title"].as_str().unwrap_or_default().to_owned())
            .collect()
    }

    /// Runs the change queue and the deadlines due now.
    pub fn run_due(&mut self) -> Vec<RanDue> {
        let handler = Handler::new(&self.core, &self.declarations);
        let mut n = 1_000_000u64;
        let mut fresh = || {
            n += 1;
            (format!("run-{n}"), u128::from(n) << 64)
        };
        run_due(&handler, &mut self.store, self.now, &runner(), &mut fresh)
            .expect("the runner runs")
    }
}

pub fn admin() -> Caller {
    Caller::new("admin", ["orders", "notes", "projects", "accounts"])
}

pub fn runner() -> Caller {
    Caller::new("runner", Vec::<String>::new())
}

pub fn code(refusal: &Refusal) -> Code {
    refusal.code
}
