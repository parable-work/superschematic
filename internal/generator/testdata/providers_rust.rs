//! fixture-providers-api's runtime providers end to end in the Rust server
//! (D159, D185), run by TestRustProvidersEndToEnd: the API crate's
//! providers module over the provider crates of
//! examples/runtime-providers/rust, which its config lists, the Deps that
//! holds them, a channel adapter that records each delivery through
//! fixture-providers-db's repositories on Postgres as its own principal,
//! and the generated router serving the deliveries to a provider that
//! calls the app with its key. The database is a schema of the test's own
//! at PROVIDERS_TEST_DATABASE_URL with the database's create.sql applied.

use std::collections::BTreeMap;
use std::sync::{Arc, Mutex};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use api::runtime::orm::postgres::{deadpool_postgres, tokio_postgres, Repositories};
use api::runtime::orm::{AccessCaller, Values};
use api::runtime::postgres::Pool;
use api::runtime::providers::{
    use_credential, ConfigValue, CredentialQuery, ExtensionAuthenticator, ExtensionKey, FileInput,
    InjectionRunner, InjectionTarget, ModelEmbedRequest, ModelMessage, ModelRole, ModelStop,
    ModelTool, ModelTurn, OutboundMessage, PreviewOptions, Providers, RuntimeInterface,
    SandboxSpec, SandboxState, SecretValue, UseCredential,
};
use api::runtime::{BoxError, PrincipalKind};
use api::{build_router, Deps, Handlers, Implementations};
use async_trait::async_trait;
use axum::body::Body;
use http::Request;
use http_body_util::BodyExt;
use serde_json::{json, Value};
use tower::ServiceExt;

const RELAY_TOKEN: &str = "relay-a-token-value";
const VAULT_TOKEN: &str = "vault-token-value";
const PASSWORD: &str = "correct-horse-battery-staple";

fn case(name: &str) {
    println!("case: {name}");
}

async fn connect(config: &tokio_postgres::Config) -> tokio_postgres::Client {
    let (client, connection) = config.connect(tokio_postgres::NoTls).await.unwrap();
    tokio::spawn(connection);
    client
}

/// A schema of its own holding fixture-providers-db's tables, the pool
/// whose connections search it, and the connection that drops it.
async fn database(url: &str) -> (Pool, tokio_postgres::Client, String) {
    let config: tokio_postgres::Config = url.parse().unwrap();
    let admin = connect(&config).await;
    let nanos = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_nanos();
    let schema = format!("providers_rust_{nanos}");
    admin.batch_execute(&format!("CREATE SCHEMA {schema}")).await.unwrap();
    let mut scoped = config;
    scoped.options(format!("-c search_path={schema},public"));
    let client = connect(&scoped).await;
    client.batch_execute(include_str!("../create.sql")).await.unwrap();
    let manager = deadpool_postgres::Manager::new(scoped, tokio_postgres::NoTls);
    let pool = Pool::builder(manager).max_size(4).build().unwrap();
    (pool, admin, schema)
}

/// The Deps of an environment whose config fields are `config`.
fn deps(base: &Deps, providers: Providers) -> Deps {
    Deps {
        config: base.config.clone(),
        providers: Arc::new(providers),
        db: base.db.clone(),
        span: tracing::Span::none(),
    }
}

/// What an implementation of the API does to send a notice: through the
/// channel adapter the environment's SMS slot names, recording the
/// delivery as the adapter's own principal, whichever adapter it is.
async fn send_notice(deps: &Deps, to: &str, body: &str) -> Result<Values, BoxError> {
    let sms = deps.providers.channel("SMS")?;
    let message = OutboundMessage { to: to.into(), body: body.into(), ..OutboundMessage::default() };
    let sent = sms.send(&message).await?;
    let principal = deps.providers.principal_of("SMS")?;
    let client = deps.db.get().await?;
    let repositories = Repositories::new(&client, types::orm::postgres_definition()?)?
        .with_access(AccessCaller::of(Some(&principal)));
    let row = json!({
        "recipient": to,
        "body": body,
        "provider": deps.providers.provider_of("SMS")?,
        "providerMessageId": sent.provider_message_id,
    });
    Ok(repositories.table("Delivery")?.create_one(row.as_object().unwrap()).await?)
}

