/*
The in-process notice of committed events. One process writes the file
(D16), and every event is appended through that process's storage, so a
notice after each commit that appended one misses nothing: no other
writer's events can go unannounced. Several processes writing one file
would need a notice across processes, which this is not.

A commit sends one notice per event it appended, with that event's
cursor. A watcher reads the log from its own cursor, as its own
principal, so a notice tells it no more than that the log grew.
*/

import type { Storage } from '../storage/storage.js';

/** What EventLog.watch calls: after each commit that appended events, and once when the engine closes. */
export interface EventWatcher {
  /** The log grew: called after the commit, once for each event it appended, with that event's cursor. */
  committed(cursor: number): void;
  /** The engine closed; nothing follows. */
  closed?(): void;
}

export class EventNotifier {
  private readonly watchers = new Set<EventWatcher>();
  private open = true;

  /** watch registers a watcher and returns the function that removes it. */
  watch(watcher: EventWatcher): () => void {
    if (!this.open) {
      throw new Error('the engine is closed');
    }
    this.watchers.add(watcher);
    return () => {
      this.watchers.delete(watcher);
    };
  }

  /** The watchers registered, for tests and diagnostics. */
  get size(): number {
    return this.watchers.size;
  }

  /** committed tells every watcher the log grew to cursor. */
  committed(cursor: number): void {
    for (const watcher of [...this.watchers]) {
      try {
        watcher.committed(cursor);
      } catch {
        // One watcher's failure is not the writer's, nor another watcher's.
      }
    }
  }

  /** close tells every watcher the engine closed, then forgets them. */
  close(): void {
    if (!this.open) {
      return;
    }
    this.open = false;
    const watchers = [...this.watchers];
    this.watchers.clear();
    for (const watcher of watchers) {
      try {
        watcher.closed?.();
      } catch {
        // As in committed.
      }
    }
  }
}

// One notifier per storage handle, so appendEvent, which the instance
// store, the catalog and behaviors call with the storage alone, reaches
// the watchers of the engine that owns it.
const notifiers = new WeakMap<Storage, EventNotifier>();

/** notifierOf returns the notifier of a storage handle, making it on first use. */
export function notifierOf(storage: Storage): EventNotifier {
  let notifier = notifiers.get(storage);
  if (!notifier) {
    notifier = new EventNotifier();
    notifiers.set(storage, notifier);
  }
  return notifier;
}
