module github.com/blerglab/blerg-ai/runner

go 1.26.0

require (
	github.com/SherClockHolmes/webpush-go v1.4.0
	github.com/blerglab/blerg-ai/contracts v0.0.0
	github.com/creack/pty v1.1.24
	github.com/gorilla/websocket v1.5.3
	github.com/jackc/pgx/v5 v5.11.0
)

replace github.com/blerglab/blerg-ai/contracts => ../contracts

require (
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/text v0.42.0 // indirect
)
