//! What the adapter's tests on real databases share: the layout rule's
//! names as the generators write them, a parameter each dialect binds, the
//! `@optimistic` triggers sqlgen writes, a Postgres schema of a test's own
//! on the database `SUPERSCHEMATIC_RULES_TEST_DATABASE_URL` names, and the
//! toy behaviors the tests register beside Workflow.

// Each test file uses some of what is here.
#![allow(dead_code)]

use serde_json::{json, Value};
use superschematic_rules::{
    Behavior, ConfigError, ConfigTarget, Cx, Declaration, FieldSpec, GuardRequest, Heard, Hook,
    Refusal, TableRef, UseSpec, Veto, View,
};
use superschematic_rules_adapter::{
    Changes, Column, Convert, Deadlines, Events, Layout, Live, Operation, OwnedField, SubTable,
    Table, TypeSpec, Use,
};

/// The variable that names the Postgres the tests run against; the
/// Postgres tests are skipped without it.
pub const RULES_DATABASE_VARIABLE: &str = "SUPERSCHEMATIC_RULES_TEST_DATABASE_URL";

/// A field's name in snake case as the generators write it
/// (`codegen.ToSnakeCase`, the core README's layout rule): a `_` before each
/// capital that follows a lowercase letter or a digit, each `-` a `_`, the
/// whole lowercased. `enteredAt` is `entered_at`, `lastIPAddress`
/// `last_ipaddress`.
pub fn snake(name: &str) -> String {
    let mut out = String::new();
    let mut previous: Option<char> = None;
    for c in name.chars() {
        if c.is_ascii_uppercase()
            && previous.is_some_and(|p| p.is_ascii_lowercase() || p.is_ascii_digit())
        {
            out.push('_');
        }
        out.push(if c == '-' {
            '_'
        } else {
            c.to_ascii_lowercase()
        });
        previous = Some(c);
    }
    out
}

/// A behavior's storage key: its name lowercased, each run of other
/// characters one `_`, none at either end (behaviorlayout's `StorageKey`).
pub fn storage_key(behavior: &str) -> String {
    let mut out = String::new();
    for c in behavior.to_ascii_lowercase().chars() {
        if c.is_ascii_lowercase() || c.is_ascii_digit() {
            out.push(c);
        } else if !out.ends_with('_') {
            out.push('_');
        }
    }
    out.trim_matches('_').to_owned()
}

/// A column's name in camel case: the IR field a type anchor's owned
/// column takes here.
pub fn camel(column: &str) -> String {
    let mut out = String::new();
    let mut upper = false;
    for c in column.chars() {
        if c == '_' {
            upper = true;
        } else if upper {
            out.push(c.to_ascii_uppercase());
            upper = false;
        } else {
            out.push(c);
        }
    }
    out
}

/// A value a test's own SQL binds.
#[derive(Clone, Debug)]
pub enum Param {
    Null,
    Text(String),
    Int(i64),
}

impl Param {
    pub fn text(value: impl Into<String>) -> Param {
        Param::Text(value.into())
    }
}

/// The text `Date.prototype.toISOString` writes for epoch milliseconds,
/// which a SQLite date-time column holds and a Postgres `TIMESTAMPTZ` reads.
pub fn iso(millis: i64) -> String {
    let days = millis.div_euclid(86_400_000);
    let of_day = millis.rem_euclid(86_400_000);
    // Howard Hinnant's civil_from_days.
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z.rem_euclid(146_097);
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let day = doy - (153 * mp + 2) / 5 + 1;
    let month = if mp < 10 { mp + 3 } else { mp - 9 };
    let year = yoe + era * 400 + i64::from(month <= 2);
    format!(
        "{year:04}-{month:02}-{day:02}T{:02}:{:02}:{:02}.{:03}Z",
        of_day / 3_600_000,
        of_day / 60_000 % 60,
        of_day / 1000 % 60,
        of_day % 1000
    )
}

/// The `@optimistic` trigger sqlgen writes on Postgres, which bumps
/// `_version` on every update of a table.
pub fn postgres_bump(table: &str) -> [String; 2] {
    [
        format!(
            "CREATE OR REPLACE FUNCTION \"{table}_bump_version\"() RETURNS trigger AS $$\nBEGIN\n  NEW._version := OLD._version + 1;\n  RETURN NEW;\nEND;\n$$ LANGUAGE plpgsql"
        ),
        format!(
            "CREATE TRIGGER \"trg_{table}_bump_version\" BEFORE UPDATE ON \"{table}\" FOR EACH ROW EXECUTE FUNCTION \"{table}_bump_version\"()"
        ),
    ]
}

/// The `@optimistic` trigger sqlgen writes on SQLite
/// (`internal/sqlmigrate`'s `sqliteBumpTrigger`): after an update that left
/// `_version` as it was, it moves it on.
pub fn sqlite_bump(table: &str) -> String {
    format!(
        "CREATE TRIGGER \"trg_{table}_bump_version\" AFTER UPDATE ON \"{table}\" FOR EACH ROW WHEN NEW._version = OLD._version BEGIN UPDATE \"{table}\" SET _version = OLD._version + 1 WHERE rowid = NEW.rowid; END"
    )
}

/// The column types of a test table in a dialect: text, a timestamp, a
/// version, a key, a number, a whole number (`Generic.Int64`), a boolean,
/// JSON.
pub struct ColumnTypes {
    pub text: &'static str,
    pub timestamp: &'static str,
    pub version: &'static str,
    pub key: &'static str,
    pub number: &'static str,
    pub integer: &'static str,
    pub boolean: &'static str,
    pub json: &'static str,
}

pub fn column_types(postgres: bool) -> ColumnTypes {
    if postgres {
        ColumnTypes {
            text: "TEXT",
            timestamp: "TIMESTAMPTZ",
            version: "BIGINT DEFAULT 1 NOT NULL",
            key: "TEXT PRIMARY KEY",
            number: "DOUBLE PRECISION",
            integer: "BIGINT",
            boolean: "BOOLEAN",
            json: "JSONB",
        }
    } else {
        ColumnTypes {
            text: "TEXT",
            timestamp: "TEXT",
            version: "INTEGER DEFAULT 1 NOT NULL",
            key: "TEXT PRIMARY KEY",
            number: "REAL",
            integer: "INTEGER",
            boolean: "INTEGER",
            json: "TEXT",
        }
    }
}

/// A deadline queue's table in a dialect (D103), as sqlgen writes a
/// queue's: its message's columns (`row_key` of `row_key_type`, the row
/// table's key's), the queue's own, an `id` with the dialect's UUID
/// default, since an arm's insert names none, its live messages unique by
/// row, use and name, and an index of each row's.
pub fn deadline_queue_ddl(postgres: bool, table: &str, row_key_type: &str) -> Vec<String> {
    let t = column_types(postgres);
    let (uuid, now) = if postgres {
        ("UUID DEFAULT gen_random_uuid()", "CURRENT_TIMESTAMP")
    } else {
        (
            "TEXT DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6))))",
            "(strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))",
        )
    };
    let token = if postgres { "UUID" } else { "TEXT" };
    vec![
        format!(
            "CREATE TABLE \"{table}\" (id {uuid} NOT NULL PRIMARY KEY, row_key {row_key_type} NOT NULL, use_id TEXT NOT NULL, name TEXT NOT NULL, at BIGINT NOT NULL, payload {json}, state TEXT DEFAULT 'ready' NOT NULL, attempts INTEGER DEFAULT 0 NOT NULL, due_at {timestamp} DEFAULT {now} NOT NULL, claim_expires_at {timestamp}, claim_token {token}, last_error TEXT, created_at {timestamp} DEFAULT {now} NOT NULL, updated_at {timestamp} DEFAULT {now} NOT NULL)",
            json = t.json,
            timestamp = t.timestamp,
        ),
        format!("CREATE UNIQUE INDEX \"{table}_live\" ON \"{table}\" (row_key, use_id, name) WHERE state IN ('ready', 'claimed')"),
        format!("CREATE INDEX \"{table}_row\" ON \"{table}\" (row_key)"),
    ]
}

