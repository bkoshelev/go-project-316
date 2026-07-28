build:
	go build -o bin/hexlet-go-crawler ./cmd/hexlet-go-crawler
test:
	go test ./...
run:
	bin/hexlet-go-crawler $(URL)
lint:
	golangci-lint run
lint-fix:
	golangci-lint run --fix
