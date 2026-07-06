test: gen
    go mod tidy
    go install github.com/fritzkeyzer/islandc/cmd/islandc
    go fmt ./...
    go test ./...
    go vet ./...
    islandc --resolve-deps testdata # runs the CLI
    go build github.com/fritzkeyzer/islandc/testdata # check that CLI output builds

gen:
    cp README.md internal/docs/README.md
    cp ISLAND_FLAVOURED_HTML.md internal/docs/ISLAND_FLAVOURED_HTML.md
    cp version.json internal/docs/version.json
