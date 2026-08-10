.PHONY: test vet fmt-check check

test:
	go test ./...

vet:
	go vet ./...

fmt-check:
	@test -z "$$(gofmt -l .)" || { echo "gofmt required"; gofmt -l .; exit 1; }

check:
	gofmt -w .
	$(MAKE) test
	$(MAKE) vet
	$(MAKE) fmt-check
