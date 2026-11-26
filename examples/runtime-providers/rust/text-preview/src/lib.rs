//! A file processor with no dependency (D159, D185): the Rust port of
//! `providers/text-preview`. It extracts the text of plain text and
//! Markdown, previews them as an SVG of their first lines, and makes a
//! thumbnail of a greyscale PGM image by averaging blocks of pixels.

use std::sync::Arc;

use async_trait::async_trait;
use superschematic_http_runtime::providers::{
    reads_media_type, ExtractedText, FileInput, FileProcessor, Preview, PreviewOptions,
    ProviderDefinition, ProviderInstance, ProviderSettingSpec, RuntimeInterface,
};
use superschematic_http_runtime::BoxError;

const TEXT: [&str; 2] = ["text/plain", "text/markdown"];
const PGM: &str = "image/x-portable-graymap";

/// A greyscale image: its size and one byte per pixel, rows top to
/// bottom.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Greymap {
    pub width: usize,
    pub height: usize,
    pub pixels: Vec<u8>,
}

/// Reads a PGM image, P2 (ASCII) or P5 (binary), of up to 255 levels.
pub fn read_pgm(bytes: &[u8]) -> Result<Greymap, BoxError> {
    let mut at = 0;
    let mut token = || -> String {
        loop {
            while at < bytes.len() && bytes[at].is_ascii_whitespace() {
                at += 1;
            }
            if bytes.get(at) != Some(&b'#') {
                break;
            }
            while at < bytes.len() && bytes[at] != b'\n' {
                at += 1;
            }
        }
        let start = at;
        while at < bytes.len() && !bytes[at].is_ascii_whitespace() {
            at += 1;
        }
        String::from_utf8_lossy(&bytes[start..at]).into_owned()
    };
    let magic = token();
    if magic != "P2" && magic != "P5" {
        return Err(format!("text-preview: {magic:?} is no PGM image").into());
    }
    let width: usize = token().parse().unwrap_or(0);
    let height: usize = token().parse().unwrap_or(0);
    let max: u32 = token().parse().unwrap_or(0);
    if width == 0 || height == 0 || max == 0 || max > 255 {
        return Err("text-preview: the PGM header is not a size and levels under 256".into());
    }
    let mut pixels = vec![0u8; width * height];
    if magic == "P5" {
        drop(token);
        let start = at + 1;
        let end = (start + pixels.len()).min(bytes.len());
        if start < end {
            pixels[..end - start].copy_from_slice(&bytes[start..end]);
        }
    } else {
        for pixel in &mut pixels {
            *pixel = token().parse().unwrap_or(0);
        }
    }
    if max != 255 {
        for pixel in &mut pixels {
            *pixel = u8::try_from((u32::from(*pixel) * 255 + max / 2) / max).unwrap_or(255);
        }
    }
    Ok(Greymap {
        width,
        height,
        pixels,
    })
}

/// Writes a greymap as a binary PGM, P5.
pub fn write_pgm(image: &Greymap) -> Vec<u8> {
    let mut out = format!("P5\n{} {}\n255\n", image.width, image.height).into_bytes();
    out.extend_from_slice(&image.pixels);
    out
}

/// The image made no larger than `max_width` by `max_height`, each pixel
/// the mean of the block it covers.
fn shrink(image: &Greymap, max_width: usize, max_height: usize) -> Greymap {
    let factor = 1
        .max(image.width.div_ceil(max_width))
        .max(image.height.div_ceil(max_height));
    let width = image.width.div_ceil(factor);
    let height = image.height.div_ceil(factor);
    let mut pixels = vec![0u8; width * height];
    for y in 0..height {
        for x in 0..width {
            let (mut sum, mut count) = (0u32, 0u32);
            for dy in 0..factor {
                for dx in 0..factor {
                    let (sy, sx) = (y * factor + dy, x * factor + dx);
                    if sy < image.height && sx < image.width {
                        sum += u32::from(image.pixels[sy * image.width + sx]);
                        count += 1;
                    }
                }
            }
            pixels[y * width + x] = u8::try_from((sum + count / 2) / count.max(1)).unwrap_or(255);
        }
    }
    Greymap {
        width,
        height,
        pixels,
    }
}

