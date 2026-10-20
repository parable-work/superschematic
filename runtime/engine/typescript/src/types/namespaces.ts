/*
Namespaces (D16): every call resolves the one it names, `default` when it
names none; the engine's options configure the others and the shared one,
and engine.namespaces creates, archives and lists more.
*/

/** The namespace a call that names none acts in. */
export const DEFAULT_NAMESPACE = 'default';

/** A namespace name: lowercase letters, digits and hyphens, starting with a letter, at most 63 characters. */
export const NAMESPACE_NAME = /^[a-z][a-z0-9-]{0,62}$/;

/** The namespaces an engine's options configure. */
export interface NamespaceOptions {
  /** Namespaces besides `default`. */
  names?: readonly string[];
  /** The namespace every other one looks schema names up in after itself. */
  shared?: string;
}

/** A namespace as engine.namespaces lists it. */
export interface NamespaceRecord {
  name: string;
  /** configured: the engine's options name it, and only they change it; created: a create made it while an engine ran. */
  origin: 'configured' | 'created';
  /** Whether it is the shared namespace, which every other one looks schema names up in after itself. */
  shared: boolean;
  /** active, or archived: read as it was, refusing every write until it is unarchived. */
  state: 'active' | 'archived';
  /** When a create made it, and who; null for a configured one. */
  createdAt: number | null;
  createdBy: string | null;
  /** When it was archived, and who; null unless it is archived. */
  archivedAt: number | null;
  archivedBy: string | null;
}
