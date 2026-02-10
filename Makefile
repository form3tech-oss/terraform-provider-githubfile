# Default values used by tests
GITHUB_EMAIL ?= foo@form3.tech
GITHUB_USERNAME ?= foo
TF_ACC ?= 1
COMMIT_MESSAGE_PREFIX ?= '[foo]'

default: vet test build

.PHONY: build
build:
	go build -o bin/terraform-provider-github-team-approver

.PHONY: vet
vet:
	go vet ./...

.PHONY: test
test:
	GITHUB_EMAIL="$(GITHUB_EMAIL)" \
	GITHUB_USERNAME="$(GITHUB_USERNAME)" \
	COMMIT_MESSAGE_PREFIX="$(COMMIT_MESSAGE_PREFIX)" \
	TF_ACC="$(TF_ACC)" \
	go test -count 1 -v ./...
