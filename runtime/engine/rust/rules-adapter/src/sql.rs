//! SQL text and values as the adapter writes them for either database,
//! and as runtime mode (`superschematic-rules-runtime`) writes its own
//! statements: the dialect, a quoted identifier, a placeholder, and with
//! the `postgres` feature a value bound as text.

/// The SQL a store's statements are written in.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Dialect {
    /// `$n` placeholders; a column read as `to_jsonb`'s JSON.
    Postgres,
    /// `?n` placeholders; a column read as it is stored.
    Sqlite,
}

impl Dialect {
    /// The dialect as a migration plan names it.
    pub fn as_str(self) -> &'static str {
        match self {
            Dialect::Sqlite => "sqlite",
            Dialect::Postgres => "postgres",
        }
    }
}

/// An identifier quoted as Postgres and SQLite both read one.
pub fn quote(identifier: &str) -> String {
    format!("\"{}\"", identifier.replace('"', "\"\""))
}

/// The nth placeholder in a dialect: `$n` on Postgres, `?n` on SQLite.
pub fn placeholder(dialect: Dialect, n: usize) -> String {
    match dialect {
        Dialect::Postgres => format!("${n}"),
        Dialect::Sqlite => format!("?{n}"),
    }
}

#[cfg(feature = "postgres")]
pub use text::Text;

#[cfg(feature = "postgres")]
mod text {
    use std::error::Error as StdError;

    use bytes::BytesMut;
    use tokio_postgres::types::{to_sql_checked, Format, IsNull, ToSql, Type};

    /// A parameter sent as text, which Postgres reads into whatever type it
    /// meets, or NULL. The synchronous `postgres` crate binds it too: its
    /// `ToSql` is tokio-postgres's.
    #[derive(Debug)]
    pub struct Text(pub Option<String>);

    impl ToSql for Text {
        fn to_sql(
            &self,
            _ty: &Type,
            out: &mut BytesMut,
        ) -> Result<IsNull, Box<dyn StdError + Sync + Send>> {
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
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn an_identifier_is_quoted_and_a_placeholder_numbered_per_dialect() {
        assert_eq!(quote("order"), "\"order\"");
        assert_eq!(quote("a\"b"), "\"a\"\"b\"");
        assert_eq!(placeholder(Dialect::Postgres, 3), "$3");
        assert_eq!(placeholder(Dialect::Sqlite, 3), "?3");
        assert_eq!(Dialect::Postgres.as_str(), "postgres");
        assert_eq!(Dialect::Sqlite.as_str(), "sqlite");
    }
}
