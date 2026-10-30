BIN_DIR := $(HOME)/.clawflow/bin
BINARY  := $(BIN_DIR)/clawflow
SRC     := ./cmd/clawflow/
VERSION := $(shell git describe --tags --always 2>/dev/null || echo "dev")
COMMIT := $(shell git rev-parse HEAD)
BRANCH := $(shell git symbolic-ref --short -q HEAD || echo main)
CHANNEL := source
LDFLAGS = -ldflags "-s -w -X github.com/zhoushoujianwork/clawflow/cmd/clawflow/commands.Version=$(VERSION) -X github.com/zhoushoujianwork/clawflow/cmd/clawflow/commands.BuildCommit=$(COMMIT) -X github.com/zhoushoujianwork/clawflow/cmd/clawflow/commands.BuildChannel=$(CHANNEL) -X github.com/zhoushoujianwork/clawflow/cmd/clawflow/commands.SourceBranch=$(BRANCH)"

.PHONY: install build release clean test fmt vet tidy dev web

# 构建前端
web:
	@cd web && pnpm build
	@echo "web built"

# run web

run: install
	clawflow web restart

# Build and replace the local CLI without a frontend toolchain.
install:
	@mkdir -p $(BIN_DIR)
	go build $(LDFLAGS) -o $(BINARY) $(SRC)
	@echo "installed → $(BINARY) ($(VERSION))"

# Build without installing or starting any service.
build:
	go build $(LDFLAGS) -o clawflow $(SRC)
	@echo "built → ./clawflow ($(VERSION))"

# tidy + build，开发日常用
dev: tidy install

# Build release CLIs. The native app is built on macOS with `make native`.
release: CHANNEL = release
release:
	GOOS=darwin  GOARCH=arm64 go build $(LDFLAGS) -o clawflow_darwin_arm64  $(SRC)
	GOOS=darwin  GOARCH=amd64 go build $(LDFLAGS) -o clawflow_darwin_amd64  $(SRC)
	GOOS=linux   GOARCH=amd64 go build $(LDFLAGS) -o clawflow_linux_amd64   $(SRC)
	@echo "release binaries built ($(VERSION))"

# 运行测试
test:
	go test -v -race ./...

# 格式化
fmt:
	gofmt -s -w .

# go vet
vet:
	go vet ./...

# 整理依赖
tidy:
	go mod tidy

clean:
	rm -f clawflow clawflow_darwin_arm64 clawflow_darwin_amd64 clawflow_linux_amd64

.PHONY: native native-test legacy-web
native: build
	./macos/build-app.sh

native-test:
	swift test --package-path macos

legacy-web: web
	go build -tags legacyweb $(LDFLAGS) -o clawflow $(SRC)
