.PHONY: lint test yaegi_test vendor clean

export GO111MODULE=on

default: test

lint:
	golangci-lint run

test:
	go test -race -cover ./...

yaegi_test:
	yaegi test -v .

vendor:
	go mod vendor

clean:
	rm -rf ./vendor
