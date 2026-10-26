import { Embedding, Identity } from "superscalar";
import { Validate } from "@superschematic/schema";
import { AutoGenerate, index, key } from "@superschematic/db";

// Each declaration below is one the loader refuses (D84).

// A vector index with no metric, a parameter out of pgvector's range and
// another method's parameter; one over two keys that is unique; one over
// more dimensions than pgvector indexes.
@index<Doc>(["embedding"], { method: "hnsw", m: 1, lists: 10 })
@index<Doc>(["embedding", "title"], { name: "pair", method: "ivfflat", ops: "vector_l2_ops", unique: true })
@index<Doc>(["wide"], { name: "wide", method: "hnsw", ops: "vector_cosine_ops", efConstruction: 8 })
export abstract class Doc {
  @key
  id: AutoGenerate<Identity.UUID>;

  title: string;

  embedding: Validate<Embedding.Vector, { dimensions: 3 }>;

  wide: Validate<Embedding.Vector, { dimensions: 4096 }>;
}
