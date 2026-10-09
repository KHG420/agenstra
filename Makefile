.PHONY: check fmt-check lint test build

check: fmt-check lint test build

fmt-check:
	@format_files="$$(git ls-files -z --cached --others --exclude-standard -- '*.go' | xargs -0 gofmt -l)" || exit $$?; if [ -n "$$format_files" ]; then printf '%s\n' "$$format_files"; exit 1; fi
	git diff --check

lint:
	go vet ./...
	golangci-lint run
	npm run lint --prefix sdk/web

test:
	go test -race ./...
	npm test --prefix sdk/web

build:
	go build ./cmd/...