/// A change queue's table in a dialect (D103), as sqlgen writes the queue
/// the loader adds for a table its behaviors hear: its message's columns
/// (`row_key` of `row_key_type`, the row table's key's), the queue's own,
/// and an `id` with the dialect's UUID default, since the trigger's insert
/// names none.
pub fn change_queue_ddl(postgres: bool, table: &str, row_key_type: &str) -> Vec<String> {
    let t = column_types(postgres);
    let (uuid, now) = if postgres {
        ("UUID DEFAULT gen_random_uuid()", "CURRENT_TIMESTAMP")
    } else {
        (
            "TEXT DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6))))",
            "(strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))",
        )
    };
    let token = if postgres { "UUID" } else { "TEXT" };
    vec![
        format!(
            "CREATE TABLE \"{table}\" (id {uuid} NOT NULL PRIMARY KEY, row_key {row_key_type} NOT NULL, \"write\" TEXT NOT NULL, at BIGINT NOT NULL, \"before\" {json}, \"after\" {json}, state TEXT DEFAULT 'ready' NOT NULL, attempts INTEGER DEFAULT 0 NOT NULL, due_at {timestamp} DEFAULT {now} NOT NULL, claim_expires_at {timestamp}, claim_token {token}, last_error TEXT, created_at {timestamp} DEFAULT {now} NOT NULL, updated_at {timestamp} DEFAULT {now} NOT NULL)",
            json = t.json,
            timestamp = t.timestamp,
        ),
        format!("CREATE INDEX \"{table}_row\" ON \"{table}\" (row_key)"),
    ]
}

/// An event queue's table in a dialect (D104), as sqlgen writes the queue
/// the loader adds for a table whose behaviors emit events: its message's
/// columns (`row_key` of `row_key_type`, the row table's key's), the
/// queue's own, and an `id` with the dialect's UUID default, since an
/// event's insert names none.
pub fn event_queue_ddl(postgres: bool, table: &str, row_key_type: &str) -> Vec<String> {
    let t = column_types(postgres);
    let (uuid, now) = if postgres {
        ("UUID DEFAULT gen_random_uuid()", "CURRENT_TIMESTAMP")
    } else {
        (
            "TEXT DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6))))",
            "(strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))",
        )
    };
    let token = if postgres { "UUID" } else { "TEXT" };
    vec![
        format!(
            "CREATE TABLE \"{table}\" (id {uuid} NOT NULL PRIMARY KEY, row_key {row_key_type} NOT NULL, use_id TEXT NOT NULL, kind TEXT NOT NULL, at BIGINT NOT NULL, payload {json}, state TEXT DEFAULT 'ready' NOT NULL, attempts INTEGER DEFAULT 0 NOT NULL, due_at {timestamp} DEFAULT {now} NOT NULL, claim_expires_at {timestamp}, claim_token {token}, last_error TEXT, created_at {timestamp} DEFAULT {now} NOT NULL, updated_at {timestamp} DEFAULT {now} NOT NULL)",
            json = t.json,
            timestamp = t.timestamp,
        ),
        format!("CREATE INDEX \"{table}_row\" ON \"{table}\" (row_key)"),
    ]
}

/// The layout's event queue of a type's table `<table>_event_queue`, its
/// columns as behaviorlayout names them (D104).
pub fn events_of(type_name: &str, table: &str) -> Events {
    Events {
        queue: format!("{type_name}Event"),
        table: format!("{table}_event_queue"),
        row_key: "row_key".into(),
        use_id: "use_id".into(),
        kind: "kind".into(),
        at: "at".into(),
        payload: "payload".into(),
    }
}

/// A table's change trigger in a dialect (D103), as the contract of sqlgen
/// writes it: on Postgres a function and an AFTER INSERT OR UPDATE OR
/// DELETE trigger, on SQLite a trigger per write, each inserting a message
/// of the queue with the write, the database's clock in epoch milliseconds
/// and the row's image before and after it, JSON by column of `image`; an
/// update that moves no column of the image writes none.
pub fn change_trigger_ddl(postgres: bool, table: &str, queue: &str, image: &[&str]) -> Vec<String> {
    let object = |row: &str| {
        let members: Vec<String> = image
            .iter()
            .map(|column| format!("'{column}', {row}.\"{column}\""))
            .collect();
        let call = if postgres {
            "jsonb_build_object"
        } else {
            "json_object"
        };
        format!("{call}({})", members.join(", "))
    };
    let moved = |not: &str| {
        image
            .iter()
            .map(|column| format!("OLD.\"{column}\" {not} NEW.\"{column}\""))
            .collect::<Vec<_>>()
            .join(" OR ")
    };
    let columns = "(\"row_key\", \"write\", \"at\", \"before\", \"after\"";
    if postgres {
        let at = "floor(extract(epoch FROM clock_timestamp()) * 1000)::bigint";
        return vec![
            format!(
                "CREATE OR REPLACE FUNCTION \"{table}_changes\"() RETURNS trigger AS $$\nBEGIN\n  IF TG_OP = 'UPDATE' AND NOT ({moved}) THEN\n    RETURN NULL;\n  END IF;\n  IF TG_OP = 'INSERT' THEN\n    INSERT INTO \"{queue}\" {columns}, \"due_at\")\n    VALUES (NEW.\"id\", 'insert', {at}, NULL, {new}, clock_timestamp());\n  ELSIF TG_OP = 'UPDATE' THEN\n    INSERT INTO \"{queue}\" {columns}, \"due_at\")\n    VALUES (NEW.\"id\", 'update', {at}, {old}, {new}, clock_timestamp());\n  ELSE\n    INSERT INTO \"{queue}\" {columns}, \"due_at\")\n    VALUES (OLD.\"id\", 'delete', {at}, {old}, NULL, clock_timestamp());\n  END IF;\n  RETURN NULL;\nEND;\n$$ LANGUAGE plpgsql",
                moved = moved("IS DISTINCT FROM"),
                new = object("NEW"),
                old = object("OLD"),
            ),
            format!(
                "CREATE TRIGGER \"trg_{table}_changes\" AFTER INSERT OR UPDATE OR DELETE ON \"{table}\" FOR EACH ROW EXECUTE FUNCTION \"{table}_changes\"()"
            ),
        ];
    }
    let at = "(CAST(strftime('%s', 'now') AS INTEGER) * 1000 + CAST(substr(strftime('%f', 'now'), 4) AS INTEGER))";
    vec![
        format!(
            "CREATE TRIGGER \"trg_{table}_changes_insert\" AFTER INSERT ON \"{table}\" FOR EACH ROW\nBEGIN\n  INSERT INTO \"{queue}\" {columns})\n  VALUES (NEW.\"id\", 'insert', {at}, NULL, {new});\nEND",
            new = object("NEW"),
        ),
        format!(
            "CREATE TRIGGER \"trg_{table}_changes_update\" AFTER UPDATE ON \"{table}\" FOR EACH ROW\nWHEN {moved}\nBEGIN\n  INSERT INTO \"{queue}\" {columns})\n  VALUES (NEW.\"id\", 'update', {at}, {old}, {new});\nEND",
            moved = moved("IS NOT"),
            new = object("NEW"),
            old = object("OLD"),
        ),
        format!(
            "CREATE TRIGGER \"trg_{table}_changes_delete\" AFTER DELETE ON \"{table}\" FOR EACH ROW\nBEGIN\n  INSERT INTO \"{queue}\" {columns})\n  VALUES (OLD.\"id\", 'delete', {at}, {old}, NULL);\nEND",
            old = object("OLD"),
        ),
    ]
}

