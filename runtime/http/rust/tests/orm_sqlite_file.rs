//! A SQLite file as a server holds it (D116): a write commits or rolls
//! back as its work answers, a read runs beside the writer, and both run
//! with a tokio runtime and without one.

use std::sync::Arc;

use superschematic_http_runtime::orm::sqlite::{rusqlite, Database};
use superschematic_http_runtime::orm::Error;

fn file() -> (tempdir::Dir, Arc<Database>) {
    let dir = tempdir::Dir::new();
    let path = dir.path().join("notes.sqlite");
    rusqlite::Connection::open(&path)
        .and_then(|connection| {
            connection
                .execute_batch("CREATE TABLE note (id INTEGER PRIMARY KEY, body TEXT NOT NULL)")
        })
        .expect("create the file");
    let database = Arc::new(Database::open(&path).expect("open the file"));
    (dir, database)
}

mod tempdir {
    use std::path::{Path, PathBuf};

    /// A directory of the test's own, removed as it drops.
    pub struct Dir(PathBuf);

    impl Dir {
        pub fn new() -> Self {
            let path = std::env::temp_dir().join(format!(
                "superschematic-orm-file-{}-{}",
                std::process::id(),
                uuid::Uuid::new_v4().simple()
            ));
            std::fs::create_dir_all(&path).expect("create the directory");
            Self(path)
        }

        pub fn path(&self) -> &Path {
            &self.0
        }
    }

    impl Drop for Dir {
        fn drop(&mut self) {
            let _ = std::fs::remove_dir_all(&self.0);
        }
    }
}

fn count(connection: &rusqlite::Connection) -> Result<i64, Error> {
    connection
        .query_row("SELECT COUNT(*) FROM note", [], |row| row.get(0))
        .map_err(|err| Error::Database(err.to_string()))
}

fn insert(connection: &rusqlite::Connection, body: &str) -> Result<(), Error> {
    connection
        .execute("INSERT INTO note (body) VALUES (?1)", [body])
        .map(|_| ())
        .map_err(|err| Error::Database(err.to_string()))
}

#[test]
fn a_write_commits_when_its_work_answers_and_rolls_back_when_it_fails() {
    let (_dir, database) = file();
    database
        .write(|connection| insert(connection, "kept"))
        .expect("a write");
    let failed: Result<(), Error> = database.write(|connection| {
        insert(connection, "lost")?;
        Err(Error::Invalid("no".to_owned()))
    });
    assert!(matches!(failed, Err(Error::Invalid(_))));
    assert_eq!(database.read(count).expect("a read"), 1);
    // A reading connection writes nothing.
    assert!(database
        .read(|connection| insert(connection, "refused"))
        .is_err());
    assert_eq!(database.read(count).unwrap(), 1);
}

#[test]
fn a_read_runs_beside_the_writer_and_sees_what_is_committed() {
    let (_dir, database) = file();
    let seen = database
        .write(|connection| {
            insert(connection, "pending")?;
            // The write is not committed yet: a reader reads the file as
            // it was.
            database.read(count)
        })
        .expect("a write that reads");
    assert_eq!(seen, 0);
    assert_eq!(database.read(count).unwrap(), 1);
}

#[test]
fn the_file_opens_in_wal_mode_and_holds_to_its_foreign_keys() {
    let (_dir, database) = file();
    let mode: String = database
        .read(|connection| {
            connection
                .query_row("PRAGMA journal_mode", [], |row| row.get(0))
                .map_err(|err| Error::Database(err.to_string()))
        })
        .unwrap();
    assert_eq!(mode, "wal");
    let keys: i64 = database
        .write(|connection| {
            connection
                .query_row("PRAGMA foreign_keys", [], |row| row.get(0))
                .map_err(|err| Error::Database(err.to_string()))
        })
        .unwrap();
    assert_eq!(keys, 1);
    assert!(Database::open(database.path().with_extension("missing")).is_err());
}

#[test]
fn a_stack_names_the_file_by_a_sqlite_url() {
    use superschematic_http_runtime::orm::sqlite::connect;
    use superschematic_http_runtime::stackconfig::Database as Field;
    let (_dir, database) = file();
    let url = format!("sqlite:{}", database.path().display());
    let opened = connect(&Field::Url(url)).expect("open by its URL");
    assert_eq!(opened.read(count).unwrap(), 0);
    assert!(connect(&Field::Url("postgres://localhost/notes".to_owned())).is_err());
    assert!(connect(&Field::Url("sqlite:".to_owned())).is_err());
}

#[test]
fn work_runs_where_it_stands_without_a_runtime() {
    let (_dir, database) = file();
    let thread = std::thread::current().id();
    let work = database.write_async(move |connection| {
        assert_eq!(std::thread::current().id(), thread);
        insert(connection, "inline")
    });
    // No runtime drives the future: it is ready when first polled.
    let mut work = std::pin::pin!(work);
    let waker = std::task::Waker::noop();
    let mut context = std::task::Context::from_waker(waker);
    match std::future::Future::poll(work.as_mut(), &mut context) {
        std::task::Poll::Ready(out) => out.expect("a write"),
        std::task::Poll::Pending => panic!("the write waited"),
    }
    assert_eq!(database.read(count).unwrap(), 1);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn work_runs_off_the_async_threads_on_tokio() {
    let (_dir, database) = file();
    let thread = std::thread::current().id();
    database
        .write_async(move |connection| {
            assert_ne!(std::thread::current().id(), thread);
            insert(connection, "blocking")
        })
        .await
        .expect("a write");
    let read = database.read_async(count).await.expect("a read");
    assert_eq!(read, 1);
}
