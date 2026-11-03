package sqlutil

// The settings of a vector index's scan that nearest takes as options
// (D89): pgvector's, each set for the transaction the read runs in, which
// nearest opens itself when none is open. NearestSettings runs them in one
// statement, each a NearestSetting's SQL, whose parameter is the option's
// value as text.

// NearestSettings is the statement that sets the settings %s, joined by
// ", ".
const NearestSettings = "SELECT %s"

// The values of the iterativeScan option, pgvector's hnsw.iterative_scan
// (0.8 and later): off, the default, which reads ef_search candidates and
// keeps those a filter keeps; and relaxed_order and strict_order, which
// scan on until the read has its rows or max_scan_tuples are read, the
// first in an order a little out of the distance's, the second in it.
const (
	IterativeScanOff     = "off"
	IterativeScanRelaxed = "relaxed_order"
	IterativeScanStrict  = "strict_order"
)

// NearestSetting is a setting an option of nearest sets: Option is the
// option, Setting pgvector's setting, and SQL the call that sets it to the
// parameter $%d for the transaction. Values are the values of the option
// the setting takes, and nil an integer from Min to Max; a value outside
// Values leaves the setting as it is, as ivfflat has no strict order.
type NearestSetting struct {
	Option  string
	Setting string
	SQL     string
	Values  []string
	Min     int
	Max     int
}

// maxInt32 is the largest value of a Postgres integer setting.
const maxInt32 = 1<<31 - 1

// NearestSettingsOf are the settings nearest's options set, in the order
// both ORMs set them: hnsw's ef_search, iterative scan and the tuples it
// scans at most, then ivfflat's probes, iterative scan and the probes it
// scans at most. Each range is pgvector's.
func NearestSettingsOf() []NearestSetting {
	setting := func(option, name string, values []string, min, max int) NearestSetting {
		return NearestSetting{Option: option, Setting: name, SQL: "set_config('" + name + "', $%d, true)", Values: values, Min: min, Max: max}
	}
	hnswScans := []string{IterativeScanOff, IterativeScanRelaxed, IterativeScanStrict}
	ivfflatScans := []string{IterativeScanOff, IterativeScanRelaxed}
	return []NearestSetting{
		setting("efSearch", "hnsw.ef_search", nil, 1, 1000),
		setting("iterativeScan", "hnsw.iterative_scan", hnswScans, 0, 0),
		setting("maxScanTuples", "hnsw.max_scan_tuples", nil, 1, maxInt32),
		setting("probes", "ivfflat.probes", nil, 1, 32768),
		setting("iterativeScan", "ivfflat.iterative_scan", ivfflatScans, 0, 0),
		setting("maxProbes", "ivfflat.max_probes", nil, 1, 32768),
	}
}
