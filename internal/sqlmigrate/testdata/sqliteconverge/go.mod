// A standalone module so the SQLite driver stays out of the compiler's
// module graph. TestConvergenceOnSQLite in the sqlmigrate package copies
// this directory, points it at the cases it wrote, and runs its test. The
// driver is the one the runner uses (runtime/migrate/go).
module example.com/superschematic/sqliteconverge

go 1.26.4

require modernc.org/sqlite v1.57.0

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/sys v0.47.0 // indirect
	modernc.org/libc v1.74.4 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)
