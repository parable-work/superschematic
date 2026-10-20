// A standalone module so the Postgres driver stays out of the compiler's
// module graph. TestStackQueueOnALocalEnvironment in the cli package copies
// this directory and builds it, to make and read the database a local
// environment's queue lives in.
module example.com/superschematic/queuedb

go 1.26.4

require github.com/jackc/pgx/v5 v5.11.0

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	golang.org/x/text v0.29.0 // indirect
)
