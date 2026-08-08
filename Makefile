SHELL := /bin/bash
.PHONY: build run test test-pg migrate cover fmt vet lint tidy clean dev manifests airsync

build: ## 编译 core 二进制到 bin/
	mkdir -p bin
	go build -o bin/core ./cmd/core

run: ## 本地运行 core
	go run ./cmd/core

dev: ## 本地开发：编译并运行（暴露 :8080）
	mkdir -p bin
	go build -o bin/core ./cmd/core
	./bin/core

test: ## 运行全部测试（内存后端，含竞态检测，零外部依赖）
	go test ./... -race -count=1

openapi: ## 启动 core 拉起 /openapi.json，生成前端 TS 类型（pnpm gen:api）
	go build -o bin/core ./cmd/core
	./bin/core & echo $$! > /tmp/paas-core.pid
	@until curl -sf http://localhost:8080/livez >/dev/null 2>&1; do sleep 0.3; done
	cd frontend/console-user && pnpm gen:api
	@kill $$(cat /tmp/paas-core.pid) 2>/dev/null; rm -f /tmp/paas-core.pid
	@echo "已生成 frontend/console-user/src/api/types.gen.ts"

PG_DSN ?= postgres://paas:paas-dev@localhost:5432/paas?sslmode=disable

test-pg: ## 运行 PostgreSQL 集成测试（需先 docker compose up postgres，或可用 PG）
	docker compose up -d postgres
	@echo "等待 postgres 就绪…"
	@until docker compose exec -T postgres pg_isready -U paas >/dev/null 2>&1; do sleep 1; done
	@echo "注：各 pg 包 resetSchema 共享同一 database，必须 -p 1 串行跑避免互清"
	PAAS_TEST_PG_URL=$(PG_DSN) go test -tags=integration -p 1 -count=1 \
	  ./internal/core/identity/pg/ ./internal/core/application/pg/ \
	  ./internal/environment/pg/ ./internal/appconfig/pg/ ./internal/dataservice/pg/ \
	  ./internal/workload/pg/ ./internal/devops/pg/ ./internal/devops/pipeline/ \
	  ./internal/governance/pg/ ./internal/configcenter/pg/ ./internal/billing/pg/ \
	  ./internal/security/pg/ ./internal/maas/pg/

migrate: ## 拉起本地 PG（迁移在 core 启动时自动跑：PAAS_DB_URL 非空即触发）
	docker compose up -d postgres
	@until docker compose exec -T postgres pg_isready -U paas >/dev/null 2>&1; do sleep 1; done
	@echo "PG 就绪：$(PG_DSN)"
	@echo "启动 core 即自动迁移 + seed：PAAS_DB_URL=$(PG_DSN) make run"

cover: ## 生成测试覆盖率报告（HTML）
	go test ./... -race -count=1 -coverprofile=coverage.out
	go tool cover -html=coverage.out -o coverage.html
	@echo "覆盖率报告已生成: coverage.html"

fmt: ## 格式化代码（gofmt）
	gofmt -s -w .

vet: ## 运行 go vet
	go vet ./...

tidy: ## 整理依赖
	go mod tidy

lint: ## 运行 golangci-lint（需先安装）
	golangci-lint run ./...

clean: ## 清理构建产物
	rm -rf bin/ dist/ coverage.out coverage.html

manifests: ## 生成 Workload + DataService CRD + deepcopy（需 controller-gen: go install sigs.k8s.io/controller-tools/cmd/controller-gen@latest）
	$(shell go env GOPATH)/bin/controller-gen object paths=./api/core/v1alpha1
	$(shell go env GOPATH)/bin/controller-gen crd paths=./api/core/v1alpha1 output:crd:artifacts:config=config/crds

airsync: ## 编译 airsync 离线交付工具到 bin/
	mkdir -p bin
	go build -o bin/airsync ./cmd/airsync

test-envtest: ## 运行 K8s controller envtest 集成测试（需 setup-envtest 装的 KUBEBUILDER_ASSETS）
	@command -v setup-envtest >/dev/null 2>&1 || { echo "请先: go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest"; exit 1; }
	KUBEBUILDER_ASSETS=$$(setup-envtest use latest -p path) go test -tags=integration ./internal/controller/ -count=1
