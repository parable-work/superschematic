package rustsdkgen

// clientVersionSDKTest is tests/client_version.rs of the generated SDK
// crate of fixture-nested-arrays-api (TestNestedArraysSDKCrateBuildsAndRuns),
// with SDK_CRATE replaced by the crate's module name (D143). A local server
// records each request's X-Client-Version: ClientConfig.client puts it on
// every call, a client without one sends none, a name or a version the
// header cannot carry is a config error, and a 426 client_too_old is an
// error whose client_too_old() holds the client, its version and the
// minimum.
const clientVersionSDKTest = `use std::io::{BufRead, BufReader, Read, Write};
use std::net::TcpListener;
use std::sync::mpsc;
use std::thread;

use serde_json::json;
use SDK_CRATE::types;
use SDK_CRATE::{ClientConfig, ClientIdentity, ClientTooOld, FixtureNestedArraysApiSdk, SDKError};

const GRID_ID: &str = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40";

/// Answers one request per canned response, a status line and a JSON body,
/// and sends each request's X-Client-Version, or None, down the channel.
fn serve(responses: Vec<(&'static str, String)>) -> (String, mpsc::Receiver<Option<String>>) {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let base_url = format!("http://{}", listener.local_addr().unwrap());
    let (seen, received) = mpsc::channel();
    thread::spawn(move || {
        for (status, body) in responses {
            let (mut stream, _) = listener.accept().unwrap();
            let mut reader = BufReader::new(stream.try_clone().unwrap());
            let mut length = 0usize;
            let mut client_version = None;
            loop {
                let mut line = String::new();
                reader.read_line(&mut line).unwrap();
                let line = line.trim_end();
                if line.is_empty() {
                    break;
                }
                if let Some((name, value)) = line.split_once(':') {
                    if name.eq_ignore_ascii_case("content-length") {
                        length = value.trim().parse().unwrap();
                    }
                    if name.eq_ignore_ascii_case("x-client-version") {
                        client_version = Some(value.trim().to_string());
                    }
                }
            }
            let mut request_body = vec![0u8; length];
            reader.read_exact(&mut request_body).unwrap();
            seen.send(client_version).unwrap();
            write!(
                stream,
                "HTTP/1.1 {status}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                body.len()
            )
            .unwrap();
        }
    });
    (base_url, received)
}

#[tokio::test]
async fn a_named_client_sends_its_version_on_every_call() {
    let (base_url, seen) = serve(vec![
        ("404 Not Found", json!({"detail": "none", "code": "not_found"}).to_string()),
        ("404 Not Found", json!({"detail": "none", "code": "not_found"}).to_string()),
        ("404 Not Found", json!({"detail": "none", "code": "not_found"}).to_string()),
    ]);
    let id: types::IdentityUUID = GRID_ID.parse().unwrap();
    let named = FixtureNestedArraysApiSdk::new(ClientConfig {
        base_url: base_url.clone(),
        client: Some(ClientIdentity::new("acme-cli", "0.5.0-rc.1+b7")),
        ..Default::default()
    })
    .unwrap();
    for _ in 0..2 {
        assert_eq!(named.grid.get_grid(id, None).await.unwrap_err().status_code(), Some(404));
        assert_eq!(seen.recv().unwrap().as_deref(), Some("acme-cli/0.5.0-rc.1+b7"));
    }
    let unnamed = FixtureNestedArraysApiSdk::new(ClientConfig::with_base_url(base_url, None, None)).unwrap();
    assert_eq!(unnamed.grid.get_grid(id, None).await.unwrap_err().status_code(), Some(404));
    assert_eq!(seen.recv().unwrap(), None);
}

#[test]
fn a_client_the_header_cannot_carry_is_a_config_error() {
    for (name, version) in [
        ("", "1.0.0"),
        ("acme/cli", "1.0.0"),
        ("acme cli", "1.0.0"),
        ("-cli", "1.0.0"),
        ("cli", ""),
        ("cli", "1.0.0\r\nX-Other: 1"),
        ("cli", "1.0/0"),
    ] {
        let built = FixtureNestedArraysApiSdk::new(ClientConfig {
            base_url: "http://127.0.0.1:1".to_string(),
            client: Some(ClientIdentity::new(name, version)),
            ..Default::default()
        });
        match built {
            Err(SDKError::Config(message)) => assert!(message.contains("client."), "{message}"),
            Err(other) => panic!("{name:?}/{version:?}: unexpected {other:?}"),
            Ok(_) => panic!("{name:?}/{version:?} was not refused"),
        }
    }
}

#[tokio::test]
async fn a_426_client_too_old_says_the_minimum() {
    let (base_url, _seen) = serve(vec![
        (
            "426 Upgrade Required",
            json!({
                "type": "about:blank", "title": "Upgrade Required", "status": 426,
                "detail": "cli 0.4.0 is older than 0.5.0", "code": "client_too_old", "requestId": "req-9",
                "details": {"client": "cli", "version": "0.4.0", "minimum": "0.5.0"}
            })
            .to_string(),
        ),
        ("426 Upgrade Required", json!({"status": 426, "detail": "use TLS", "code": "upgrade_required"}).to_string()),
        ("404 Not Found", json!({"detail": "none", "code": "client_too_old"}).to_string()),
    ]);
    let sdk = FixtureNestedArraysApiSdk::new(ClientConfig {
        base_url,
        client: Some(ClientIdentity::new("cli", "0.4.0")),
        ..Default::default()
    })
    .unwrap();
    let id: types::IdentityUUID = GRID_ID.parse().unwrap();

    let refused = sdk.grid.get_grid(id, None).await.unwrap_err();
    assert_eq!(refused.status_code(), Some(426));
    assert_eq!(refused.error_code(), Some("client_too_old"));
    assert_eq!(
        refused.client_too_old(),
        Some(ClientTooOld {
            client: "cli".to_string(),
            version: "0.4.0".to_string(),
            minimum: "0.5.0".to_string(),
        })
    );
    // A 426 with another code, and the code on another status, are not it.
    let other = sdk.grid.get_grid(id, None).await.unwrap_err();
    assert_eq!((other.status_code(), other.client_too_old()), (Some(426), None));
    let not_found = sdk.grid.get_grid(id, None).await.unwrap_err();
    assert_eq!((not_found.status_code(), not_found.client_too_old()), (Some(404), None));
}
`
