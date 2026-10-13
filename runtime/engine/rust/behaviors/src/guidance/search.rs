//! Search's guidance (`guidance/search.ts`): which fields a search reads
//! and how they weigh, and, with vectors, their model and dimensions and
//! who settles them, so a caller knows whether a vector search,
//! staleEmbeddings or settleEmbeddings applies on this type.

use superschematic_engine::behaviors::{BehaviorGuidance, DescribeTarget, OperationGuidance};
use superschematic_engine::json::js_number;

use super::{list, said, sentences};
use crate::search::SearchConfig;

const PAGE: &str = "Pass next as cursor for the page after, until it is null.";

pub(crate) fn guidance(config: &SearchConfig, target: &DescribeTarget) -> BehaviorGuidance {
    let weighed: Vec<String> = config
        .fields
        .iter()
        .zip(&config.weights)
        .map(|(field, weight)| {
            if *weight == 1.0 {
                field.clone()
            } else {
                format!("{field} (weight {})", js_number(*weight))
            }
        })
        .collect();
    let type_name = &target.type_name;
    let vectors = config.vectors.as_ref();
    let no_vectors = |what: &str| OperationGuidance {
        do_not_use_when: Some(format!(
            "Do not use: Search on {type_name} keeps no vectors, so {what} is refused."
        )),
        ..OperationGuidance::default()
    };
    let summary = sentences(&[
        Some(format!("Full-text search over {}.", list(&weighed, "and"))),
        Some(match vectors {
            None => "It keeps no vectors.".to_owned(),
            Some(vectors) => format!(
                "With vectors of {} dimensions from model {}, which an outside embedder with permission {} settles.",
                vectors.dimensions, vectors.model, vectors.permission
            ),
        }),
    ]);
    let search = said(
        Some(sentences(&[
            Some(format!(
                "Use to find {type_name} instances by words in {}: plain words match when the fields hold every one; syntax fts5 takes an FTS5 expression.",
                list(&config.fields, "or")
            )),
            vectors.map(|vectors| {
                format!(
                    "A vector of {} numbers from model {} ranks by similarity, alone or fused with a query.",
                    vectors.dimensions, vectors.model
                )
            }),
        ])),
        Some("Do not use to page through every instance; call list.".to_owned()),
        Some(format!(
            "Returns hits best first, each its id, rank and a snippet of the field that matched. {PAGE}"
        )),
        Vec::new(),
    );
    let draft = if config.fields.len() == 1 {
        config.fields[0].clone()
    } else {
        format!("{}, joined by a blank line", list(&config.fields, "and"))
    };
    let similar = said(
        Some(sentences(&[
            Some(format!(
                "Use before creating a {type_name}, to find near-duplicates to reuse or link instead: with id, the instances nearest to one you have; with text, the ones nearest to a draft's {draft}."
            )),
            vectors.map(|vectors| {
                format!(
                    "With text, a vector of {} numbers from model {} computed from the same text ranks by similarity too.",
                    vectors.dimensions, vectors.model
                )
            }),
        ])),
        Some("Do not use to search by a few words; call search.".to_owned()),
        Some(format!(
            "Returns the nearest instances, leaving out the one named by id, and whether a vector ranked (embedded). {PAGE}"
        )),
        Vec::new(),
    );
    let stale = match vectors {
        None => no_vectors("listing stale embeddings"),
        Some(vectors) => said(
            Some(format!(
                "Use as an embedder: it lists the instances whose vector from {} is missing or stale, with the text to embed and its hash.",
                vectors.model
            )),
            None,
            Some(format!("Returns the model, the dimensions and the items. {PAGE}")),
            Vec::new(),
        ),
    };
    let settle = match vectors {
        None => no_vectors("a settle"),
        Some(vectors) => said(
            Some(format!(
                "Use as an embedder with permission {}: it stores at most 100 vectors of {} numbers, each with the hash staleEmbeddings gave.",
                vectors.permission, vectors.dimensions
            )),
            Some("Do not settle a vector computed from other text or another model: its hash no longer matches and it is skipped.".to_owned()),
            Some("Returns how many it settled and each skipped item with its reason, moved or not_found.".to_owned()),
            Vec::new(),
        ),
    };
    BehaviorGuidance {
        summary,
        operations: vec![
            ("search".to_owned(), search),
            ("similar".to_owned(), similar),
            ("staleEmbeddings".to_owned(), stale),
            ("settleEmbeddings".to_owned(), settle),
            (
                "create".to_owned(),
                OperationGuidance {
                    do_not_use_when: Some(
                        "Do not create a duplicate: call similar with the draft's text first, to find one to reuse."
                            .to_owned(),
                    ),
                    ..OperationGuidance::default()
                },
            ),
        ],
    }
}
