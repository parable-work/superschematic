// Media.Photo stands in for a scalar an extension's scalar package defines:
// the brand names it, and the catalog the extension registers declares it a
// file upload. The core scalar package has no upload scalar.
export namespace Media {
  export type Photo = string & { readonly __brand: "Media.Photo" };
}
