default: vet test build

.PHONY: build
build:
	go build -o bin/terraform-provider-github-team-approver

.PHONY: vet
vet:
	go vet ./...

.PHONY: test
test:
	go test -count 1 -v ./...
