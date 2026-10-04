//! How a validator spells the path of a value inside the one it checks.

/// The element at index of the list at base: `tags[2]`, `grid[0][1]`.
pub fn index_path(base: &str, index: usize) -> String {
    format!("{base}[{index}]")
}

/// The value under key of the map at base, as a generated validator spells
/// it: `labels.color` (D14).
pub fn key_path(base: &str, key: &str) -> String {
    format!("{base}.{key}")
}

/// The value under key of a map argument, as a server spells it when it
/// decodes the request: `labels[color]` (D12, amended).
pub fn bracket_key_path(base: &str, key: &str) -> String {
    format!("{base}[{key}]")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn paths_compose() {
        assert_eq!(index_path(&index_path("grid", 0), 1), "grid[0][1]");
        assert_eq!(key_path("labels", "color"), "labels.color");
        assert_eq!(
            index_path(&bracket_key_path("byTeam", "a"), 2),
            "byTeam[a][2]"
        );
    }
}
