.PHONY: test vet fmt-check check

test:
	go test ./...

vet:
	go vet ./...

fmt-check:
	@files="$$(gofmt -l .)" || { echo "gofmt unavailable"; exit 1; }; \
	test -z "$$files" || { echo "gofmt required:"; echo "$$files"; exit 1; }

check:
	gofmt -w .
	$(MAKE) test
	$(MAKE) vet
	$(MAKE) fmt-check
