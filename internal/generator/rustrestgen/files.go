package rustrestgen

import (
	"fmt"
	"slices"
)

// The file processor's pass in a Rust API (D154, D159, #603, #614): an
// API whose database declares @attachments and whose @envVars class names
// a files slot gets src/files.rs, whose process_pending_files runs the
// HTTP runtime's orm::file_processing::process_files over the
// attachments of Deps.db's repositories and Deps.providers, with the
// bucket its caller names. It is the twin of a Go API's
// ProcessPendingFiles and a TypeScript API's processPendingFiles. Nothing
// calls it on its own: an implementation's job runs it on a schedule, or
// an operation after an upload completes, since what is pending lives in
// the attachments' rows, not in a queue.

// FilesFile is the file of an API crate that holds the file processor's
// pass.
const FilesFile = "files.rs"

// HasFileProcessing reports whether the crate holds src/files.rs: Deps
// says the database declares @attachments, and one of the API's provider
// slots takes the files interface.
func (o *APIOutput) HasFileProcessing() bool {
	return o.Deps != nil && o.Deps.FileProcessing && o.Deps.Database != "" && o.Providers != nil &&
		slices.ContainsFunc(o.Providers.Slots, func(s ProviderSlotInfo) bool { return s.Interface == "files" })
}

// checkFileProcessingDeps refuses a pass the crate cannot run: the
// attachments are read through the database's Rust types.
func (o *APIOutput) checkFileProcessingDeps() error {
	if !o.HasFileProcessing() || o.Deps.DatabaseTypesCrate != "" {
		return nil
	}
	return fmt.Errorf("rustrestgen: %s runs the file processor's pass over the attachments of %s (#614), whose repositories are in its Rust types, and %s generates none; enable outputs.types.rust in %s's config", o.SchemaName, o.Deps.Database, o.Deps.Database, o.Deps.Database)
}
