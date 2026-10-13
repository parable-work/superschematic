//! The two databases the engine runs on, and SQL text written for both
//! (D58). Statements are SQL text, portable by default: they bind `?`
//! placeholders, which [`crate::tokens::postgres_placeholders`] numbers
//! `$1..$n` for Postgres. A statement the two databases word differently
//! (JSON access, `rowid`, a parameter whose type Postgres cannot infer) is
//! written once for each with [`Sql::per`]. This is not a dialect layer
//! that rewrites statements, which D16 turned down.

use std::borrow::Cow;
use std::fmt;

/// Which database a [`crate::Db`] runs on.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum Dialect {
    /// SQLite, a file one process writes: a test, a local run, a Topcoat
    /// app (D44).
    Sqlite,
    /// Postgres, the database of the DB service an engine type belongs to,
    /// shared by every instance of a server.
    Postgres,
}

impl Dialect {
    /// The database's name as messages write it.
    pub fn name(self) -> &'static str {
        match self {
            Dialect::Sqlite => "SQLite",
            Dialect::Postgres => "Postgres",
        }
    }
}

impl fmt::Display for Dialect {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.name())
    }
}

/// One statement's text: the same for both databases, or written for each.
///
/// Every call that runs SQL takes `impl Into<Sql>`, so a `&str` or a
/// `String` is a statement both databases run.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Sql<'a> {
    /// Text both databases run as written (Postgres after its `?`s are
    /// numbered).
    Same(Cow<'a, str>),
    /// Text for each database.
    Per {
        /// What SQLite runs.
        sqlite: Cow<'a, str>,
        /// What Postgres runs; its `?`s are numbered too, and text that
        /// numbers its own (`$1`) runs as written.
        postgres: Cow<'a, str>,
    },
}

impl<'a> Sql<'a> {
    /// A statement both databases run.
    pub fn same(text: impl Into<Cow<'a, str>>) -> Self {
        Sql::Same(text.into())
    }

    /// A statement written once for each database.
    pub fn per(sqlite: impl Into<Cow<'a, str>>, postgres: impl Into<Cow<'a, str>>) -> Self {
        Sql::Per {
            sqlite: sqlite.into(),
            postgres: postgres.into(),
        }
    }

    /// The text `dialect` runs.
    pub fn text(&self, dialect: Dialect) -> &str {
        match self {
            Sql::Same(text) => text,
            Sql::Per { sqlite, postgres } => match dialect {
                Dialect::Sqlite => sqlite,
                Dialect::Postgres => postgres,
            },
        }
    }

    /// The statement, owning its text.
    pub fn into_owned(self) -> Sql<'static> {
        match self {
            Sql::Same(text) => Sql::Same(Cow::Owned(text.into_owned())),
            Sql::Per { sqlite, postgres } => Sql::Per {
                sqlite: Cow::Owned(sqlite.into_owned()),
                postgres: Cow::Owned(postgres.into_owned()),
            },
        }
    }
}

impl<'a> From<&'a str> for Sql<'a> {
    fn from(text: &'a str) -> Self {
        Sql::Same(Cow::Borrowed(text))
    }
}

impl<'a> From<&'a String> for Sql<'a> {
    fn from(text: &'a String) -> Self {
        Sql::Same(Cow::Borrowed(text.as_str()))
    }
}

impl From<String> for Sql<'_> {
    fn from(text: String) -> Self {
        Sql::Same(Cow::Owned(text))
    }
}

impl<'a> From<Cow<'a, str>> for Sql<'a> {
    fn from(text: Cow<'a, str>) -> Self {
        Sql::Same(text)
    }
}

impl<'a> From<&'a Sql<'a>> for Sql<'a> {
    fn from(sql: &'a Sql<'a>) -> Self {
        match sql {
            Sql::Same(text) => Sql::Same(Cow::Borrowed(text)),
            Sql::Per { sqlite, postgres } => Sql::Per {
                sqlite: Cow::Borrowed(sqlite),
                postgres: Cow::Borrowed(postgres),
            },
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_statement_is_the_same_or_one_for_each() {
        let same = Sql::from("SELECT 1");
        assert_eq!(same.text(Dialect::Sqlite), "SELECT 1");
        assert_eq!(same.text(Dialect::Postgres), "SELECT 1");
        let per = Sql::per("SELECT rowid FROM t", "SELECT ctid FROM t");
        assert_eq!(per.text(Dialect::Sqlite), "SELECT rowid FROM t");
        assert_eq!(per.text(Dialect::Postgres), "SELECT ctid FROM t");
        assert_eq!(per.clone().into_owned(), per);
        assert_eq!(Sql::from(&per), per);
        assert_eq!(Dialect::Postgres.to_string(), "Postgres");
    }
}