/// The nth placeholder in a dialect.
pub fn p(postgres: bool, n: usize) -> String {
    if postgres {
        format!("${n}")
    } else {
        format!("?{n}")
    }
}

// ---- the tables the store tests run on ----

pub fn flow() -> Value {
    json!({
        "states": ["draft", "paid", "shipped"],
        "initial": "draft",
        "transitions": [
            {"from": "draft", "to": "paid"},
            {"from": "paid", "to": "shipped", "permission": "orders.ship"}
        ]
    })
}

fn owned(name: &str, field: &str, column: &str, type_name: &str, value: bool) -> OwnedField {
    OwnedField {
        name: name.into(),
        column: Column::new(field, column, type_name),
        value,
    }
}

/// A table written as sqlgen writes one that composes Workflow on status:
/// the owned columns `status` and `status_entered_at` beside the own
/// `title`, and `_version`; its key a string.
pub fn order_table(type_name: &str, table: &str) -> Table {
    let mut entered = owned(
        "enteredAt",
        "statusEnteredAt",
        "status_entered_at",
        "Temporal.DateTime",
        false,
    );
    entered.column = entered.column.converted(Convert::EpochMillis);
    Table {
        type_name: type_name.into(),
        table: table.into(),
        key: Column::new("id", "id", "string"),
        version: "_version".into(),
        live: None,
        own: vec![Column::new("title", "title", "string")],
        audit: Vec::new(),
        uses: vec![Use {
            id: "status".into(),
            behavior: "Workflow".into(),
            member: "status".into(),
            anchor: Some("status".into()),
            fields: vec![owned("status", "status", "status", "string", true), entered],
            operations: vec![Operation::new("transition", true)],
            tables: Vec::new(),
        }],
        deadlines: None,
        changes: None,
        events: None,
        spec: TypeSpec::new(type_name).with(UseSpec::on_field("status", "Workflow", flow())),
    }
}

/// The columns of Question's change trigger's image: its own columns, then
/// its status's owned columns, the key, `_version` and `created_by` left
/// out.
pub const QUESTION_IMAGE: [&str; 4] = ["prompt", "asked_of", "status", "status_entered_at"];

/// Question (D104): Order's shape with the own `prompt` and `askedOf`, a
/// UUID, `createdBy` among its audit fields, a UUID the repositories stamp,
/// and its status a Workflow whose answer is made by the subject `askedOf`
/// holds and whose withdrawal by the one `createdBy` holds, a move to the
/// state a row is in answered as no change; its change queue, which its
/// trigger writes, shows that such a move queues no message.
pub fn question_table() -> Table {
    let flow = json!({
        "states": ["open", "answered", "withdrawn"],
        "initial": "open",
        "transitions": [
            {"from": "open", "to": "answered", "by": ["askedOf"]},
            {"from": "open", "to": "withdrawn", "by": ["createdBy"]}
        ],
        "repeat": "unchanged"
    });
    Table {
        own: vec![
            Column::new("prompt", "prompt", "string"),
            Column::new("askedOf", "asked_of", "Identity.UUID"),
        ],
        audit: vec![Column::new("createdBy", "created_by", "Identity.UUID")],
        changes: Some(Changes {
            queue: "QuestionChange".into(),
            table: "question_change_queue".into(),
            row_key: "row_key".into(),
            write: "write".into(),
            at: "at".into(),
            before: "before".into(),
            after: "after".into(),
        }),
        events: None,
        spec: TypeSpec::new("Question")
            .with(UseSpec::on_field("status", "Workflow", flow))
            .with_field(FieldSpec::new("prompt", "string"))
            .with_field(FieldSpec::new("askedOf", "Identity.UUID"))
            .with_field(FieldSpec::new("createdBy", "Identity.UUID")),
        ..order_table("Question", "question")
    }
}

/// [`question_table`]'s table in a dialect, its UUIDs in Postgres's `UUID`
/// columns and SQLite's text, with its version trigger, its change queue
/// and its change trigger.
pub fn question_ddl(postgres: bool) -> Vec<String> {
    let t = column_types(postgres);
    let uuid = if postgres { "UUID" } else { "TEXT" };
    let mut ddl = vec![format!(
        "CREATE TABLE \"question\" (id {}, prompt {} NOT NULL, asked_of {uuid}, created_by {uuid}, status {}, status_entered_at {}, _version {})",
        t.key, t.text, t.text, t.timestamp, t.version
    )];
    if postgres {
        ddl.extend(postgres_bump("question"));
    } else {
        ddl.push(sqlite_bump("question"));
    }
    ddl.extend(change_queue_ddl(postgres, "question_change_queue", "TEXT"));
    ddl.extend(change_trigger_ddl(
        postgres,
        "question",
        "question_change_queue",
        &QUESTION_IMAGE,
    ));
    ddl
}

