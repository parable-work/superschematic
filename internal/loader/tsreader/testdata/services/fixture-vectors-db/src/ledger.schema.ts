import { Embedding, Finance, Generic, Identity, Temporal } from "superscalar";
import { Nullable, Validate, display } from "@superschematic/schema";
import { AutoGenerate, Encrypted, column, index, key, table } from "@superschematic/db";

// A line of a ledger: a signed amount in its currency's minor units, a
// debit negative, and the exchange rate it was booked at, exact.
export abstract class LedgerLine {
  @key
  id: AutoGenerate<Identity.UUID>;

  @display({ currency: { field: "currency" } })
  amount: Finance.SignedMoney;

  currency: Finance.Currency;

  rate: Validate<Generic.Decimal, { precision: 10; scale: 4 }>;

  confidence: Nullable<Validate<Generic.Decimal, { precision: 5; scale: 2 }>>;

  quantity: Nullable<Generic.Decimal>;

  bookedAt: Temporal.DateTime;
}

// A text and its embedding, 1024 numbers, found by cosine distance.
@index<TextEmbedding>(["embedding"], { method: "hnsw", ops: "vector_cosine_ops", m: 16, efConstruction: 64 })
export abstract class TextEmbedding {
  @key
  id: AutoGenerate<Identity.UUID>;

  text: string;

  embedding: Validate<Embedding.Vector, { dimensions: 1024 }>;
}

// A document a nearest-neighbour search finds, by a small embedding: by
// cosine distance through an HNSW index, and by Euclidean distance through
// an IVFFlat one, which a migration builds inside its step.
@index<Passage>(["embedding"], { method: "hnsw", ops: "vector_cosine_ops" })
@index<Passage>(["embedding"], { name: "embedding_l2", method: "ivfflat", ops: "vector_l2_ops", lists: 4, concurrently: false })
export abstract class Passage {
  @key
  id: AutoGenerate<Identity.UUID>;

  title: string;

  topic: Nullable<string>;

  embedding: Validate<Embedding.Vector, { dimensions: 3 }>;

  // An embedding of the title alone, which a passage may lack.
  titleEmbedding: Nullable<Validate<Embedding.Vector, { dimensions: 3 }>>;

  rates: Nullable<Validate<Generic.Decimal, { precision: 6; scale: 3 }>[]>;

  deletedAt: Nullable<Temporal.DateTime>;
}

// A chunk of a note, in a table a database named before the schema did
// (D85): @table and @column name the table, its vector column and its
// soft-delete column, in mixed case, which every statement quotes, and its
// index is named after them. Its note is Encrypted<T>, sealed at rest, so a
// row nearest reads opens it as any read does.
@table("NoteChunk")
@index<Chunk>(["embedding"], { name: "by_vector", method: "hnsw", ops: "vector_l2_ops" })
export abstract class Chunk {
  @key
  id: AutoGenerate<Identity.UUID>;

  @column("chunkText")
  text: string;

  @column("chunkVector")
  embedding: Validate<Embedding.Vector, { dimensions: 3 }>;

  note: Nullable<Encrypted<string>>;

  @column("removedAt")
  deletedAt: Nullable<Temporal.DateTime>;
}
