//! The file processor's pass (D154, D159, #603) held to
//! `runtime/http/testdata/file_processing_parity.json`, which the Go
//! runtime writes and its pass and the TypeScript runtime's run too: the
//! same files, objects and processors give the same counts, rows,
//! previews, calls and logged reasons. The file's comment states the
//! harness. It holds one test, in a process of its own, since tracing
//! decides once per process which of its call sites any subscriber hears.

use std::collections::BTreeMap;
use std::path::Path;
use std::pin::Pin;
use std::sync::{Arc, Mutex};
use std::task::{Context, Poll};

use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use serde_json::Value;
use superschematic_http_runtime::bucket::{
    Body, Bucket, BucketError, Bytes, ListOptions, Object, Page, PutOptions, PutResult, Reader,
    SignedUrlOptions, Stream,
};
use superschematic_http_runtime::logging::json_lines;
use superschematic_http_runtime::orm::file_processing::{
    process_files, FileProcessingOptions, FileProcessingRun, PendingFiles,
};
use superschematic_http_runtime::orm::rowfeatures::{
    PendingFile, Processed, ProcessingKind, ProcessingState,
};
use superschematic_http_runtime::orm::Error;
use superschematic_http_runtime::providers::{
    providers_from, reads_media_type, ConfigValue, ExtractedText, FileInput, FileProcessor,
    Preview, PreviewOptions, ProviderDefinition, ProviderInstance, ProvidersOptions,
    RuntimeInterface,
};
use superschematic_http_runtime::BoxError;
use tracing_subscriber::layer::SubscriberExt as _;

const SCENARIOS: &str = "../testdata/file_processing_parity.json";

#[derive(Clone, Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct ProcessorSpec {
    slot: String,
    name: String,
    media_types: Vec<String>,
    extract: String,
    preview: String,
    #[serde(default)]
    fails_on: Vec<String>,
}

#[derive(Clone, Debug, PartialEq, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
struct Row {
    object_key: String,
    media_type: String,
    extraction: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    extracted_text: Option<String>,
    preview: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    preview_object: Option<String>,
}

#[derive(Debug, Deserialize)]
struct ObjectSpec {
    name: String,
    #[serde(default)]
    text: String,
    #[serde(default)]
    size: usize,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct Scenario {
    name: String,
    processors: Vec<ProcessorSpec>,
    files: Vec<Row>,
    objects: Vec<ObjectSpec>,
    max_bytes: u64,
    want: Value,
}

/// A bucket kept in memory: each object's bytes and media type.
#[derive(Default)]
struct MemBucket(Mutex<Objects>);

/// Each object's bytes and media type, by name.
type Objects = BTreeMap<String, (Vec<u8>, Option<String>)>;

/// The bytes in one chunk.
struct Once(Option<Bytes>);

impl Stream for Once {
    type Item = Result<Bytes, BucketError>;

    fn poll_next(mut self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<Option<Self::Item>> {
        Poll::Ready(self.0.take().map(Ok))
    }
}

fn object(name: &str, held: &(Vec<u8>, Option<String>)) -> Object {
    Object {
        name: name.to_string(),
        size: held.0.len() as u64,
        content_type: held.1.clone(),
        updated: None,
    }
}

#[async_trait]
impl Bucket for MemBucket {
    fn name(&self) -> &str {
        "mem"
    }

    async fn put(
        &self,
        name: &str,
        body: Body,
        options: PutOptions,
    ) -> Result<PutResult, BucketError> {
        let stored = (
            body.as_bytes().expect("a preview is put whole").to_vec(),
            options.content_type,
        );
        let out = object(name, &stored);
        self.0.lock().unwrap().insert(name.to_string(), stored);
        Ok(PutResult {
            object: out,
            created: true,
        })
    }

    async fn head(&self, name: &str) -> Result<Option<Object>, BucketError> {
        Ok(self
            .0
            .lock()
            .unwrap()
            .get(name)
            .map(|held| object(name, held)))
    }

