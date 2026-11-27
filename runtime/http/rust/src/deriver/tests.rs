//! The loop over tables in memory, on tokio's paused clock: each test's
//! clock starts at zero and jumps to the next timer whenever every task
//! waits, so a wait of seconds takes none. A table's fields are stale
//! until a settle writes a value or counts an attempt, as Derived's core
//! would; a settle of a hash the table did not hand out is skipped.

use super::*;
use std::sync::Mutex;

/// A table in memory and what it was asked.
#[derive(Default)]
struct Table {
    items: Vec<DerivationItem>,
    values: HashMap<String, Value>,
    failures: HashMap<String, Vec<Value>>,
    pulls: Vec<Value>,
    settles: Vec<Vec<Value>>,
    /// Errors the next calls return, one each.
    errors: Vec<orm::Error>,
}

/// The tables of a test, by name.
type Tables = Arc<Mutex<HashMap<String, Table>>>;

fn item(id: &str) -> DerivationItem {
    DerivationItem {
        id: id.to_owned(),
        field: "category".to_owned(),
        source_hash: format!("h-{id}"),
        attempt: 1,
        instruction: "Classify it.".to_owned(),
        model: "standard".to_owned(),
        inputs: json!({ "subject": id })
            .as_object()
            .cloned()
            .unwrap_or_default(),
        output: json!({ "type": "string" }),
    }
}

fn tables(of: &[(&str, &[&str])]) -> Tables {
    let mut out = HashMap::new();
    for (name, ids) in of {
        let table = Table {
            items: ids.iter().map(|id| item(id)).collect(),
            ..Table::default()
        };
        out.insert((*name).to_owned(), table);
    }
    Arc::new(Mutex::new(out))
}

/// The table's derived operations, as the generated `main.rs` runs them.
fn call(
    tables: &Tables,
    table: &str,
    operation: &str,
    params: &Value,
) -> Result<Value, orm::Error> {
    let mut tables = tables.lock().unwrap();
    let t = tables.get_mut(table).expect("a table of the test");
    if !t.errors.is_empty() {
        return Err(t.errors.remove(0));
    }
    match operation {
        "staleDerivations" => {
            t.pulls.push(params.clone());
            let open: Vec<&DerivationItem> = t
                .items
                .iter()
                .filter(|item| {
                    !t.values.contains_key(&item.id)
                        && t.failures.get(&item.id).map_or(0, Vec::len) < 2
                })
                .collect();
            let start = params["cursor"].as_str().map_or(0, |c| c.parse().unwrap());
            let limit = usize::try_from(params["limit"].as_u64().unwrap()).unwrap();
            let end = (start + limit).min(open.len());
            let items: Vec<Value> = open[start..end]
                .iter()
                .map(|item| {
                    json!({
                        "id": item.id, "field": item.field, "sourceHash": item.source_hash,
                        "attempt": t.failures.get(&item.id).map_or(0, Vec::len) + 1,
                        "instruction": item.instruction, "model": item.model,
                        "inputs": item.inputs, "output": item.output,
                    })
                })
                .collect();
            let next = (end < open.len()).then(|| end.to_string());
            Ok(json!({ "items": items, "next": next }))
        }
        "settleDerivations" => {
            let items = params["items"].as_array().unwrap().clone();
            t.settles.push(items.clone());
            let (mut settled, mut failed, mut skipped) = (0, 0, Vec::new());
            for s in items {
                let id = s["id"].as_str().unwrap().to_owned();
                if s["sourceHash"] != json!(format!("h-{id}")) {
                    skipped.push(json!({ "id": id, "field": s["field"], "reason": "moved" }));
                } else if let Some(failure) = s.get("failure") {
                    t.failures.entry(id).or_default().push(failure.clone());
                    failed += 1;
                } else {
                    t.values.insert(id, s["value"].clone());
                    settled += 1;
                }
            }
            Ok(json!({ "settled": settled, "failed": failed, "skipped": skipped }))
        }
        other => panic!("no operation {other}"),
    }
}

