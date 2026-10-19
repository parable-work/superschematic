//! A Postgres storage's password provider (D58): what a host hands the
//! storage when a connection's password is a credential it refreshes, an
//! IAM access token over the Cloud SQL socket. Each runs against the
//! Postgres `SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL` names, its password
//! taken out of the URL and given by the provider alone.

#![cfg(feature = "postgres")]

mod support;

use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};

use superschematic_engine_storage::{Db, Error, PasswordProvider, PostgresOptions};

/// `url`, a `postgres://` URL, without its password, and the password.
fn without_password(url: &str) -> (String, Option<String>) {
    let Some((scheme, rest)) = url.split_once("://") else {
        return (url.to_owned(), None);
    };
    let (authority, tail) = match rest.find('/') {
        Some(at) => rest.split_at(at),
        None => (rest, ""),
    };
    let Some((credentials, host)) = authority.rsplit_once('@') else {
        return (url.to_owned(), None);
    };
    match credentials.split_once(':') {
        Some((user, password)) => (
            format!("{scheme}://{user}@{host}{tail}"),
            Some(password.to_owned()),
        ),
        None => (url.to_owned(), None),
    }
}

/// The options of the target's own schema, with `provider` and no
/// listener unless `listen`.
fn options(target: &support::Target, provider: PasswordProvider, listen: bool) -> PostgresOptions {
    PostgresOptions {
        schema: target.schema().expect("a Postgres target").to_owned(),
        listen,
        connect_timeout: Duration::from_secs(5),
        password: Some(provider),
        ..Default::default()
    }
}

#[test]
fn the_provider_gives_the_password_of_each_new_connection() {
    let test = "the_provider_gives_the_password_of_each_new_connection";
    let Some(target) = support::postgres(test) else {
        return;
    };
    let (url, password) = without_password(target.url().unwrap());
    let asked = Arc::new(AtomicUsize::new(0));
    let counted = Arc::clone(&asked);
    let provider = PasswordProvider::new(move || {
        counted.fetch_add(1, Ordering::SeqCst);
        Ok(password.clone().unwrap_or_default())
    });
    let debug = format!("{:?}", options(&target, provider.clone(), true));
    assert!(
        debug.contains("password: Some(PasswordProvider)"),
        "the options' Debug names the provider and no password: {debug}"
    );
    let db = Db::open_postgres(&url, options(&target, provider, true))
        .expect("open with the provider's password");
    let rows = db.read(|tx| tx.all("SELECT 1 AS one", &[])).expect("read");
    assert_eq!(
        rows.first().map(|row| row.i64("one").expect("an integer")),
        Some(1)
    );
    // The first connection's and the listener's, each asked as it opens.
    let deadline = Instant::now() + Duration::from_secs(10);
    while !db.notifier().listening() {
        assert!(Instant::now() < deadline, "the listener did not connect");
        std::thread::sleep(Duration::from_millis(20));
    }
    assert!(
        asked.load(Ordering::SeqCst) >= 2,
        "the provider was asked {} times",
        asked.load(Ordering::SeqCst)
    );
}

#[test]
fn a_provider_that_fails_fails_the_connection() {
    let test = "a_provider_that_fails_fails_the_connection";
    let Some(target) = support::postgres(test) else {
        return;
    };
    let (url, _) = without_password(target.url().unwrap());
    let provider = PasswordProvider::new(|| Err("the metadata server did not answer".to_owned()));
    match Db::open_postgres(&url, options(&target, provider, false)) {
        Err(Error::Connection { message }) => assert!(
            message.contains(
                "the password of a Postgres connection: the metadata server did not answer"
            ),
            "{message}"
        ),
        Err(other) => panic!("the open failed otherwise: {other:?}"),
        Ok(_) => panic!("the open passed without a password"),
    }
}

#[test]
fn the_providers_password_is_the_one_the_server_checks() {
    let test = "the_providers_password_is_the_one_the_server_checks";
    let Some(target) = support::postgres(test) else {
        return;
    };
    let (url, password) = without_password(target.url().unwrap());
    if password.is_none() {
        eprintln!("{test}: the URL names no password, so the server may check none; skipped");
        return;
    }
    let provider = PasswordProvider::new(|| Ok("not the password".to_owned()));
    match Db::open_postgres(&url, options(&target, provider, false)) {
        Err(Error::Connection { .. } | Error::Database { .. }) => {}
        Err(other) => panic!("the open failed otherwise: {other:?}"),
        Ok(_) => panic!("the server took a wrong password"),
    }
}
