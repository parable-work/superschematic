package generator

// rustFiles reports whether the Rust API holds the file processor's pass
// (D154, D159, #614), the twin of a Go API's ProcessPendingFiles: its
// @envVars class names a files slot, and db, its database, declares
// @attachments. Its crate then writes src/files.rs over the database's
// Rust types.
func (r run) rustFiles(db string) bool {
	slots, err := r.Schema.ProviderSlotsOf()
	if err != nil || db == "" || r.Options.LoadDependency == nil {
		return false
	}
	files := false
	for _, iface := range slots {
		files = files || iface == "files"
	}
	if !files {
		return false
	}
	schema, err := r.LoadDependency(db)
	return err == nil && schema != nil && len(schema.AttachmentsTables()) > 0
}
