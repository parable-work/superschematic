import { auth, behaviorOperations, rowFeatureOperations } from "@superschematic/api";
import { Issue, Note } from "@schemas/issues-db";

// An issue's conversation: post a message, whose attachment block names a
// file of the issue's, edit, react, resolve and the rest (D103).
@behaviorOperations(Issue)
@auth
export abstract class IssueActions {}

// An issue's references, backlinks and files (#723): ask for an upload,
// complete it once the bytes are in the bucket, file it onto a note, list
// and download, and the rows that point at the issue.
@rowFeatureOperations(Issue)
@auth
export abstract class IssueOperations {}

// A note's: the same, but its files are never removed through the API.
@rowFeatureOperations(Note, {
  operations: ["references", "addReference", "removeReference", "backlinks", "attachments", "beginUpload", "completeUpload", "download", "fileAttachment"]
})
@auth
export abstract class NoteOperations {}
