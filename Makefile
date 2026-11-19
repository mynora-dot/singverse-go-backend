SHELL := /bin/sh
.DEFAULT_GOAL := help
PYTHON ?= python3
GO_BACKEND_IMAGE ?= singverse:local
APP_COMPOSE = docker compose --project-directory . -f infra/docker/compose/compose.yaml
SOURCE_COMPOSE = $(APP_COMPOSE) -f infra/docker/compose/compose.build.yaml
LOCAL_COMPOSE = docker compose --env-file .env.local --project-directory . -f infra/docker/compose/compose.yaml -f infra/docker/compose/compose.local.yaml --profile compute
FRONTEND_ARGS = $(if $(FRONTEND_DIST),--frontend-dist "$(FRONTEND_DIST)",)
LOCK_ARGS = $(if $(COMPONENT_LOCK),--lock "$(COMPONENT_LOCK)",)
.PHONY: help go-tools-setup dbmate-setup go-backend-assets go-backend-build go-backend-test go-backend-run web-check db-fresh-check db-sqlite-check go-mysql-check db-mysql-check go-compute-check backend-check go-recovery-check platform-assets docker-build docker-up docker-down docker-pull docker-image-up docker-status docker-logs docker-check docker-config-check docker-context-check docker-test go-container-check auth-docker-check metadata-standard-check verify
.PHONY: docker-local-up docker-local-down docker-local-status docker-local-logs docker-local-releases
.PHONY: docker-macos-up
.PHONY: lan-discovery
.PHONY: qishui-check
lan-discovery:
	$(PYTHON) scripts/lan-discovery.py $(if $(DISCOVERY_CONTAINER),--container "$(DISCOVERY_CONTAINER)",)
qishui-check:
	sh scripts/run-go.sh run ./cmd/qishui-check -require-results $(if $(QUERY),-query "$(QUERY)",)
docker-macos-up:
	$(PYTHON) scripts/deploy-macos.py
docker-local-releases:
	$(PYTHON) scripts/prepare-local-releases.py $(if $(MOBILE_RELEASE_DIR),--mobile-release "$(MOBILE_RELEASE_DIR)",) $(if $(TV_RELEASE_DIR),--tv-release "$(TV_RELEASE_DIR)",)
docker-local-up:
	mkdir -p .build/local-releases
	$(LOCAL_COMPOSE) build singverse-go-backend singverse-compute
	$(LOCAL_COMPOSE) up -d --no-build --pull never --wait --wait-timeout 180
docker-local-down:
	$(LOCAL_COMPOSE) down
docker-local-status:
	$(LOCAL_COMPOSE) ps -a
docker-local-logs:
	$(LOCAL_COMPOSE) logs -f --tail 100
go-tools-setup:
	sh scripts/setup-go-tools.sh
dbmate-setup:
	sh scripts/setup-dbmate.sh
go-backend-assets:
	$(PYTHON) scripts/prepare-go-assets.py $(FRONTEND_ARGS)
go-backend-build: go-backend-assets
	sh scripts/build-go-backend.sh
go-backend-test: go-backend-assets dbmate-setup
	sh scripts/check-go-backend.sh
go-backend-run: go-backend-build dbmate-setup
	bin/singverse
web-check: go-backend-assets dbmate-setup
	sh scripts/run-go.sh test ./internal/web ./internal/app ./internal/clientrelease
db-fresh-check: dbmate-setup
	$(PYTHON) scripts/check-database-baseline.py
db-sqlite-check: go-backend-assets db-fresh-check
	sh scripts/run-go.sh test -race ./internal/platform/store ./internal/app -run 'SQLite|DeploymentLock|MigrationFailure'
go-mysql-check: go-backend-assets dbmate-setup
	$(PYTHON) infra/docker/check-go-mysql.py
db-mysql-check: go-mysql-check
go-compute-check: go-backend-assets dbmate-setup
	$(PYTHON) scripts/check-go-compute.py
backend-check: go-backend-test go-compute-check
go-recovery-check: go-backend-build dbmate-setup
	$(PYTHON) scripts/check-go-recovery.py
metadata-standard-check:
	sh scripts/run-go.sh test ./internal/metastandard ./internal/metastore
	sh scripts/run-go.sh run ./cmd/metadata-validate "$(CURDIR)/contracts/metadata/examples/song.valid.json"
platform-assets:
	$(PYTHON) scripts/prepare-platform.py $(LOCK_ARGS)
docker-build: platform-assets
	$(SOURCE_COMPOSE) build singverse-go-backend
docker-up: platform-assets
	$(SOURCE_COMPOSE) up -d --build --pull missing --wait --wait-timeout 180
docker-down:
	$(APP_COMPOSE) down
docker-pull:
	$(APP_COMPOSE) pull
docker-image-up:
	$(APP_COMPOSE) up -d --no-build --pull always --wait --wait-timeout 180
docker-status:
	$(APP_COMPOSE) ps
docker-logs:
	$(APP_COMPOSE) logs -f --tail 100
docker-check:
	$(PYTHON) infra/docker/check-docker-app.py
docker-config-check:
	$(PYTHON) infra/docker/check-config.py
docker-context-check:
	$(PYTHON) infra/docker/check-build-context.py
docker-test:
	$(PYTHON) -m unittest discover -s infra/docker/tests -v
	$(PYTHON) -m unittest discover -s scripts/tests -v
go-container-check:
	$(PYTHON) infra/docker/check-go-container.py --backend-image "$(GO_BACKEND_IMAGE)"
auth-docker-check:
	$(PYTHON) infra/docker/check-auth-docker.py --backend-image "$(GO_BACKEND_IMAGE)"
verify: go-backend-test web-check db-fresh-check docker-test docker-config-check
help:
	@printf '%s\n' 'Local Docker: copy .env.local.example to .env.local, then docker-local-up docker-local-status docker-local-logs docker-local-down' 'Local APKs: docker-local-releases MOBILE_RELEASE_DIR=/absolute/mobile-release TV_RELEASE_DIR=/absolute/tv-release (requires apksigner/aapt)'
	@printf '%s\n' 'Mac LAN discovery: lan-discovery (run on the Docker host in a separate terminal; Ctrl-C stops advertising)'
	@printf '%s\n' 'Go: go-tools-setup dbmate-setup go-backend-build go-backend-test go-backend-run' 'Local frontend: FRONTEND_DIST=/absolute/path/to/dist' 'Platform: platform-assets docker-build docker-up docker-pull docker-image-up docker-down' 'Retry fixed inputs: COMPONENT_LOCK=/absolute/path/components.lock.json' 'Checks: web-check db-fresh-check db-sqlite-check go-mysql-check docker-test docker-config-check docker-context-check' 'Explicit integration: SINGVERSE_COMPUTE_PROJECT=/absolute/path/to/prepared/checkout make go-compute-check'
