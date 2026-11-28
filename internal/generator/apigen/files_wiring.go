package apigen

import (
	ir "github.com/parable-work/superschematic/ir"
)

// The file processor's pass in a Go server (D154, D159, D184, #603): an
// API whose database declares @attachments and whose @envVars class names
// a files slot gets ProcessPendingFiles in wiring.go, the twin of a
// TypeScript API's processPendingFiles. It runs the HTTP runtime's
// fileprocessing.Process over Deps.DB's Attachments() and Deps.Providers,
// with the bucket the implementation names in Wiring.Files. Nothing calls
// it on its own: an implementation's job runs it on a schedule, or an
// operation after an upload completes, as in TypeScript, since what is
// pending lives in the attachments' rows, not in a queue.

// WithFiles is w with the file processor's pass when db, the API's
// database, declares @attachments and providers, the API's, fill a files
// slot: a wiring of its own when w is nil, on SQLite when sqlite (#717).
// It answers w as it is otherwise.
func WithFiles(w *DepsWiring, db *ir.Schema, providers *DepsProviders, sqlite bool) *DepsWiring {
	if db == nil || len(db.AttachmentsTables()) == 0 || !providers.HasInterface("files") {
		return w
	}
	if w == nil {
		w = &DepsWiring{SQLite: sqlite}
	}
	w.Files = true
	return w
}

// HasInterface reports whether one of the API's provider slots takes
// iface.
func (p *DepsProviders) HasInterface(iface string) bool {
	if p == nil {
		return false
	}
	for _, s := range p.Slots {
		if s.Interface == iface {
			return true
		}
	}
	return false
}

// Wires reports whether an entrypoint wires anything of w: the notifier,
// the calendar, search or the grants. A wiring that holds the file
// processor's pass alone is the implementation's to call.
func (w *DepsWiring) Wires() bool {
	return w != nil && (w.Builds() || w.Calendar || w.Search || w.Grants)
}

// HasFileProcessing reports whether wiring.go holds ProcessPendingFiles.
func (o *APIOutput) HasFileProcessing() bool {
	return o.Deps.Wiring != nil && o.Deps.Wiring.Files
}
