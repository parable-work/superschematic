/** Read the argument from the query string. */
export type QueryParam<T> = T & { readonly __queryParam: true };
/** The operation's request body travels as an encrypted envelope. */
export type EncryptedField<T> = T & { readonly __encrypted: true };

/** Which operation lists a reference's targets, and which of their fields it holds and shows (D61). */
export type RefersToConfig<Target> = {
  /** The operation that lists the targets: "listProducts" or "ProductCatalog.listProducts". The API's one operation that lists them when unsaid. */
  readonly options?: string;
  /** The target's field the value holds: "id" unsaid. */
  readonly value?: keyof Target & string;
  /** The target's field that names an item: its @display titleField unsaid. */
  readonly label?: keyof Target & string;
};

/**
 * A value that names an item of Target, as an operation of this API lists
 * it (D61): `RefersTo<Identity.UUID, ProductView>`, or with its config,
 * `RefersTo<Identity.UUID, ProductView, { options: "listProducts" }>`. A
 * list refers element by element: `RefersTo<Identity.UUID, ProductView>[]`.
 */
export type RefersTo<T, Target, C extends RefersToConfig<Target> = {}> = T & {
  readonly __refersTo: { readonly target: Target; readonly config: C };
};

/**
 * The operation's search text: an optional query parameter whose words each
 * item's searchable text contains (D62). An operation takes one.
 */
export type SearchQuery<T extends string = string> = (T & { readonly __queryParam: true; readonly __search: true }) | null;

/**
 * The order a paged list is read in (D60, amended): a field Fields names,
 * ascending, or after a "-", descending; ties break by the item's id in
 * the same direction. @paginated's defaultSort is the order when none is
 * sent. An operation takes one.
 */
export type SortBy<Item, Fields extends readonly (keyof Item & string)[]> =
  | ((Fields[number] | `-${Fields[number]}`) & { readonly __queryParam: true; readonly __sortBy: Fields })
  | null;

/** The constraints of a FileUpload field (D81); each one unsaid admits any. */
export type FileUploadConfig = {
  /** The largest file, in bytes: 100 MiB unsaid. */
  readonly maxBytes?: number;
  /** The media types the part's Content-Type may name, without parameters: ["text/csv"]. */
  readonly contentTypes?: readonly string[];
  /** The extensions the part's filename may end in, with the dot, in any case: [".csv", ".ofx"]. */
  readonly extensions?: readonly string[];
};

/**
 * A field of an operation's input type that is a file (D81): the request
 * is multipart/form-data, the file travels as a part of its own named as
 * the field, and the input's other fields as JSON in the `data` part.
 * The implementation receives the file in `args.files`, and an SDK takes
 * it as a File or Blob: `statement: FileUpload<{ maxBytes: 5242880; extensions: [".csv"] }>`.
 */
export type FileUpload<C extends FileUploadConfig = {}> = string & { readonly __fileUpload: C };

/** What a FileDownload result answers (D81). */
export type FileDownloadConfig = {
  /** The body's media type, as Content-Type carries it: "text/csv" or "text/csv; charset=utf-8". */
  readonly contentType: string;
  /** The filename Content-Disposition offers when the implementation names none: "orders.csv". */
  readonly filename?: string;
};

/**
 * An operation's result that is a file, not JSON (D81): the route answers
 * its bytes with Content-Type and Content-Disposition, and an SDK returns
 * them as a Blob with the filename: `exportOrders(): FileDownload<{ contentType: "text/csv" }>`.
 */
export type FileDownload<C extends FileDownloadConfig> = { readonly __fileDownload: C };
