// A standalone module so the Postgres driver stays out of the compiler's
// module graph. TestProjectionMigrationsOnPostgres in the sqlgen package
// copies this directory, points it at generated SQL, and runs its test.
module example.com/superschematic/pgcheck

go 1.26.4

require github.com/jackc/pgx/v5 v5.11.0

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	golang.org/x/text v0.29.0 // indirect
)