/// Task (D104), the end-to-end fixture's table as behaviorlayout writes it,
/// its subjects strings: the own `title`, `owner`, `reviewer`, `plan`,
/// `proof`, a boolean `urgent`, a number `attempts` and a JSON `data`;
/// `createdBy` among its audit fields, which an Approvals gate that excludes
/// the creator reads; Constants, StateRules and Approvals, which it
/// implements, before its status, a Workflow. Approvals keeps its sign-offs
/// in the sub-table `task_approvals_sign_offs`, and nothing arms a deadline
/// or hears a change, so the table has neither queue.
pub fn task_table() -> Table {
    let flow = json!({
        "states": ["planned", "working", "proving", "done", "cancelled"],
        "transitions": [
            {"from": "planned", "to": "working", "by": ["owner"]},
            {"from": "working", "to": "proving", "by": ["owner"]},
            {"from": "proving", "to": "working"},
            {"from": "proving", "to": "done"},
            {"from": "planned", "to": "cancelled"},
            {"from": "working", "to": "cancelled"}
        ]
    });
    let at = |name: &str| OwnedField {
        name: name.into(),
        column: Column::new(name, snake(name), "Temporal.DateTime").converted(Convert::EpochMillis),
        value: false,
    };
    let text = |name: &str| owned(name, name, name, "string", false);
    let implemented = |behavior: &str| Use {
        id: behavior.into(),
        behavior: behavior.into(),
        member: behavior.to_ascii_lowercase(),
        anchor: None,
        fields: Vec::new(),
        operations: Vec::new(),
        tables: Vec::new(),
    };
    let approvals = Use {
        operations: [
            ("signOff", true),
            ("withdrawSignOff", true),
            ("listSignOffs", false),
        ]
        .into_iter()
        .map(|(name, writes)| Operation::new(name, writes))
        .collect(),
        tables: vec![SubTable {
            name: "signOffs".into(),
            table: "task_approvals_sign_offs".into(),
            row: "task_id".into(),
            key: text("number"),
            fields: vec![
                text("state"),
                text("subject"),
                text("note"),
                at("at"),
                at("consumedAt"),
            ],
        }],
        ..implemented("Approvals")
    };
    let own = [
        ("title", "string"),
        ("owner", "string"),
        ("reviewer", "string"),
        ("plan", "string"),
        ("proof", "string"),
        ("urgent", "boolean"),
        ("attempts", "number"),
        ("data", "Generic.JSON"),
    ];
    let mut spec = TypeSpec::new("Task")
        .with(UseSpec::on_type(
            "Constants",
            json!({
                "fields": ["owner"],
                "oneWay": {"urgent": "up", "attempts": "up", "due": "up"},
                "permission": "tasks.admin"
            }),
        ))
        .with(UseSpec::on_type(
            "StateRules",
            json!({"states": {
                "planned": {"frozen": ["plan"], "by": ["createdBy"]},
                "working": {"frozen": ["plan"], "by": ["reviewer"]},
                "proving": {"required": ["proof"]}
            }}),
        ))
        .with(UseSpec::on_type(
            "Approvals",
            json!({"gates": {
                "proving": {"count": 1, "permission": "tasks.review", "resetOn": []},
                "done": {"count": 1, "permission": "tasks.review", "resetOn": ["proof"], "pinned": true}
            }}),
        ))
        .with(UseSpec::on_field("status", "Workflow", flow));
    for (name, type_name) in own.iter().chain(&[("createdBy", "string")]) {
        spec = spec.with_field(FieldSpec::new(*name, *type_name));
    }
    spec = spec.with_field(FieldSpec::date_time("due", "Temporal.DateTime"));
    let mut status = order_table("Task", "task");
    let workflow = status.uses.remove(0);
    Table {
        type_name: "Task".into(),
        table: "task".into(),
        key: Column::new("id", "id", "string"),
        version: "_version".into(),
        live: None,
        own: own
            .iter()
            .map(|(name, type_name)| Column::new(*name, *name, *type_name))
            .chain([Column::new("due", "due", "Temporal.DateTime").converted(Convert::EpochMillis)])
            .collect(),
        audit: vec![Column::new("createdBy", "created_by", "string")],
        uses: vec![
            implemented("Constants"),
            implemented("StateRules"),
            approvals,
            workflow,
        ],
        deadlines: None,
        changes: None,
        events: None,
        spec,
    }
}

/// [`task_table`]'s tables in a dialect as sqlgen writes them: the row's,
/// with its version trigger, and its sub-table's, a surrogate `id` beside
/// the row's key and the sign-off's number, unique together, its rows
/// deleted with the row's.
pub fn task_ddl(postgres: bool) -> Vec<String> {
    let t = column_types(postgres);
    let mut ddl = vec![format!(
        "CREATE TABLE \"task\" (id {}, title {text} NOT NULL, owner {text}, reviewer {text}, plan {text}, proof {text}, urgent {}, attempts {}, data {}, due {}, created_by {text}, status {text}, status_entered_at {}, _version {})",
        t.key,
        t.boolean,
        t.number,
        t.json,
        t.timestamp,
        t.timestamp,
        t.version,
        text = t.text,
    )];
    if postgres {
        ddl.extend(postgres_bump("task"));
    } else {
        ddl.push(sqlite_bump("task"));
    }
    let id = if postgres {
        "UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY"
    } else {
        "TEXT DEFAULT (lower(hex(randomblob(16)))) NOT NULL PRIMARY KEY"
    };
    ddl.push(format!(
        "CREATE TABLE \"task_approvals_sign_offs\" (id {id}, task_id TEXT NOT NULL REFERENCES \"task\" (id) ON DELETE CASCADE, number TEXT NOT NULL, state TEXT, subject TEXT, note TEXT, at {ts}, consumed_at {ts}, UNIQUE (task_id, number))",
        ts = t.timestamp,
    ));
    ddl
}

