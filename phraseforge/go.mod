module phraseforge

go 1.26.5

require (
	github.com/dpurge/cli-tools v0.0.0-20260908190423-1c3adf26ea11
	github.com/go-chi/chi/v5 v5.3.2
	github.com/jackc/pgx/v5 v5.10.0
	golang.org/x/crypto v0.57.0
	k8s-lab/shared v0.0.0
)

replace k8s-lab/shared => ../shared

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/rogpeppe/go-internal v1.16.0 // indirect
	github.com/yuin/goldmark v1.8.4 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
