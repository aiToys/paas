SHELL := /bin/bash
.PHONY: build test lint tidy run clean

build: ## 编译 core 二进制到 bin/
	mkdir -p bin
	go build -o bin/core ./cmd/core

run: ## 本地运行 core
	go run ./cmd/core

test: ## 运行全部测试
	go test ./... -race -count=1

tidy: ## 整理依赖
	go mod tidy

lint: ## 运行 golangci-lint（需先安装）
	golangci-lint run ./...

clean: ## 清理构建产物
	rm -rf bin/ dist/ coverage.out coverage.html
