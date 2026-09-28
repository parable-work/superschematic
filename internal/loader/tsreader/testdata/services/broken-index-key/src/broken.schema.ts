import { Identity } from "superscalar";
import { AutoGenerate, HasMany, Relation, index, key } from "@superschematic/db";

// keyof Author admits the list relation posts, which has no column in the
// author table.
@index<Author>(["name", "posts"])
export abstract class Author {
  @key
  id: AutoGenerate<Identity.UUID>;
  name: string;
  posts: HasMany<Post>;
}

// The type argument names Author, so the compiler checks the keys against
// Author's fields, and name is not a field of Post.
@index<Author>(["name"])
export abstract class Post {
  @key
  id: AutoGenerate<Identity.UUID>;
  author: Relation<Author>;
  title: string;
}
