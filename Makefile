BIN_DIR := $(HOME)/.clawflow/bin
BINARY  := $(BIN_DIR)/clawflow
SRC     := ./cmd/clawflow/
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X github.com/zhoushoujianwork/clawflow/cmd/clawflow/commands.Version=$(VERSION)"

.PHONY: install build release clean

# 构建并替换本地二进制
install:
	@mkdir -p $(BIN_DIR)
	go build $(LDFLAGS) -o $(BINARY) $(SRC)
	@echo "installed → $(BINARY) ($(VERSION))"

# 仅构建，不安装
build:
	go build $(LDFLAGS) -o clawflow $(SRC)
	@echo "built → ./clawflow ($(VERSION))"

# 构建所有平台发版二进制
release:
	GOOS=darwin  GOARCH=arm64 go build $(LDFLAGS) -o clawflow_darwin_arm64  $(SRC)
	GOOS=darwin  GOARCH=amd64 go build $(LDFLAGS) -o clawflow_darwin_amd64  $(SRC)
	GOOS=linux   GOARCH=amd64 go build $(LDFLAGS) -o clawflow_linux_amd64   $(SRC)
	@echo "release binaries built ($(VERSION))"

clean:
	rm -f clawflow clawflow_darwin_arm64 clawflow_darwin_amd64 clawflow_linux_amd64