/// Job (D104), the end-to-end fixture's leased table as behaviorlayout
/// writes it, its key a string: the own `title` and a whole-number
/// `revision`; Lease, Retries and the toy [`Finder`], which it implements,
/// before its status, a Workflow. Lease's owned columns hold its holder,
/// its token and its expiries as whole numbers, its times as timestamps and
/// how the last lease ended as JSON, and its directives a sub-table; Retries'
/// hold its counts as whole numbers and JSON, its best score as a number,
/// its flags as booleans and its waits as timestamps, and its attempts a
/// sub-table; and both arm deadlines in `job_deadline_queue`. A write under
/// a lease presents its token (`requireToken`), and a timeout waits a
/// minute before the job runs again, its counts keyed by the revision.
pub fn job_table() -> Table {
    let flow = json!({
        "states": ["queued", "running", "failed"],
        "transitions": [
            {"from": "queued", "to": "running"},
            {"from": "running", "to": "queued"},
            {"from": "queued", "to": "failed"},
            {"from": "running", "to": "failed"}
        ]
    });
    let at = |name: &str, column: &str| OwnedField {
        name: name.into(),
        column: Column::new(camel(column), column, "Temporal.DateTime")
            .converted(Convert::EpochMillis),
        value: false,
    };
    let field = |name: &str, column: &str, type_name: &str| {
        owned(name, &camel(column), column, type_name, false)
    };
    let sub = |name: &str, type_name: &str| owned(name, name, &snake(name), type_name, false);
    let sub_at = |name: &str| OwnedField {
        name: name.into(),
        column: Column::new(name, snake(name), "Temporal.DateTime").converted(Convert::EpochMillis),
        value: false,
    };
    let operations = |names: &[&str]| -> Vec<Operation> {
        names
            .iter()
            .map(|name| Operation::new(*name, true))
            .collect()
    };
    let lease = Use {
        id: "Lease".into(),
        behavior: "Lease".into(),
        member: "lease".into(),
        anchor: None,
        fields: vec![
            field("holder", "lease_holder", "string"),
            field("token", "lease_token", "Generic.Int64"),
            at("acquiredAt", "lease_acquired_at"),
            at("renewedAt", "lease_renewed_at"),
            at("expiresAt", "lease_expires_at"),
            field("expiries", "lease_expiries", "Generic.Int64"),
            field("ended", "lease_ended", "Generic.JSON"),
        ],
        // Its operations of a row, then expireHolder, of the table as a
        // whole (D104).
        operations: operations(&[
            "acquire",
            "heartbeat",
            "release",
            "expire",
            "direct",
            "acknowledge",
            "resetExpiries",
        ])
        .into_iter()
        .chain([Operation::of_table("expireHolder", true)])
        .collect(),
        tables: vec![SubTable {
            name: "directives".into(),
            table: "job_lease_directives".into(),
            row: "job_id".into(),
            key: sub("number", "string"),
            fields: vec![
                sub("token", "Generic.Int64"),
                sub("name", "string"),
                sub("data", "Generic.JSON"),
                sub("dedupeKey", "string"),
                sub("sentBy", "string"),
                sub_at("sentAt"),
                sub_at("acknowledgedAt"),
            ],
        }],
    };
    let retries = Use {
        id: "Retries".into(),
        behavior: "Retries".into(),
        member: "retries".into(),
        anchor: None,
        fields: vec![
            field("total", "retries_total", "Generic.Int64"),
            field("classAttempts", "retries_class_attempts", "Generic.JSON"),
            field("bestScore", "retries_best_score", "number"),
            field("exhausted", "retries_exhausted", "boolean"),
            field("stuck", "retries_stuck", "boolean"),
            at("retryAt", "retries_retry_at"),
            at("failingSince", "retries_failing_since"),
            field("keyedBy", "retries_keyed_by", "string"),
        ],
        operations: operations(&["recordAttempt"]),
        tables: vec![SubTable {
            name: "attempts".into(),
            table: "job_retries_attempts".into(),
            row: "job_id".into(),
            key: sub("number", "string"),
            fields: vec![
                sub_at("at"),
                sub("failure", "string"),
                sub("score", "number"),
                sub("kept", "boolean"),
                sub("signature", "string"),
                sub("predicates", "Generic.JSON"),
                sub("detail", "Generic.JSON"),
                sub("subject", "string"),
                sub("keyedBy", "string"),
            ],
        }],
    };
    let finder = Use {
        id: "Finder".into(),
        behavior: "Finder".into(),
        member: "finder".into(),
        anchor: None,
        fields: Vec::new(),
        operations: vec![
            Operation::new("find", false),
            Operation::of_table("rows", false),
        ],
        tables: Vec::new(),
    };
    let mut status = order_table("Job", "job");
    let workflow = status.uses.remove(0);
    let spec = TypeSpec::new("Job")
        .with(UseSpec::on_type(
            "Lease",
            json!({
                "requireToken": true,
                "overridePermission": "jobs.override",
                "onExpiry": {"transition": "queued", "from": ["running"]}
            }),
        ))
        .with(UseSpec::on_type(
            "Retries",
            json!({
                "classes": {
                    "timeout": {"attempts": 3, "backoff": {"firstMs": 60000, "maxMs": 600000}},
                    "rejected": "terminal"
                },
                "totalAttempts": 3,
                "exhaustedState": "failed",
                "retryInto": ["running"],
                "keyField": "revision"
            }),
        ))
        .with(UseSpec::on_type("Finder", json!({})))
        .with(UseSpec::on_field("status", "Workflow", flow))
        .with_field(FieldSpec::new("title", "string"))
        .with_field(FieldSpec::new("revision", "Generic.Int64"));
    Table {
        type_name: "Job".into(),
        table: "job".into(),
        key: Column::new("id", "id", "string"),
        version: "_version".into(),
        live: None,
        own: vec![
            Column::new("title", "title", "string"),
            Column::new("revision", "revision", "Generic.Int64"),
        ],
        audit: Vec::new(),
        uses: vec![lease, retries, finder, workflow],
        deadlines: Some(deadlines_of("Job", "job")),
        changes: None,
        events: None,
        spec,
    }
}

/// [`job_table`]'s tables in a dialect as sqlgen writes them: the row's,
/// with its version trigger, its whole numbers `BIGINT` on Postgres and
/// `INTEGER` on SQLite; each sub-table's, a surrogate `id` beside the row's
/// key and the row's number, unique together, its rows deleted with the
/// row's; and its deadline queue.
pub fn job_ddl(postgres: bool) -> Vec<String> {
    let t = column_types(postgres);
    let mut ddl = vec![format!(
        "CREATE TABLE \"job\" (id {key}, title {text} NOT NULL, revision {int}, status {text}, status_entered_at {ts}, lease_holder {text}, lease_token {int}, lease_acquired_at {ts}, lease_renewed_at {ts}, lease_expires_at {ts}, lease_expiries {int}, lease_ended {json}, retries_total {int}, retries_class_attempts {json}, retries_best_score {number}, retries_exhausted {boolean}, retries_stuck {boolean}, retries_retry_at {ts}, retries_failing_since {ts}, retries_keyed_by {text}, _version {version})",
        key = t.key,
        text = t.text,
        int = t.integer,
        ts = t.timestamp,
        json = t.json,
        number = t.number,
        boolean = t.boolean,
        version = t.version,
    )];
    if postgres {
        ddl.extend(postgres_bump("job"));
    } else {
        ddl.push(sqlite_bump("job"));
    }
    let id = if postgres {
        "UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY"
    } else {
        "TEXT DEFAULT (lower(hex(randomblob(16)))) NOT NULL PRIMARY KEY"
    };
    ddl.push(format!(
        "CREATE TABLE \"job_lease_directives\" (id {id}, job_id TEXT NOT NULL REFERENCES \"job\" (id) ON DELETE CASCADE, number TEXT NOT NULL, token {int}, name TEXT, data {json}, dedupe_key TEXT, sent_by TEXT, sent_at {ts}, acknowledged_at {ts}, UNIQUE (job_id, number))",
        int = t.integer,
        json = t.json,
        ts = t.timestamp,
    ));
    ddl.push(format!(
        "CREATE TABLE \"job_retries_attempts\" (id {id}, job_id TEXT NOT NULL REFERENCES \"job\" (id) ON DELETE CASCADE, number TEXT NOT NULL, at {ts}, failure TEXT, score {number}, kept {boolean}, signature TEXT, predicates {json}, detail {json}, subject TEXT, keyed_by TEXT, UNIQUE (job_id, number))",
        ts = t.timestamp,
        number = t.number,
        boolean = t.boolean,
        json = t.json,
    ));
    ddl.extend(deadline_queue_ddl(postgres, "job_deadline_queue", "TEXT"));
    ddl
}

/// A deadline queue's layout on a table (D103): `<table>_deadline_queue`,
/// its columns as behaviorlayout names them.
pub fn deadlines_of(type_name: &str, table: &str) -> Deadlines {
    Deadlines {
        queue: format!("{type_name}Deadline"),
        table: format!("{table}_deadline_queue"),
        row_key: "row_key".into(),
        use_id: "use_id".into(),
        name: "name".into(),
        at: "at".into(),
        payload: "payload".into(),
    }
}

/// Timers implemented on a table (D103), as behaviorlayout writes the use:
/// no owned column, its three operations, and its sub-table
/// `<table>_timers_deadlines` keyed by the row's `<type>_id` and the
/// deadline's name.
pub fn timers_use(table: &str, row: &str) -> Use {
    let at = |name: &str| OwnedField {
        name: name.into(),
        column: Column::new(name, snake(name), "Temporal.DateTime").converted(Convert::EpochMillis),
        value: false,
    };
    let plain = |name: &str, type_name: &str| owned(name, name, &snake(name), type_name, false);
    Use {
        id: "Timers".into(),
        behavior: "Timers".into(),
        member: "timers".into(),
        anchor: None,
        fields: Vec::new(),
        operations: ["fireTimer", "resetTimer", "snoozeTimer"]
            .into_iter()
            .map(|name| Operation::new(name, true))
            .collect(),
        tables: vec![SubTable {
            name: "deadlines".into(),
            table: format!("{table}_timers_deadlines"),
            row: row.into(),
            key: plain("name", "string"),
            fields: vec![
                at("baseAt"),
                plain("armedFrom", "string"),
                at("dueAt"),
                plain("fired", "boolean"),
                at("firedAt"),
                plain("outcome", "string"),
            ],
        }],
    }
}

