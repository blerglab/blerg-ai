module github.com/blerglab/blerg-ai/core

go 1.25.14

require (
	github.com/blerglab/blerg-ai/contracts v0.0.0-00010101000000-000000000000
	github.com/coreos/go-oidc/v3 v3.17.0
	github.com/jackc/pgx/v5 v5.10.0
	golang.org/x/crypto v0.55.0
	golang.org/x/oauth2 v0.30.0
)

require (
	github.com/go-jose/go-jose/v4 v4.1.4 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/text v0.41.0 // indirect
)

replace github.com/blerglab/blerg-ai/contracts => ../contracts
