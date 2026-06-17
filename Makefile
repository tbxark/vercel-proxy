BUILD_DIR=./build
BUILD=$(shell git rev-parse --short HEAD)@$(shell date +%s)
LD_FLAGS=-ldflags "-s -w -X main.BuildVersion=$(BUILD)"
GO_BUILD=CGO_ENABLED=0 go build $(LD_FLAGS)

.PHONY: build
build:
	$(GO_BUILD) -o $(BUILD_DIR)/vercel-proxy .

.PHONY: buildLinuxX86
buildLinuxX86:
	GOOS=linux GOARCH=amd64 $(GO_BUILD) -o $(BUILD_DIR)/vercel-proxy .

.PHONY: buildImage
buildImage:
	docker buildx build --platform=linux/amd64,linux/arm64 -t ghcr.io/tbxark/vercel-proxy:latest . --push --provenance=false

.PHONY: run
run:
	go run main.go

.PHONY: deploy
deploy:
	vercel --prod
