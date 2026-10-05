import { Identity } from "superscalar";
import { Validate } from "@superschematic/schema";
import { Acme, crossSell, feedKey, shelf } from "@acme/schema";

// A Catalog schema's classes are embedded structs (KindSpec.StructRole).
// @shelf and @feedKey come from @acme/schema and are only allowed in Catalog
// schemas; they land in the field's extensions.acme slot in the IR.
export abstract class Product {
  @shelf({ aisle: 3, bay: "B" })
  @feedKey
  sku: Identity.Slug;

  @shelf({ aisle: 7 })
  barcode: string;

  name: Identity.Name;

  // Acme.Photo is a file-upload scalar the acme scalar catalog declares
  // (8 MiB, JPEG, PNG or WebP); uploadMaxBytes lowers the limit to 2 MiB for
  // this field.
  photo: Validate<Acme.Photo, { uploadMaxBytes: 2097152 }>;
}

// A bundle is offered beside Product's listing. @crossSell names the class
// itself; the IR and the data forms write it {"class": "Product"}.
@crossSell({ with: Product })
export abstract class Bundle {
  @shelf({ aisle: 12 })
  @feedKey
  code: Identity.Slug;

  products: Product[];
}