/// A `call` over `tables` for `run_deriver`.
fn caller(
    tables: &Tables,
) -> impl Fn(String, &'static str, Value) -> std::future::Ready<Result<Value, orm::Error>>
       + Send
       + Sync
       + 'static {
    let tables = Arc::clone(tables);
    move |table, operation, params| std::future::ready(call(&tables, &table, operation, &params))
}

fn settled(tables: &Tables, table: &str) -> usize {
    tables.lock().unwrap()[table].values.len()
}

/// Runs the deriver until `done`, then stops it, and returns what it
/// returned.
async fn run_until<A, AF>(
    tables: &Tables,
    names: &[&str],
    answer: A,
    options: DeriverOptions,
    done: impl Fn() -> bool,
) -> Result<(), DeriverFailure>
where
    A: Fn(String, DerivationItem) -> AF + Send + Sync + 'static,
    AF: Future<Output = DerivationAnswer> + Send + 'static,
{
    let (stop, stopped) = tokio::sync::oneshot::channel::<()>();
    let names = names.iter().map(|name| (*name).to_owned()).collect();
    let running = tokio::spawn(run_deriver(names, caller(tables), answer, options, async {
        let _ = stopped.await;
    }));
    for _ in 0..10_000 {
        if done() || running.is_finished() {
            break;
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    let _ = stop.send(());
    running.await.expect("the deriver did not panic")
}

fn quick() -> DeriverOptions {
    DeriverOptions {
        idle: Duration::from_millis(50),
        ..DeriverOptions::default()
    }
}

#[tokio::test(start_paused = true)]
async fn pulls_each_table_answers_and_settles_with_the_hash_it_pulled() {
    let t = tables(&[("Ticket", &["t1", "t2"]), ("Article", &["a1"])]);
    let answer = |table: String, item: DerivationItem| async move {
        DerivationAnswer::value(format!("{table}:{}", item.id))
    };
    let done = || settled(&t, "Ticket") == 2 && settled(&t, "Article") == 1;
    run_until(&t, &["Ticket", "Article"], answer, quick(), done)
        .await
        .unwrap();
    let tables = t.lock().unwrap();
    assert_eq!(tables["Ticket"].values["t2"], json!("Ticket:t2"));
    for s in &tables["Ticket"].settles[0] {
        assert_eq!(
            s["sourceHash"],
            json!(format!("h-{}", s["id"].as_str().unwrap()))
        );
    }
}

#[tokio::test(start_paused = true)]
async fn pages_a_table_by_its_cursor_and_settles_each_page() {
    let t = tables(&[("Ticket", &["1", "2", "3", "4", "5"])]);
    let options = DeriverOptions {
        batch_size: 2,
        ..quick()
    };
    let answer = |_: String, item: DerivationItem| async move { DerivationAnswer::value(item.id) };
    run_until(&t, &["Ticket"], answer, options, || {
        settled(&t, "Ticket") == 5
    })
    .await
    .unwrap();
    let tables = t.lock().unwrap();
    assert_eq!(tables["Ticket"].settles[0].len(), 2);
    assert_eq!(tables["Ticket"].pulls[0]["limit"], json!(2));
}

#[tokio::test(start_paused = true)]
async fn a_panic_a_failure_and_an_answer_of_no_shape_fail_their_own_items() {
    let t = tables(&[("Ticket", &["panic", "failure", "sure", "ok"])]);
    let answer = |_: String, item: DerivationItem| async move {
        match item.id.as_str() {
            "panic" => panic!("boom"),
            "failure" => DerivationAnswer::failure("refused", "x".repeat(3000)),
            "sure" => DerivationAnswer::Value {
                value: json!("x"),
                confidence: Some(1.5),
                usage: None,
            },
            _ => DerivationAnswer::Value {
                value: json!("ok"),
                confidence: Some(0.9),
                usage: Some(json!({ "inputTokens": 12.4, "outputTokens": -1, "other": true })),
            },
        }
    };
    let done = || {
        t.lock().unwrap()["Ticket"]
            .failures
            .get("sure")
            .map_or(0, Vec::len)
            == 2
    };
    run_until(&t, &["Ticket"], answer, quick(), done)
        .await
        .unwrap();
    let tables = t.lock().unwrap();
    let ticket = &tables["Ticket"];
    assert_eq!(ticket.failures["panic"][0]["class"], json!("panicked"));
    assert_eq!(
        ticket.failures["failure"][0]["message"]
            .as_str()
            .unwrap()
            .len(),
        MAX_FAILURE
    );
    assert_eq!(ticket.failures["sure"][0]["class"], json!("invalid_answer"));
    assert_eq!(ticket.values["ok"], json!("ok"));
    let ok = ticket.settles[0]
        .iter()
        .find(|s| s["id"] == json!("ok"))
        .unwrap();
    assert_eq!(ok["usage"], json!({ "inputTokens": 12 }));
    assert_eq!(ok["confidence"], json!(0.9));
}

#[tokio::test(start_paused = true)]
async fn waits_out_what_time_cures_while_the_next_table_goes_on() {
    let t = tables(&[("Ticket", &["t1"]), ("Article", &["a1"])]);
    t.lock().unwrap().get_mut("Ticket").unwrap().errors = vec![
        orm::Error::Database("connection refused".into()),
        orm::Error::Busy("busy".into()),
    ];
    let answer = |_: String, item: DerivationItem| async move { DerivationAnswer::value(item.id) };
    let options = DeriverOptions {
        idle: Duration::from_secs(3600),
        ..DeriverOptions::default()
    };
    let done = || settled(&t, "Ticket") == 1 && settled(&t, "Article") == 1;
    run_until(&t, &["Ticket", "Article"], answer, options, done)
        .await
        .unwrap();
    // The article settled before the ticket's second try, a second on.
    assert_eq!(t.lock().unwrap()["Article"].settles.len(), 1);
}

#[tokio::test(start_paused = true)]
async fn a_refusal_time_does_not_cure_ends_the_loop_with_the_table_named() {
    let t = tables(&[("Ticket", &["t1"])]);
    let refusal = orm::Refusal {
        code: "forbidden".into(),
        status: 403,
        message: "the caller holds no tickets.derive".into(),
        details: Value::Null,
    };
    t.lock().unwrap().get_mut("Ticket").unwrap().errors =
        vec![orm::Error::Refused(Box::new(refusal))];
    let answer = |_: String, item: DerivationItem| async move { DerivationAnswer::value(item.id) };
    let failed = run_until(&t, &["Ticket"], answer, quick(), || false)
        .await
        .unwrap_err();
    assert_eq!(failed.table, "Ticket");
    assert!(failed.to_string().contains("table Ticket"), "{failed}");
}

#[tokio::test(start_paused = true)]
async fn a_pass_that_settles_nothing_waits_before_it_pulls_again() {
    let t = tables(&[("Ticket", &["t1"])]);
    // Every answer settles with a hash that moved, which the core skips.
    let moved = Arc::clone(&t);
    let call = move |table: String, operation: &'static str, mut params: Value| {
        if operation == "settleDerivations" {
            for item in params["items"].as_array_mut().unwrap() {
                item["sourceHash"] = json!("moved");
            }
        }
        std::future::ready(super::tests::call(&moved, &table, operation, &params))
    };
    let answer = |_: String, item: DerivationItem| async move { DerivationAnswer::value(item.id) };
    let options = DeriverOptions {
        idle: Duration::from_secs(60),
        ..DeriverOptions::default()
    };
    let shutdown = tokio::time::sleep(Duration::from_secs(30));
    run_deriver(vec!["Ticket".into()], call, answer, options, shutdown)
        .await
        .unwrap();
    assert_eq!(
        t.lock().unwrap()["Ticket"].pulls.len(),
        1,
        "a pass that settled nothing pulled again before its idle wait"
    );
}
