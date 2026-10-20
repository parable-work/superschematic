import { display } from "@superschematic/schema";

// summaryFields names a field twice.
@display({ noun: "Note", summaryFields: ["body", "body"] })
export abstract class Note {
  body: string;
}

// text takes two field displays.
export abstract class Memo {
  @display({ multiline: true })
  @display({ multiline: false })
  text: string;
}
