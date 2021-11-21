GOCMD=go
GOBUILD=$(GOCMD) build
export PATH := "${CURDIR}/bin:$(PATH)"

bin/bindown:
	curl -sfLO https://github.com/WillAbides/bindown/releases/download/v3.5.1/bootstrap-bindown.sh
	sh bootstrap-bindown.sh
	rm bootstrap-bindown.sh

bin/golangci-lint: bin/bindown
	bin/bindown install $(notdir $@)

bin/shellcheck: bin/bindown
	bin/bindown install $(notdir $@)

bin/gofumpt: bin/bindown
	bin/bindown install $(notdir $@)

bin/protoc: bin/bindown
	bin/bindown install $(notdir $@)

bin/protoc-gen-go: bin/bindown
	bin/bindown install $(notdir $@)

bin/goyacc:
	GOBIN=${CURDIR}/bin \
	go install golang.org/x/tools/cmd/goyacc@v0.1.7

HANDCRAFTED_REV := 082e94edadf89c33db0afb48889c8419a2cb46a9
bin/handcrafted:
	GOBIN=${CURDIR}/bin \
	go install github.com/willabides/handcrafted@$(HANDCRAFTED_REV)