/// Timers' sub-table `<table>_timers_deadlines` in a dialect, its rows
/// deleted with the row's.
pub fn timers_deadlines_ddl(postgres: bool, table: &str, row: &str) -> String {
    let t = column_types(postgres);
    format!(
        "CREATE TABLE \"{table}_timers_deadlines\" ({row} TEXT NOT NULL REFERENCES \"{table}\" (id) ON DELETE CASCADE, name TEXT NOT NULL, base_at {ts}, armed_from TEXT, due_at {ts}, fired {boolean}, fired_at {ts}, outcome TEXT, PRIMARY KEY ({row}, name))",
        ts = t.timestamp,
        boolean = t.boolean,
    )
}

/// A room key no other layout's toy locks: `Room/r1/<pid>/<nanos>/<n>`. An
/// advisory lock is the whole database's, not a schema's, so a test that
/// checks a lock is free after its transaction would fail while another
/// test, in this process or another on the same database, held a key of
/// one name.
pub fn room() -> String {
    static ROOMS: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);
    let nanos = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|since| since.as_nanos())
        .unwrap_or(0);
    format!(
        "Room/r1/{}/{nanos}/{}",
        std::process::id(),
        ROOMS.fetch_add(1, std::sync::atomic::Ordering::SeqCst)
    )
}

/// Order, Ticket (soft-deletable, its live rows NULL in `deleted_at`), Item
/// (Order's shape with a UUID key) and Booking (no behavior of Workflow's,
/// the toy's rule across rows on its create), its room a [`room`] of its
/// own.
pub fn store_layout() -> Layout {
    store_layout_in(&room())
}

/// [`store_layout`] with Booking's create locking `room`.
pub fn store_layout_in(room: &str) -> Layout {
    let ticket = Table {
        live: Some(superschematic_rules_adapter::Live {
            column: "deleted_at".into(),
        }),
        ..order_table("Ticket", "ticket")
    };
    let item = Table {
        key: Column::new("id", "id", "Identity.UUID"),
        ..order_table("Item", "item")
    };
    let booking = Table {
        type_name: "Booking".into(),
        table: "booking".into(),
        key: Column::new("id", "id", "string"),
        version: "_version".into(),
        live: None,
        own: Vec::new(),
        audit: Vec::new(),
        uses: Vec::new(),
        deadlines: None,
        changes: None,
        events: None,
        spec: TypeSpec::new("Booking").with(UseSpec::on_type("OneRoom", json!({"room": room}))),
    };
    let loud = Table {
        type_name: "Loud".into(),
        table: "loud".into(),
        spec: TypeSpec::new("Loud").with(UseSpec::on_type("OneRoom", json!({"emit": true}))),
        ..booking.clone()
    };
    let timed = Table {
        type_name: "Timed".into(),
        table: "timed".into(),
        deadlines: Some(deadlines_of("Timed", "timed")),
        spec: TypeSpec::new("Timed").with(UseSpec::on_type("OneRoom", json!({"arm": true}))),
        ..booking.clone()
    };
    let held = Table {
        type_name: "Held".into(),
        table: "held".into(),
        uses: vec![
            timers_use("held", "held_id"),
            Use {
                id: "Hold".into(),
                behavior: "Hold".into(),
                member: "hold".into(),
                anchor: None,
                fields: Vec::new(),
                operations: Vec::new(),
                tables: Vec::new(),
            },
        ],
        deadlines: Some(deadlines_of("Held", "held")),
        spec: TypeSpec::new("Held")
            .with(UseSpec::on_type(
                "Timers",
                json!({"deadlines": {"expire": {"start": {"create": true}, "afterMs": 1000}}}),
            ))
            .with(UseSpec::on_type("Hold", json!({}))),
        ..booking.clone()
    };
    // Shout (D104): its behavior emits events, which its table's event
    // queue keeps.
    let shout = Table {
        type_name: "Shout".into(),
        table: "shout".into(),
        uses: vec![Use {
            id: "Shout".into(),
            behavior: "Shout".into(),
            member: "shout".into(),
            anchor: None,
            fields: Vec::new(),
            operations: vec![Operation::new("say", true)],
            tables: Vec::new(),
        }],
        events: Some(events_of("Shout", "shout")),
        spec: TypeSpec::new("Shout").with(UseSpec::on_type("Shout", json!({}))),
        ..booking.clone()
    };
    Layout::new([
        order_table("Order", "order"),
        ticket,
        item,
        booking,
        loud,
        timed,
        held,
        shout,
        probe_table(),
    ])
}

/// The columns of Probe's change trigger's image (D103): its own columns,
/// then Echo's owned column, the key, `_version` and `deleted_at` left out.
pub const PROBE_IMAGE: [&str; 6] = ["title", "due", "owner_id", "done", "data", "echo_heard"];

/// Probe (D103): a soft-deletable table its behaviors hear, its key a UUID
/// in a TEXT column, an own column of each kind a change's image converts
/// (text, a timestamp, a UUID, a boolean and JSON), Echo's mark and Timers'
/// create deadline `expire` a second after a row's create, its deadline
/// queue and its change queue, which its trigger writes.
pub fn probe_table() -> Table {
    Table {
        type_name: "Probe".into(),
        table: "probe".into(),
        key: Column::new("id", "id", "Identity.UUID"),
        version: "_version".into(),
        live: Some(Live {
            column: "deleted_at".into(),
        }),
        own: vec![
            Column::new("title", "title", "string"),
            Column::new("due", "due", "Temporal.DateTime").converted(Convert::EpochMillis),
            Column::new("ownerId", "owner_id", "Identity.UUID"),
            Column::new("done", "done", "boolean"),
            Column::new("data", "data", "Generic.JSON"),
        ],
        audit: Vec::new(),
        uses: vec![
            Use {
                id: "Echo".into(),
                behavior: "Echo".into(),
                member: "echo".into(),
                anchor: None,
                fields: vec![owned("heard", "echoHeard", "echo_heard", "string", false)],
                operations: Vec::new(),
                tables: Vec::new(),
            },
            timers_use("probe", "probe_id"),
        ],
        deadlines: Some(deadlines_of("Probe", "probe")),
        changes: Some(Changes {
            queue: "ProbeChange".into(),
            table: "probe_change_queue".into(),
            row_key: "row_key".into(),
            write: "write".into(),
            at: "at".into(),
            before: "before".into(),
            after: "after".into(),
        }),
        events: None,
        spec: TypeSpec::new("Probe")
            .with(UseSpec::on_type("Echo", json!({})))
            .with(UseSpec::on_type(
                "Timers",
                json!({"deadlines": {"expire": {"start": {"create": true}, "afterMs": 1000}}}),
            )),
    }
}

