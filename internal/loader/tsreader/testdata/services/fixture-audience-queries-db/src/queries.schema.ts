import { Identity } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { query } from "@superschematic/db";

// ---- the documents the caller sees, by title --------------------------------

export abstract class DocumentSearch {
  // Documents whose title holds the text, in any case; every one when null.
  search: Nullable<string>;
}

export abstract class DocumentRow {
  id: Identity.UUID;
  title: string;
}

@query<DocumentSearch, DocumentRow>({
  sql: `SELECT d.id, d.title
    FROM document d
    WHERE :audience(d)
      AND (:search::text IS NULL OR d.title ILIKE '%' || :search || '%')
    ORDER BY d.title, d.id`,
})
export abstract class VisibleDocuments {}

// ---- a folder's documents, each table held to the caller --------------------

export abstract class FolderParams {
  folder: Identity.UUID;
}

export abstract class FolderDocumentRow {
  id: Identity.UUID;
  title: string;
  folderName: string;
}

@query<FolderParams, FolderDocumentRow>({
  sql: `SELECT d.id, d.title, f."name" AS folder_name
    FROM document AS d
    JOIN folder f ON f.id = d.folder_id AND :audience(f)
    WHERE :audience(d) AND f.id = :folder
    ORDER BY d.title, d.id`,
})
export abstract class FolderDocuments {}

// ---- the documents a page at a time -----------------------------------------

@query<DocumentSearch, DocumentRow>({
  name: "documentPage",
  sql: `SELECT d.id, d.title
    FROM document d
    WHERE (:search::text IS NULL OR d.title ILIKE '%' || :search || '%') AND :audience(d)`,
  page: { key: ["title", "id"] },
})
export abstract class DocumentPage {}

// ---- the documents through the projection's view ----------------------------

@query<DocumentSearch, DocumentRow>({
  sql: `SELECT l.id, l.title
    FROM document_list l
    WHERE :audience(l)
      AND (:search::text IS NULL OR l.title ILIKE '%' || :search || '%')
    ORDER BY l.title, l.id`,
})
export abstract class ListedDocuments {}