fn escape_xml(text: &str) -> String {
    text.chars()
        .map(|c| match c {
            '<' | '>' | '&' | '"' | '\'' => format!("&#{};", u32::from(c)),
            other => other.to_string(),
        })
        .collect()
}

/// An SVG of the first lines of `text`.
fn text_preview(text: &str, max_width: u32, max_height: u32) -> Preview {
    const LINE: u32 = 18;
    let fits = ((max_height.saturating_sub(8)) / LINE).max(1) as usize;
    let lines: Vec<&str> = text.lines().take(fits).collect();
    let height = max_height.min(u32::try_from(lines.len()).unwrap_or(u32::MAX) * LINE + 8);
    let chars = (max_width / 8).max(4) as usize;
    let rows: String = lines
        .iter()
        .enumerate()
        .map(|(i, line)| {
            let shown: String = if line.chars().count() > chars {
                line.chars().take(chars - 1).chain(['…']).collect()
            } else {
                (*line).to_owned()
            };
            format!(r#"<text x="4" y="{}">{}</text>"#, (i + 1) * LINE as usize, escape_xml(&shown))
        })
        .collect();
    let svg = format!(
        r##"<svg xmlns="http://www.w3.org/2000/svg" width="{max_width}" height="{height}" font-family="monospace" font-size="13"><rect width="100%" height="100%" fill="#fff"/>{rows}</svg>"##
    );
    Preview {
        media_type: "image/svg+xml".into(),
        bytes: svg.into_bytes(),
        width: max_width,
        height,
    }
}

struct Processor {
    max_chars: usize,
}

#[async_trait]
impl FileProcessor for Processor {
    fn media_types(&self) -> Vec<String> {
        TEXT.iter().chain([&PGM]).map(|t| (*t).to_owned()).collect()
    }

    async fn extract_text(&self, file: &FileInput) -> Result<Option<ExtractedText>, BoxError> {
        if !reads_media_type(&TEXT, &file.media_type) {
            return Ok(None);
        }
        let text = String::from_utf8_lossy(&file.bytes);
        let truncated = text.chars().count() > self.max_chars;
        Ok(Some(ExtractedText {
            text: text.chars().take(self.max_chars).collect(),
            pages: None,
            truncated,
        }))
    }

    async fn preview(&self, file: &FileInput, options: &PreviewOptions) -> Result<Option<Preview>, BoxError> {
        let max_width = options.max_width.unwrap_or(320);
        let max_height = options.max_height.unwrap_or(160);
        if reads_media_type(&TEXT, &file.media_type) {
            return Ok(Some(text_preview(&String::from_utf8_lossy(&file.bytes), max_width, max_height)));
        }
        if reads_media_type(&[PGM], &file.media_type) {
            let thumb = shrink(&read_pgm(&file.bytes)?, max_width as usize, max_height as usize);
            return Ok(Some(Preview {
                media_type: PGM.into(),
                bytes: write_pgm(&thumb),
                width: u32::try_from(thumb.width).unwrap_or(u32::MAX),
                height: u32::try_from(thumb.height).unwrap_or(u32::MAX),
            }));
        }
        Ok(None)
    }
}

/// The definition a server links.
pub fn definition() -> ProviderDefinition {
    ProviderDefinition::new("text-preview", RuntimeInterface::Files, |settings, _| {
        let max_chars = match settings.optional_text("MAX_CHARS")? {
            Some(text) => text.parse()?,
            None => 100_000,
        };
        Ok(ProviderInstance::Files(Arc::new(Processor { max_chars })))
    })
    .with_setting("MAX_CHARS", ProviderSettingSpec::plain().optional())
}
