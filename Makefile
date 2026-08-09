TAGS ?= with_quic with_utls

.PHONY: build test vet fmt e2e clean

build:
	go build -tags "$(TAGS)" -o proxypool ./cmd/proxypool

test:
	go test -tags "$(TAGS)" ./...

vet:
	go vet -tags "$(TAGS)" ./...

fmt:
	gofmt -w .

e2e:
	bash scripts/e2e.sh config.yaml

clean:
	go clean
