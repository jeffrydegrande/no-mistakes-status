.PHONY: build test lint install run

build:
	go build -o ./bin/nms ./cmd/nms

test:
	go test -race -count=1 ./...

lint:
	gofmt -l .
	go vet ./...

install:
	go install ./cmd/nms

run: build
	./bin/nms