/// The tables of [`store_layout`] in a dialect, each with its trigger.
pub fn store_ddl(postgres: bool) -> Vec<String> {
    let t = column_types(postgres);
    let mut ddl = Vec::new();
    for (table, extra) in [
        ("order", ""),
        ("ticket", ", deleted_at TIMESTAMPTZ"),
        ("item", ""),
    ] {
        let extra = if postgres {
            extra.to_owned()
        } else {
            extra.replace("TIMESTAMPTZ", t.timestamp)
        };
        ddl.push(format!(
            "CREATE TABLE \"{table}\" (id {}, title {} NOT NULL, status {}, status_entered_at {}{extra}, _version {})",
            t.key, t.text, t.text, t.timestamp, t.version
        ));
    }
    for table in ["booking", "loud", "timed", "held", "shout"] {
        ddl.push(format!(
            "CREATE TABLE \"{table}\" (id {}, _version {})",
            t.key, t.version
        ));
    }
    for table in [
        "order", "ticket", "item", "booking", "loud", "timed", "held", "shout",
    ] {
        if postgres {
            ddl.extend(postgres_bump(table));
        } else {
            ddl.push(sqlite_bump(table));
        }
    }
    ddl.extend(deadline_queue_ddl(postgres, "timed_deadline_queue", "TEXT"));
    ddl.push(timers_deadlines_ddl(postgres, "held", "held_id"));
    ddl.extend(deadline_queue_ddl(postgres, "held_deadline_queue", "TEXT"));
    ddl.extend(event_queue_ddl(postgres, "shout_event_queue", "TEXT"));
    let uuid = if postgres { "UUID" } else { "TEXT" };
    ddl.push(format!(
        "CREATE TABLE \"probe\" (id {}, title {} NOT NULL, due {ts}, owner_id {uuid}, done {}, data {}, echo_heard {}, deleted_at {ts}, _version {})",
        t.key,
        t.text,
        t.boolean,
        t.json,
        t.text,
        t.version,
        ts = t.timestamp,
    ));
    if postgres {
        ddl.extend(postgres_bump("probe"));
    } else {
        ddl.push(sqlite_bump("probe"));
    }
    ddl.push(timers_deadlines_ddl(postgres, "probe", "probe_id"));
    ddl.extend(deadline_queue_ddl(postgres, "probe_deadline_queue", "TEXT"));
    ddl.extend(change_queue_ddl(postgres, "probe_change_queue", "TEXT"));
    ddl.extend(change_trigger_ddl(
        postgres,
        "probe",
        "probe_change_queue",
        &PROBE_IMAGE,
    ));
    ddl
}

/// A behavior of the tests' own whose create is a rule across rows, as two
/// bookings of one room are: it takes the lock its config's `room` names
/// (`Room/r1` when it names none), reads under it whether its type has
/// rows, and lets its row be created only when there is none, refusing
/// `conflict` otherwise. Only the row it creates is written, so only the
/// lock holds two such commands apart. A config of `{"emit": true}` emits
/// its event `created` on a create instead (D104), and
/// one of `{"arm": true}` arms the deadline `expire` a second after the
/// create, which, when it falls due with the payload `{"again": true}`,
/// arms itself again a second later.
pub struct OneRoom;

impl Behavior for OneRoom {
    type Config = Value;

    fn declaration(&self) -> Declaration {
        Declaration::new("OneRoom")
            .arms_deadlines()
            .event("created")
    }

    fn deadline(&self, cx: &Cx<'_, Value>, name: &str, payload: &Value) -> Hook<()> {
        if payload.get("again") == Some(&Value::Bool(true)) {
            cx.arm(name, cx.now() + 1000, Value::Null)?;
        }
        Ok(())
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value, ConfigError> {
        Ok(raw.clone())
    }

    fn initialize(&self, cx: &Cx<'_, Value>, _params: &Value) -> Hook<()> {
        if cx.config().get("emit") == Some(&Value::Bool(true)) {
            return cx.emit("created", json!({}));
        }
        if cx.config().get("arm") == Some(&Value::Bool(true)) {
            return cx.arm("expire", cx.now() + 1000, Value::Null);
        }
        let room = cx
            .config()
            .get("room")
            .and_then(Value::as_str)
            .unwrap_or("Room/r1");
        cx.lock(room)?;
        if cx.has_rows(cx.type_name())? {
            return Err(
                Refusal::new(superschematic_rules::Code::Conflict, "room r1 is booked").into(),
            );
        }
        Ok(())
    }
}

/// A behavior of the tests' own whose guard holds every fire of Timers on
/// its row (D103): it vetoes `fireTimer` with its declared code `held`, as
/// a lease another principal holds would, which no behavior the core
/// registers does of a deadline's fire.
pub struct Hold;

impl Behavior for Hold {
    type Config = Value;

    fn declaration(&self) -> Declaration {
        Declaration::new("Hold").veto("held")
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value, ConfigError> {
        Ok(raw.clone())
    }

    fn guard(&self, _view: &View<'_, Value>, request: &GuardRequest<'_>) -> Hook<Option<Veto>> {
        Ok(request
            .operation_of("Timers", "fireTimer")
            .is_some()
            .then(|| Veto::new("the fire is held").with_code("held")))
    }
}

/// A behavior of the tests' own that hears its row's writes (D103) and
/// marks the row with each it hears, as the core got it: its owned field
/// `heard` holds the change's JSON. An update that moved none of the row's
/// own fields (its own mark's) it passes over, so it hears its own write
/// and writes nothing more.
pub struct Echo;

impl Behavior for Echo {
    type Config = Value;

    fn declaration(&self) -> Declaration {
        Declaration::new("Echo").field("heard").hears_changes()
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value, ConfigError> {
        Ok(raw.clone())
    }

    fn heard(&self, cx: &Cx<'_, Value>, heard: &Heard<'_>) -> Hook<()> {
        let Heard::Own { change } = heard else {
            return Ok(());
        };
        let moved = match (&change.before, &change.after) {
            (Some(before), Some(after)) => before.own != after.own,
            _ => true,
        };
        if moved {
            cx.set("heard", serde_json::to_string(change).unwrap_or_default())?;
        }
        Ok(())
    }
}

/// A behavior of the tests' own that reads the rows of a type whose use's
/// owned field holds a value (D104), as Presence's releaseLeases reads the
/// rows whose Lease's holder is a principal: its read-only operation `find`
/// answers the page of `{type, use, field, value, after}`. Its read-only
/// operation of the table as a whole, `rows` (D104), answers the page of a
/// read across a table, `{type, equal, tables, after}`, as Pool's pick
/// reads its members.
pub struct Finder;

impl Behavior for Finder {
    type Config = Value;

    fn declaration(&self) -> Declaration {
        Declaration::new("Finder")
            .operation("find", false)
            .table_operation("rows", false)
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value, ConfigError> {
        Ok(raw.clone())
    }

    fn operation(&self, cx: &Cx<'_, Value>, name: &str, params: &Value) -> Hook<Value> {
        let param = |name: &str| params.get(name).and_then(Value::as_str);
        if name == "rows" {
            let equal = params
                .get("equal")
                .and_then(Value::as_object)
                .cloned()
                .unwrap_or_default();
            let tables: Vec<TableRef> = params
                .get("tables")
                .and_then(|tables| serde_json::from_value(tables.clone()).ok())
                .unwrap_or_default();
            let page = cx.read_rows(
                param("type").unwrap_or_default(),
                &equal,
                &tables,
                param("after"),
            )?;
            return Ok(serde_json::to_value(page).unwrap_or_default());
        }
        let page = cx.read_holding(
            param("type").unwrap_or_default(),
            param("use").unwrap_or_default(),
            param("field").unwrap_or_default(),
            params.get("value").unwrap_or(&Value::Null),
            param("after"),
        )?;
        Ok(serde_json::to_value(page).unwrap_or_default())
    }
}

/// A behavior of the tests' own that emits events about its row (D104):
/// its create emits `said` with `{"word": "hello"}`, and its operation
/// `say` emits `said` once per word of params' `words`, in their order.
pub struct Shout;

impl Behavior for Shout {
    type Config = Value;

