/// The length of text as every validator counts it: Unicode code points
/// (D14, amended), not UTF-8 bytes.
pub fn code_points(text: &str) -> usize {
    text.chars().count()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn counts_code_points() {
        assert_eq!(code_points(""), 0);
        assert_eq!(code_points("abc"), 3);
        assert_eq!(code_points("é"), 1);
        assert_eq!(code_points("😀a"), 2);
    }
}
