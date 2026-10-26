import { Identity } from "superscalar";
import { jsonField } from "@superschematic/schema";
import { AutoGenerate, index, key, rawSql } from "@superschematic/db";

// Each declaration below is one the loader refuses (D83).

// A vector index over a column that is not a vector, which every column is
// until vector columns arrive; a predicate of two statements.
@index<Note>(["body"], { method: "hnsw" })
@index<Note>(["body"], { name: "two", where: "true; DROP TABLE note" })
// A statement a plan cannot run again, a piece after one no piece is, and
// two that run after each other.
@rawSql({
  id: "notes_view",
  up: `
    CREATE OR REPLACE VIEW note_bodies AS SELECT body FROM note;
    CREATE INDEX note_body ON note (body)
  `
})
@rawSql({ id: "orphan", after: "missing", up: "CREATE OR REPLACE VIEW orphan AS SELECT 1" })
@rawSql({ id: "first", after: "second", up: "CREATE OR REPLACE VIEW first AS SELECT 1" })
@rawSql({ id: "second", after: "first", up: "CREATE OR REPLACE VIEW second AS SELECT 1" })
export abstract class Note {
  @key
  id: AutoGenerate<Identity.UUID>;
  body: string;
}

// Raw SQL on a type that gets no table.
@jsonField
@rawSql({ id: "on_json", up: "CREATE OR REPLACE VIEW on_json AS SELECT 1" })
export abstract class Attachment {
  name: string;
}
