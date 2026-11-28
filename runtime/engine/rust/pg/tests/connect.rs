//! A connection with each sslmode to the Postgres
//! `SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL` names, through the synchronous
//! client the storage connects with. The test asks the server first whether
//! it offers TLS, and holds each mode to what libpq does against it: on a
//! server without TLS, `disable` and `prefer` connect in the clear and
//! `require` fails; on one with a self-signed certificate, `prefer` and
//! `require` connect over TLS and `verify-ca` and `verify-full` fail.
//! Skipped, saying so, without the variable.

use postgres::Client;
use superschematic_engine_pg::tls_for;

fn database() -> Option<String> {
    match std::env::var("SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL") {
        Ok(url) if !url.is_empty() => Some(url),
        _ => {
            println!("set SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL to connect with each sslmode");
            None
        }
    }
}

/// The URL with its sslmode, whatever it named, replaced by `mode`.
fn with_mode(url: &str, mode: &str) -> String {
    let (base, query) = url.split_once('?').unwrap_or((url, ""));
    let mut pairs: Vec<&str> = query
        .split('&')
        .filter(|pair| !pair.is_empty() && !pair.starts_with("sslmode="))
        .collect();
    let mode = format!("sslmode={mode}");
    pairs.push(&mode);
    format!("{base}?{}", pairs.join("&"))
}

fn connect(url: &str, mode: &str) -> Result<Client, String> {
    let (text, tls) = tls_for(&with_mode(url, mode)).map_err(|error| error.to_string())?;
    let config: postgres::Config = text.parse().map_err(|error| format!("{error}"))?;
    config.connect(tls).map_err(|error| format!("{error}"))
}

/// Whether the session runs over TLS.
fn encrypted(client: &mut Client) -> bool {
    client
        .query_one(
            "SELECT coalesce((SELECT ssl FROM pg_stat_ssl WHERE pid = pg_backend_pid()), false)",
            &[],
        )
        .expect("the session's TLS")
        .get(0)
}

#[test]
fn each_sslmode_connects_as_libpq_would() {
    let Some(url) = database() else { return };
    let mut plain = connect(&url, "disable").expect("sslmode=disable connects");
    assert!(!encrypted(&mut plain), "sslmode=disable runs in the clear");
    let offers: String = plain
        .query_one("SHOW ssl", &[])
        .expect("whether the server offers TLS")
        .get(0);
    let offers = offers == "on";

    let mut preferred = connect(&url, "prefer").expect("sslmode=prefer connects");
    assert_eq!(
        encrypted(&mut preferred),
        offers,
        "sslmode=prefer takes TLS when the server offers it"
    );

    match connect(&url, "require") {
        Ok(mut client) => {
            assert!(offers, "sslmode=require connected to a server without TLS");
            assert!(encrypted(&mut client), "sslmode=require runs over TLS");
        }
        Err(error) => assert!(!offers, "sslmode=require failed over TLS: {error}"),
    }

    if offers {
        // A test server's certificate is its own, which no system root
        // signs: the modes that verify refuse it.
        for mode in ["verify-ca", "verify-full"] {
            assert!(
                connect(&url, mode).is_err(),
                "sslmode={mode} took a certificate no system root signs"
            );
        }
    }
}