fn text(value: &str) -> ConfigValue {
    ConfigValue::Text(value.into())
}

fn secret(value: &str) -> ConfigValue {
    ConfigValue::Secret(SecretValue::new(value))
}

/// The headers a runner set.
#[derive(Default)]
struct Headers(Mutex<Vec<(String, String)>>);

#[async_trait]
impl InjectionRunner for Headers {
    async fn fill(&self, _selector: &str, _value: &str) -> Result<(), BoxError> {
        Err("no browser here".into())
    }

    async fn set_header(&self, name: &str, value: &str) -> Result<(), BoxError> {
        self.0.lock().unwrap().push((name.into(), value.into()));
        Ok(())
    }
}

async fn get(router: &axum::Router, path: &str, key: Option<&str>) -> (u16, Value) {
    let mut request = Request::builder().uri(path);
    if let Some(key) = key {
        request = request.header(http::header::AUTHORIZATION, format!("Bearer {key}"));
    }
    let response = router.clone().oneshot(request.body(Body::empty()).unwrap()).await.unwrap();
    let status = response.status().as_u16();
    let bytes = response.into_body().collect().await.unwrap().to_bytes();
    let body = if bytes.is_empty() { Value::Null } else { serde_json::from_slice(&bytes).unwrap() };
    (status, body)
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn runtime_providers_in_the_rust_server() {
    let url = std::env::var("PROVIDERS_TEST_DATABASE_URL").expect("PROVIDERS_TEST_DATABASE_URL names the database");
    // The environment Dev's config: a provider in every slot, sms-relay-a
    // sending SMS.
    for (name, value) in [
        ("SMS", "sms-relay-a"),
        ("SMS_URL", "http://relay-a.test/"),
        ("SMS_SENDER", "+15550000"),
        ("SMS_TOKEN", RELAY_TOKEN),
        ("VAULT", "vault-broker"),
        ("VAULT_URL", "http://vault.test"),
        ("VAULT_VAULTS", "ops"),
        ("VAULT_TOKEN", VAULT_TOKEN),
        ("RUNNER", "local-sandbox"),
        ("MODEL", "stub-model"),
        ("PREVIEWS", "text-preview"),
        ("PREVIEWS_MAX_CHARS", "12"),
        ("FIXTURE_PROVIDERS_DB_DATABASE_URL", url.as_str()),
    ] {
        std::env::set_var(name, value);
    }
    let (pool, admin, schema) = database(&url).await;
    let config = api::config::load_env_config().unwrap();
    let dev = Deps {
        providers: Arc::new(api::providers::from_config(&config).unwrap()),
        config,
        db: pool.clone(),
        span: tracing::Span::none(),
    };

    case("the provider crates the API lists run in the server with no change to core, each as its own principal");
    {
        let configured: Vec<(String, RuntimeInterface, String)> = dev
            .providers
            .configured(None)
            .into_iter()
            .map(|c| (c.slot, c.interface, c.provider))
            .collect();
        assert_eq!(
            configured,
            [
                ("MODEL".to_owned(), RuntimeInterface::Model, "stub-model".to_owned()),
                ("PREVIEWS".to_owned(), RuntimeInterface::Files, "text-preview".to_owned()),
                ("RUNNER".to_owned(), RuntimeInterface::Sandbox, "local-sandbox".to_owned()),
                ("SMS".to_owned(), RuntimeInterface::Channel, "sms-relay-a".to_owned()),
                ("VAULT".to_owned(), RuntimeInterface::Credentials, "vault-broker".to_owned()),
            ]
        );
        let relay = dev.providers.principal_of("SMS").unwrap();
        assert_eq!((relay.subject.as_str(), relay.kind), ("extension:sms-relay-a", PrincipalKind::Extension));
        assert_eq!(relay.permissions, ["messages.relay"]);
        assert!(dev.providers.principal_of("MODEL").unwrap().permissions.is_empty());
        // The channel adapter sends and reports its health.
        let sms = dev.providers.channel("SMS").unwrap();
        assert_eq!(sms.kind(), "sms");
        assert!(sms.health().await.unwrap().ok);
        // The model completes a turn, calls a tool it is offered, and
        // embeds.
        let model = dev.providers.model("MODEL").unwrap();
        let turn = |content: &str, untrusted: bool| ModelTurn {
            messages: vec![ModelMessage { role: ModelRole::User, content: content.into(), tool_call_id: None, tool_calls: Vec::new(), provider_state: None, untrusted }],
            tools: vec![ModelTool { name: "lookup".into(), description: "Looks a note up.".into(), input_schema: serde_json::Map::new() }],
            ..ModelTurn::default()
        };
        let answered = model.complete(&turn("hello there", false)).await.unwrap();
        assert_eq!((answered.content.as_str(), answered.stop, answered.usage.input_tokens), ("ok: hello there", ModelStop::End, 2));
        let called = model.complete(&turn(r#"call lookup {"id":"n1"}"#, false)).await.unwrap();
        assert_eq!((called.stop, called.tool_calls[0].name.as_str(), &called.tool_calls[0].input), (ModelStop::ToolUse, "lookup", &json!({ "id": "n1" })));
        let ignored = model.complete(&turn(r#"call lookup {"id":"n1"}"#, true)).await.unwrap();
        assert_eq!(ignored.stop, ModelStop::End);
        let embedded = model.embed(&ModelEmbedRequest { model: None, texts: vec!["b a".into(), "a b".into()] }).await.unwrap().unwrap();
        assert_eq!(embedded.vectors[0], embedded.vectors[1]);
        // The file processor extracts text, to its config's bound, and
        // makes a thumbnail of a greyscale image.
        let files = dev.providers.files("PREVIEWS").unwrap();
        let notes = FileInput { media_type: "text/plain".into(), bytes: b"Planning notes, in brief.".to_vec(), name: None };
        let extracted = files.extract_text(&notes).await.unwrap().unwrap();
        assert_eq!((extracted.text.as_str(), extracted.truncated), ("Planning not", true));
        let mut pgm = b"P5\n4 2\n255\n".to_vec();
        pgm.extend_from_slice(&[0, 0, 255, 255, 0, 0, 255, 255]);
        let image = FileInput { media_type: "image/x-portable-graymap".into(), bytes: pgm, name: None };
        let thumb = files.preview(&image, &PreviewOptions { max_width: Some(2), max_height: Some(1) }).await.unwrap().unwrap();
        assert_eq!((thumb.width, thumb.height, thumb.bytes.ends_with(&[0, 255])), (2, 1, true));
        assert!(files.extract_text(&image).await.unwrap().is_none());
        // The sandbox runs an instance as a local process, and its secrets
        // reach it as environment variables.
        let sandbox = dev.providers.sandbox("RUNNER").unwrap();
        let spec = SandboxSpec {
            run_id: "run-1".into(),
            command: vec!["sh".into(), "-c".into(), r#"test "${#RUN_TOKEN}" = 15"#.into()],
            secrets: BTreeMap::from([("RUN_TOKEN".to_owned(), SecretValue::new("run-token-value"))]),
            ..SandboxSpec::default()
        };
        assert!(!serde_json::to_string(&spec).unwrap().contains("run-token-value"));
        let instance = sandbox.start(&spec).await.unwrap();
        let mut state = SandboxState::Running;
        for _ in 0..100 {
            state = sandbox.status(&instance.id).await.unwrap().state;
            if state != SandboxState::Running {
                break;
            }
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
        assert_eq!(state, SandboxState::Succeeded);
        assert!(sandbox.cost(&instance.id).await.unwrap().wall_seconds >= 0.0);
        let long = sandbox
            .start(&SandboxSpec { run_id: "run-2".into(), command: vec!["sleep".into(), "30".into()], ..SandboxSpec::default() })
            .await
            .unwrap();
        sandbox.cancel(&long.id).await.unwrap();
        assert_eq!(sandbox.status(&long.id).await.unwrap().state, SandboxState::Cancelled);
        // No secret setting shows in what the providers print.
        let printed = format!("{:?} {:?}", dev.providers, dev.providers.principal_of("SMS"));
        assert!(!printed.contains(RELAY_TOKEN) && !printed.contains(VAULT_TOKEN), "{printed}");
    }

    case("swapping one SMS provider for another is only a config change");
    let (delivered_a, delivered_b);
    {
        // Dev sends through sms-relay-a.
        delivered_a = send_notice(&dev, "+15550100", "The plan is ready.").await.unwrap();
        let sent = sms_relay_a::outbox().sent();
        let request = sent.iter().rfind(|r| r.method == "POST").unwrap();
        assert_eq!(request.url, "http://relay-a.test/v1/messages");
        assert_eq!(request.headers["authorization"], format!("Bearer {RELAY_TOKEN}"));
        assert_eq!(
            serde_json::from_str::<Value>(&request.body).unwrap(),
            json!({ "to": "+15550100", "text": "The plan is ready.", "from": "+15550000" })
        );
        // Another environment names sms-relay-b and the settings it reads:
        // the same code sends through it.
        let other = api::providers::from_values(BTreeMap::from([
            ("SMS".to_owned(), text("sms-relay-b")),
            ("SMS_URL".to_owned(), text("http://relay-b.test")),
            ("SMS_ACCOUNT".to_owned(), text("AC-1")),
            ("SMS_TOKEN".to_owned(), secret("relay-b-token-value")),
        ]))
        .unwrap();
        let other = deps(&dev, other);
        delivered_b = send_notice(&other, "+15550100", "The plan is ready.").await.unwrap();
        let sent = sms_relay_b::outbox().sent();
        let request = sent.last().unwrap();
        assert_eq!(request.url, "http://relay-b.test/accounts/AC-1/send");
        assert!(request.headers["authorization"].starts_with("Basic "));
        assert_eq!(request.body, "To=%2B15550100&Body=The+plan+is+ready.");
        assert_eq!(
            (delivered_a["provider"].as_str(), delivered_a["providerMessageId"].as_str()),
            (Some("sms-relay-a"), Some("a-2"))
        );
        assert_eq!(
            (delivered_b["provider"].as_str(), delivered_b["providerMessageId"].as_str()),
            (Some("sms-relay-b"), Some("b-1"))
        );
    }

    case("a credential reaches the runner alone, and the record holds its reference and time");
    {
        vault_broker::vault().stock(
            VAULT_TOKEN,
            vault_broker::Item {
                vault: "ops".into(),
                id: "reports-api".into(),
                title: "Reports API".into(),
                urls: vec!["https://reports.test".into()],
                fields: vec![("password".into(), SecretValue::new(PASSWORD))],
            },
        );
        let vault = dev.providers.credentials("VAULT").unwrap();
        let listed = vault.list(&CredentialQuery { site: Some("https://reports.test/login".into()) }).await.unwrap();
        assert_eq!(listed.iter().map(|l| (l.reference.as_str(), l.title.as_str())).collect::<Vec<_>>(), [("vault://ops/reports-api/password", "Reports API")]);
        assert!(vault.list(&CredentialQuery { site: Some("https://elsewhere.test".into()) }).await.unwrap().is_empty());
        let runner = Headers::default();
        let records = Mutex::new(Vec::new());
        let keep = |use_: &api::runtime::providers::CredentialUse| records.lock().unwrap().push(use_.clone());
        let target = InjectionTarget::Header { site: "https://reports.test".into(), name: "Authorization".into() };
        let used = use_credential(UseCredential {
            provider: vault.as_ref(),
            provider_name: dev.providers.provider_of("VAULT").unwrap(),
            reference: "vault://ops/reports-api/password",
            target: &target,
            runner: &runner,
            record: Some(&keep),
            now: None,
        })
        .await
        .unwrap();
        assert_eq!(runner.0.lock().unwrap().as_slice(), [("Authorization".to_owned(), PASSWORD.to_owned())]);
        assert_eq!((used.reference.as_str(), used.provider.as_str()), ("vault://ops/reports-api/password", "vault-broker"));
        let injection = vault.resolve("vault://ops/reports-api/password", &target).await.unwrap();
        let printed = [
            format!("{injection:?}"),
            serde_json::to_string(&injection).unwrap(),
            serde_json::to_string(&used).unwrap(),
            serde_json::to_string(&listed).unwrap(),
            format!("{:?}", records.lock().unwrap()),
        ]
        .join("\n");
        assert!(!printed.contains(PASSWORD), "{printed}");
        // A vault outside the broker's scope, and a site the item is not
        // for, are refused.
        let refused = vault.resolve("vault://finance/payroll/password", &target).await.err().unwrap();
        assert!(refused.to_string().contains("is not one this broker may read"), "{refused}");
        let elsewhere = InjectionTarget::Header { site: "https://elsewhere.test".into(), name: "Authorization".into() };
        assert!(vault.resolve("vault://ops/reports-api/password", &elsewhere).await.is_err());
    }

    case("a config that names an unknown provider or leaves a setting unset is refused at start, naming no value");
    {
        let refused = |config: Vec<(&str, ConfigValue)>| {
            api::providers::from_values(config.into_iter().map(|(k, v)| (k.to_owned(), v)).collect())
                .unwrap_err()
                .problems
        };
        assert_eq!(
            refused(vec![("SMS", text("sms-relay-x"))]),
            ["slot SMS names provider sms-relay-x, which the server does not link (it links local-sandbox, sms-relay-a, sms-relay-b, stub-model, text-preview, vault-broker)"]
        );
        assert_eq!(
            refused(vec![("SMS", text("sms-relay-b")), ("SMS_URL", text("http://relay-b.test")), ("SMS_TOKEN", secret("relay-b-token-value"))]),
            ["provider sms-relay-b in slot SMS needs SMS_ACCOUNT, which is not set"]
        );
        let problems = refused(vec![("SMS", text("sms-relay-a")), ("SMS_URL", text("u")), ("SMS_TOKEN", text("relay-a-plain-value"))]);
        assert_eq!(problems, ["provider sms-relay-a in slot SMS reads SMS_TOKEN as a secret, and the config holds it as no secret"]);
        assert!(!problems.join("").contains("relay-a-plain-value"));
        assert_eq!(refused(vec![("MODEL", text("sms-relay-a"))]), ["slot MODEL takes a model provider, and provider sms-relay-a implements channel"]);
        // A slot an environment leaves unset has no provider.
        let none = api::providers::from_values(BTreeMap::new()).unwrap();
        assert!(none.channel("SMS").err().unwrap().to_string().contains("names no provider in this environment"));
    }

    case("a provider that calls the app is authenticated as itself, with its role alone");
    {
        let relay = dev.providers.principal_of("SMS").unwrap();
        let model = dev.providers.principal_of("MODEL").unwrap();
        let authenticator = ExtensionAuthenticator::new(vec![
            ExtensionKey { key: SecretValue::new("relay-key"), name: "sms-relay-a".into(), role: relay.permissions.clone() },
            ExtensionKey { key: SecretValue::new("model-key"), name: "stub-model".into(), role: model.permissions.clone() },
        ]);
        let router = build_router(Implementations::new(Handlers { authenticator: Arc::new(authenticator) }, pool.clone()));
        for delivered in [&delivered_a, &delivered_b] {
            let id = delivered["id"].as_str().unwrap();
            let (status, body) = get(&router, &format!("/api/deliveries/{id}"), Some("relay-key")).await;
            assert_eq!(status, 200, "{body}");
            assert_eq!(body["data"]["provider"], delivered["provider"]);
            assert_eq!(body["data"]["recipient"], "+15550100");
        }
        let id = delivered_a["id"].as_str().unwrap();
        assert_eq!(get(&router, &format!("/api/deliveries/{id}"), Some("model-key")).await.0, 403);
        assert_eq!(get(&router, &format!("/api/deliveries/{id}"), Some("forged-key")).await.0, 401);
        assert_eq!(get(&router, &format!("/api/deliveries/{id}"), None).await.0, 401);
    }

    pool.close();
    admin.batch_execute(&format!("DROP SCHEMA {schema} CASCADE")).await.unwrap();
}