    fn declaration(&self) -> Declaration {
        Declaration::new("Shout")
            .operation("say", true)
            .event("said")
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value, ConfigError> {
        Ok(raw.clone())
    }

    fn initialize(&self, cx: &Cx<'_, Value>, _params: &Value) -> Hook<()> {
        cx.emit("said", json!({"word": "hello"}))
    }

    fn operation(&self, cx: &Cx<'_, Value>, _name: &str, params: &Value) -> Hook<Value> {
        let words = params.get("words").and_then(Value::as_array);
        for word in words.into_iter().flatten() {
            cx.emit("said", json!({ "word": word }))?;
        }
        Ok(Value::Null)
    }
}

/// The core's behaviors and the toys.
pub fn core() -> superschematic_rules::Core {
    superschematic_rules_behaviors::core()
        .with(OneRoom)
        .with(Hold)
        .with(Echo)
        .with(Finder)
        .with(Shout)
}

#[cfg(feature = "sqlite")]
pub fn sqlite_params(values: &[Param]) -> Vec<rusqlite::types::Value> {
    values
        .iter()
        .map(|value| match value {
            Param::Null => rusqlite::types::Value::Null,
            Param::Text(text) => rusqlite::types::Value::Text(text.clone()),
            Param::Int(n) => rusqlite::types::Value::Integer(*n),
        })
        .collect()
}

/// A SQLite file of a test's own under the temporary directory, removed
/// with its WAL files when dropped.
#[cfg(feature = "sqlite")]
pub struct SqliteFile(std::path::PathBuf);

#[cfg(feature = "sqlite")]
impl SqliteFile {
    pub fn new() -> Self {
        static FILES: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);
        let nanos = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|since| since.as_nanos())
            .unwrap_or(0);
        let n = FILES.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        SqliteFile(
            std::env::temp_dir().join(format!("rules_rs_{}_{nanos}_{n}.db", std::process::id())),
        )
    }

    /// A connection to the file that waits up to ten seconds for a lock.
    pub fn open(&self) -> rusqlite::Connection {
        let conn = rusqlite::Connection::open(&self.0).expect("SQLite opens");
        conn.busy_timeout(std::time::Duration::from_secs(10))
            .expect("a busy timeout");
        conn
    }
}

#[cfg(feature = "sqlite")]
impl Drop for SqliteFile {
    fn drop(&mut self) {
        for suffix in ["", "-wal", "-shm"] {
            let _ = std::fs::remove_file(format!("{}{suffix}", self.0.display()));
        }
    }
}

#[cfg(feature = "postgres")]
pub mod pg {
    //! A Postgres schema of a test's own, and parameters sent as text.

    use std::future::Future;
    use std::sync::atomic::{AtomicUsize, Ordering};
    use std::time::{SystemTime, UNIX_EPOCH};

    use bytes::BytesMut;
    use tokio_postgres::types::{to_sql_checked, Format, IsNull, ToSql, Type};
    use tokio_postgres::{Client, NoTls};

    use super::Param;

    /// The database the variable names, or `None`, with the line that says
    /// so, when it is unset.
    pub fn dsn(skipping: &str) -> Option<String> {
        match std::env::var(super::RULES_DATABASE_VARIABLE) {
            Ok(url) if !url.is_empty() => Some(url),
            _ => {
                println!("{skipping}");
                None
            }
        }
    }

    /// A connection, its search path the schema's when one is named.
    pub async fn connect(url: &str, schema: Option<&str>) -> Client {
        let (client, connection) = tokio_postgres::connect(url, NoTls)
            .await
            .expect("Postgres connects");
        tokio::spawn(async move {
            let _ = connection.await;
        });
        if let Some(schema) = schema {
            client
                .batch_execute(&format!("SET search_path TO \"{schema}\""))
                .await
                .expect("the search path is set");
        }
        client
    }

    /// A schema of a test's own on the database.
    #[derive(Clone, Debug)]
    pub struct Schema {
        pub url: String,
        pub name: String,
    }

    impl Schema {
        /// A connection whose search path is the schema.
        pub async fn client(&self) -> Client {
            connect(&self.url, Some(&self.name)).await
        }
    }

    static SCHEMAS: AtomicUsize = AtomicUsize::new(0);

    /// Runs body on a schema of its own, `rules_rs_<pid>_<nanos>_<n>`,
    /// created first and dropped after, whether body passes or panics.
    pub async fn on_schema<F, Fut>(url: String, body: F)
    where
        F: FnOnce(Schema) -> Fut,
        Fut: Future<Output = ()> + Send + 'static,
    {
        let nanos = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map(|since| since.as_nanos())
            .unwrap_or(0);
        let name = format!(
            "rules_rs_{}_{nanos}_{}",
            std::process::id(),
            SCHEMAS.fetch_add(1, Ordering::SeqCst)
        );
        let admin = connect(&url, None).await;
        admin
            .batch_execute(&format!("CREATE SCHEMA \"{name}\""))
            .await
            .expect("the schema is created");
        let outcome = tokio::spawn(body(Schema {
            url,
            name: name.clone(),
        }))
        .await;
        admin
            .batch_execute(&format!("DROP SCHEMA \"{name}\" CASCADE"))
            .await
            .expect("the schema is dropped");
        if let Err(error) = outcome {
            if error.is_panic() {
                std::panic::resume_unwind(error.into_panic());
            }
            panic!("the test's task did not finish: {error}");
        }
    }

    /// A parameter sent as text, which Postgres reads into the type it
    /// meets, or NULL.
    #[derive(Debug)]
    pub struct Text(Option<String>);

    impl ToSql for Text {
        fn to_sql(
            &self,
            _ty: &Type,
            out: &mut BytesMut,
        ) -> Result<IsNull, Box<dyn std::error::Error + Sync + Send>> {
            match &self.0 {
                Some(text) => {
                    out.extend_from_slice(text.as_bytes());
                    Ok(IsNull::No)
                }
                None => Ok(IsNull::Yes),
            }
        }

        fn accepts(_ty: &Type) -> bool {
            true
        }

        fn encode_format(&self, _ty: &Type) -> Format {
            Format::Text
        }

        to_sql_checked!();
    }

    pub fn texts(values: &[Param]) -> Vec<Text> {
        values
            .iter()
            .map(|value| {
                Text(match value {
                    Param::Null => None,
                    Param::Text(text) => Some(text.clone()),
                    Param::Int(n) => Some(n.to_string()),
                })
            })
            .collect()
    }

    pub fn refs(values: &[Text]) -> Vec<&(dyn ToSql + Sync)> {
        values
            .iter()
            .map(|value| value as &(dyn ToSql + Sync))
            .collect()
    }
}