    async fn get(&self, name: &str) -> Result<Reader, BucketError> {
        let held = self.0.lock().unwrap();
        let Some(there) = held.get(name) else {
            return Err(BucketError::NotFound {
                bucket: "mem".to_string(),
                object: name.to_string(),
            });
        };
        Ok(Reader::new(
            object(name, there),
            Once(Some(Bytes::from(there.0.clone()))),
        ))
    }

    async fn delete(&self, name: &str) -> Result<(), BucketError> {
        self.0.lock().unwrap().remove(name);
        Ok(())
    }

    async fn list(&self, _options: ListOptions) -> Result<Page, BucketError> {
        Ok(Page::default())
    }

    async fn signed_url(
        &self,
        name: &str,
        _options: SignedUrlOptions,
    ) -> Result<String, BucketError> {
        Ok(format!("https://mem.example/{name}"))
    }
}

/// The attachments' pending and processed over rows in memory, as the
/// repositories run them.
struct MemAttachments(Mutex<Vec<Row>>);

fn state_name(state: ProcessingState) -> &'static str {
    match state {
        ProcessingState::None => "none",
        ProcessingState::Pending => "pending",
        ProcessingState::Done => "done",
        ProcessingState::Failed => "failed",
    }
}

impl PendingFiles for MemAttachments {
    async fn pending(&self, kind: ProcessingKind, limit: u32) -> Result<Vec<PendingFile>, Error> {
        let rows = self.0.lock().unwrap();
        let mut seen = Vec::new();
        let mut out = Vec::new();
        for row in rows.iter() {
            let state = match kind {
                ProcessingKind::Extraction => &row.extraction,
                ProcessingKind::Preview => &row.preview,
            };
            if state != "pending" || seen.contains(&row.object_key) {
                continue;
            }
            seen.push(row.object_key.clone());
            out.push(PendingFile {
                object_key: row.object_key.clone(),
                media_type: row.media_type.clone(),
                content_hash: "h".into(),
            });
        }
        out.truncate(limit as usize);
        Ok(out)
    }

    async fn processed(
        &self,
        object_key: &str,
        extraction: Option<&Processed>,
        preview: Option<&Processed>,
    ) -> Result<u64, Error> {
        let mut rows = self.0.lock().unwrap();
        let mut n = 0;
        for row in rows.iter_mut().filter(|row| row.object_key == object_key) {
            if let Some(p) = extraction {
                row.extraction = state_name(p.state).into();
                if p.value.is_some() {
                    row.extracted_text = p.value.clone();
                }
            }
            if let Some(p) = preview {
                row.preview = state_name(p.state).into();
                if p.value.is_some() {
                    row.preview_object = p.value.clone();
                }
            }
            n += 1;
        }
        Ok(n)
    }
}

/// A processor a scenario names, which records each call into it.
struct ScenarioProcessor {
    spec: ProcessorSpec,
    asked: Arc<Mutex<Vec<String>>>,
}

impl ScenarioProcessor {
    fn fails(&self, media_type: &str) -> bool {
        self.spec
            .fails_on
            .iter()
            .any(|t| reads_media_type(&[t], media_type))
    }
}

#[async_trait]
impl FileProcessor for ScenarioProcessor {
    fn media_types(&self) -> Vec<String> {
        self.spec.media_types.clone()
    }

    async fn extract_text(&self, file: &FileInput) -> Result<Option<ExtractedText>, BoxError> {
        self.asked.lock().unwrap().push(format!(
            "{}:extractText:{}",
            self.spec.name, file.media_type
        ));
        if self.fails(&file.media_type) || self.spec.extract == "fail" {
            return Err("the extraction failed".into());
        }
        Ok((self.spec.extract == "upper").then(|| ExtractedText {
            text: String::from_utf8_lossy(&file.bytes).to_uppercase(),
            ..ExtractedText::default()
        }))
    }

