//! The file processor (D159): what attachments (D154) and search (D155)
//! call on a stored file. It extracts the file's text and makes a preview,
//! an image a list shows in place of the file. A processor names the media
//! types it reads; for any other it answers `None`, and core tries the
//! next one.

use async_trait::async_trait;
use serde::{Deserialize, Serialize};

use crate::BoxError;

/// A file to process: its bytes and what they are.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct FileInput {
    pub media_type: String,
    pub bytes: Vec<u8>,
    pub name: Option<String>,
}

/// A file's text.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct ExtractedText {
    pub text: String,
    /// The pages it came from, for a paged document.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub pages: Option<u32>,
    /// Whether the text stops short of the file's.
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub truncated: bool,
}

/// How large a preview may be.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct PreviewOptions {
    pub max_width: Option<u32>,
    pub max_height: Option<u32>,
}

/// A preview: an image's bytes and what they are.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Preview {
    pub media_type: String,
    pub bytes: Vec<u8>,
    pub width: u32,
    pub height: u32,
}

/// A file processor.
#[async_trait]
pub trait FileProcessor: Send + Sync {
    /// The media types it reads, each a type or a family (`image/*`).
    fn media_types(&self) -> Vec<String>;

    /// The file's text, or `None` for a file it does not read.
    async fn extract_text(&self, file: &FileInput) -> Result<Option<ExtractedText>, BoxError>;

    /// The file's preview, or `None` for a file it does not preview.
    async fn preview(
        &self,
        file: &FileInput,
        options: &PreviewOptions,
    ) -> Result<Option<Preview>, BoxError>;
}

/// Whether a processor that reads `media_types` reads a file of
/// `media_type`: an equal type, or one of a family (`image/*`), with the
/// type's parameters and case left aside.
pub fn reads_media_type<S: AsRef<str>>(media_types: &[S], media_type: &str) -> bool {
    let kind = media_type
        .split(';')
        .next()
        .unwrap_or("")
        .trim()
        .to_ascii_lowercase();
    media_types.iter().any(|entry| {
        let want = entry.as_ref().to_ascii_lowercase();
        want == kind || (want.ends_with("/*") && kind.starts_with(&want[..want.len() - 1]))
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_processor_reads_its_types_and_families() {
        let types = ["text/plain", "image/*"];
        assert!(reads_media_type(&types, "text/plain; charset=utf-8"));
        assert!(reads_media_type(&types, "IMAGE/PNG"));
        assert!(!reads_media_type(&types, "text/markdown"));
        assert!(!reads_media_type(&types, "imagex/png"));
    }
}
