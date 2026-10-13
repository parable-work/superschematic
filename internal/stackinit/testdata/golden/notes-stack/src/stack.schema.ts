import { NotesApi } from "@schemas/notes-api";
import { NotesIndex } from "@schemas/notes-index";
import { NotesWeb } from "@schemas/notes-web";
import { environment, stack } from "@superschematic/stack";

// Written by superschematic stack init. It deploys every API and site of the
// tree, and the databases and buckets they reach come with them.
// It exposes each API an end user or a third party calls, one with an
// operation that is not @requireService, and each API a site calls; only
// other servers reach the rest.
@stack({ deploy: [NotesApi, NotesIndex, NotesWeb], expose: [NotesApi] })
export abstract class Notes {}

// This machine.
@environment({ target: "local" })
export abstract class Dev {}

// The cloud.
@environment({ target: "fake", fake: { project: "notes-staging", region: "us-east1" } })
export abstract class Staging {}
