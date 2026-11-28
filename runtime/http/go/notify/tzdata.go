package notify

// On SQLite the notifier reads a recipient's wall time from Go's zone
// database (wallOf, #618), which a server's image may not hold; the
// binary carries its own.
import _ "time/tzdata"
