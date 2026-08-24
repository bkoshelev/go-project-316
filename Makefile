build:
	go build -o bin/hexlet-go-crawler ./cmd/hexlet-go-crawler
test:
	go test ./... --race --count=1
# запустить конкретный тест:
# go test -race -run 'TestCrawler_WithDelay' -count=1 ./crawler -v
run:
	bin/hexlet-go-crawler $(URL)
lint:
	golangci-lint run
lint-fix:
	golangci-lint run --fix
