module github.com/blerglab/blerg-ai/board

go 1.25.14

require (
	github.com/anthropics/anthropic-sdk-go v1.75.0
	github.com/blerglab/blerg-ai/contracts v0.0.0
	github.com/gorilla/websocket v1.5.3
	github.com/jackc/pgx/v5 v5.11.0
)

replace github.com/blerglab/blerg-ai/contracts => ../contracts

require (
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.1.2 // indirect
	github.com/invopop/jsonschema v0.14.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/pb33f/ordered-map/v2 v2.3.1 // indirect
	github.com/standard-webhooks/standard-webhooks/libraries v0.0.1 // indirect
	github.com/tidwall/gjson v1.18.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	go.yaml.in/yaml/v4 v4.0.0-rc.2 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/text v0.41.0 // indirect
)
