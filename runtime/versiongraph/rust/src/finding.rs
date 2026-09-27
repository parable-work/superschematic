//! Findings: problems in a tree that `compose` reports and `validate` lists,
//! as opposed to an [`Error`](crate::Error), which refuses the input.

use serde::Serialize;

use crate::compose::Entity;
use crate::descriptor::Graph;

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct Finding {
    pub code: &'static str,
    pub kind: String,
    #[serde(rename = "entityKey", skip_serializing_if = "Option::is_none")]
    pub entity_key: Option<String>,
    pub message: String,
}

impl Finding {
    pub fn new(
        code: &'static str,
        graph: &Graph,
        kind: usize,
        key: Option<&str>,
        message: String,
    ) -> Self {
        Self {
            code,
            kind: graph.kinds[kind].name.clone(),
            entity_key: key.map(str::to_owned),
            message,
        }
    }

    pub fn absent_parent(graph: &Graph, kind: usize, key: &str, parent: Entity) -> Self {
        let parent_kind = &graph.kinds[parent.0].name;
        Self::new(
            "absent_parent",
            graph,
            kind,
            Some(key),
            format!("parent {parent_kind} {:?} is not in the tree", parent.1),
        )
    }
}
