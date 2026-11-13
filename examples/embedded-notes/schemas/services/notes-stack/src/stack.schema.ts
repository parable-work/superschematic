import { NotesApi } from "@notes/notes-api";
import { NotesDigest } from "@notes/notes-digest";
import { NotesWeb } from "@notes/notes-web";
import { environment, stack } from "@superschematic/stack";

// The embedded target's authoring types, as stack init writes a target's,
// are in embedded-target.ts beside this file.
//
// notes-api, notes-digest, which calls it, and the page, on one origin in
// every environment (D91): the page's own host serves each API under
// /api/<service>.
@stack({
  deploy: [NotesApi, NotesDigest, NotesWeb],
  expose: [NotesDigest],
  origins: [{ site: NotesWeb, apis: [NotesApi] }]
})
export abstract class Notes {}

// The app a person installs: one binary, which superschematic stack build
// Desktop builds. It listens on 127.0.0.1 at PORT, 4170 when unset, and
// keeps notes-db under its data directory, which it migrates as it starts
// from the plans in plans/Desktop/notes-db/.
@environment({ target: "embedded", embedded: { port: 4170 } })
export abstract class Desktop {}
