export abstract class Commented {
  /** One line. */
  single: string;
  /** First line.
   * Last line carries the terminator. */
  closedOnText: string;
  /**
   * First line.
   * Last line.
   */
  closedAlone: string;
  /* Plain block. */
  plainBlock: string;
  // Line comment.
  lineComment: string;
}
