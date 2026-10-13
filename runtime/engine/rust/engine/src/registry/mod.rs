//! The schema registry (D16). So far, what a schema document is to the
//! engine (`document`) and the JSON types of its fields (`fields`), which
//! engine migration 10 reads to build the indexes of unique fields.

pub(crate) mod document;
pub(crate) mod fields;