    async fn preview(
        &self,
        file: &FileInput,
        _options: &PreviewOptions,
    ) -> Result<Option<Preview>, BoxError> {
        self.asked
            .lock()
            .unwrap()
            .push(format!("{}:preview:{}", self.spec.name, file.media_type));
        if self.fails(&file.media_type) || self.spec.preview == "fail" {
            return Err("the preview failed".into());
        }
        Ok((self.spec.preview == "svg").then(|| Preview {
            media_type: "image/svg+xml".into(),
            bytes: b"<svg/>".to_vec(),
            width: 1,
            height: 1,
        }))
    }
}

async fn run(scenario: &Scenario, lines: &Arc<Mutex<Vec<String>>>) -> Value {
    lines.lock().unwrap().clear();
    let asked = Arc::new(Mutex::new(Vec::new()));
    let mut options = ProvidersOptions::default();
    for spec in &scenario.processors {
        options.slots.insert(spec.slot.clone(), "files".into());
        options
            .config
            .insert(spec.slot.clone(), ConfigValue::Text(spec.name.clone()));
        let (spec, asked) = (spec.clone(), Arc::clone(&asked));
        options.definitions.push(ProviderDefinition::new(
            spec.name.clone(),
            RuntimeInterface::Files,
            move |_, _| {
                Ok(ProviderInstance::Files(Arc::new(ScenarioProcessor {
                    spec: spec.clone(),
                    asked: Arc::clone(&asked),
                })))
            },
        ));
    }
    let providers = providers_from(options).unwrap();
    let bucket = MemBucket::default();
    for o in &scenario.objects {
        let bytes = if o.size > 0 {
            vec![0; o.size]
        } else {
            o.text.as_bytes().to_vec()
        };
        bucket
            .0
            .lock()
            .unwrap()
            .insert(o.name.clone(), (bytes, None));
    }
    let rows = MemAttachments(Mutex::new(scenario.files.clone()));
    let mut pass = FileProcessingOptions::new(&providers, &bucket);
    if scenario.max_bytes > 0 {
        pass.max_bytes = Some(scenario.max_bytes);
    }
    let mut runs: Vec<FileProcessingRun> = Vec::new();
    for _ in 0..2 {
        runs.push(process_files(&rows, &pass).await.unwrap());
    }
    let previews: Vec<Value> = bucket
        .0
        .lock()
        .unwrap()
        .iter()
        .filter_map(|(name, (bytes, content_type))| {
            content_type.as_ref().map(|ct| {
                serde_json::json!({ "name": name, "contentType": ct, "text": String::from_utf8_lossy(bytes) })
            })
        })
        .collect();
    let logged = lines.lock().unwrap().clone();
    let mut reasons = Vec::new();
    for line in &logged {
        let line: Value = serde_json::from_str(line).unwrap();
        assert_eq!(
            line["msg"], "a file was not processed",
            "{}: {line}",
            scenario.name
        );
        reasons.push(line["reason"].clone());
    }
    for o in &scenario.objects {
        if !o.text.is_empty() {
            assert!(
                !logged.join("\n").contains(&o.text),
                "{}: a failure logs {:?}",
                scenario.name,
                o.text
            );
        }
    }
    let files = rows.0.lock().unwrap().clone();
    let asked = asked.lock().unwrap().clone();
    serde_json::json!({
        "runs": runs,
        "files": files,
        "previews": previews,
        "asked": asked,
        "reasons": reasons,
    })
}

#[tokio::test]
async fn every_scenario_gives_what_the_go_and_typescript_passes_give() {
    let lines = Arc::new(Mutex::new(Vec::new()));
    let sink = Arc::clone(&lines);
    let layer = json_lines("files-stack", "files-api-process")
        .with_writer(move |line| sink.lock().unwrap().push(line.to_string()));
    let _default = tracing::subscriber::set_default(tracing_subscriber::registry().with(layer));
    let data = std::fs::read_to_string(Path::new(env!("CARGO_MANIFEST_DIR")).join(SCENARIOS))
        .expect("the scenarios");
    let file: Value = serde_json::from_str(&data).unwrap();
    let scenarios: Vec<Scenario> = serde_json::from_value(file["scenarios"].clone()).unwrap();
    assert!(!scenarios.is_empty());
    for scenario in &scenarios {
        assert_eq!(
            run(scenario, &lines).await,
            scenario.want,
            "{}",
            scenario.name
        );
    }
}
