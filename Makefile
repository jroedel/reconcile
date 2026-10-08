# Every target is here so that there is one command per purpose and no habit of
# piping one through a filter written in a hurry. `make help` lists them.

APP  := reconcile
MAIN := ./cmd/reconcile
GO   ?= go

.DEFAULT_GOAL := help

.PHONY: help
help: ## List every target
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[1m%-16s\033[0m %s\n", $$1, $$2}'

# ---------------------------------------------------------------- navigation

.PHONY: sym
sym: ## Where is a Go symbol, and what is its declaration? (make sym NAME=Serve)
	@if [ -z "$(NAME)" ]; then echo "usage: make sym NAME=Serve" >&2; exit 2; fi
	@scripts/sym "$(NAME)"

.PHONY: outline
outline: ## Every symbol in one file, with line numbers (make outline FILE=path/to/x.go)
	@if [ -z "$(FILE)" ]; then echo "usage: make outline FILE=path/to/x.go" >&2; exit 2; fi
	@gopls symbols "$(FILE)" | awk '{printf "%s:%s\n", "$(FILE)", $$0}'

.PHONY: dev-tools
dev-tools: ## Install the Go tooling this Makefile navigates and checks with
	$(GO) install golang.org/x/tools/gopls@latest
	$(GO) install honnef.co/go/tools/cmd/staticcheck@2026.1

# ---------------------------------------------------------------- the checks

# One package rather than ./..., deliberately. The module is new and clean, so
# the bound costs nothing today; it is here because the day staticcheck stops
# being clean, the rule that makes a new finding in your own code the only line
# you see is already the habit.
.PHONY: go-check
go-check: ## Format, vet, staticcheck, build and test one package (make go-check PKG=./cmd/reconcile)
	@if [ -z "$(PKG)" ]; then echo "usage: make go-check PKG=./cmd/reconcile" >&2; exit 2; fi
	@gofmt -s -w $(PKG)
	@$(GO) vet $(PKG)/...
	@if command -v staticcheck >/dev/null 2>&1; then \
		staticcheck $(PKG)/...; \
	else \
		echo "staticcheck is not installed; run make dev-tools" >&2; \
	fi
	@$(GO) build ./...
	@GO=$(GO) scripts/go-test $(PKG)/...

.PHONY: vet
vet: ## go vet the whole module
	@$(GO) vet ./...

.PHONY: fmt
fmt: ## Format all Go sources in place
	@$(GO) fmt ./...

.PHONY: fmt-check
fmt-check: ## Fail, naming the files, if anything is not gofmt-clean
	@out=$$(gofmt -s -l .); \
	if [ -n "$$out" ]; then echo "not gofmt-clean:"; echo "$$out"; exit 1; fi

.PHONY: lint
lint: vet fmt-check data-guard ## vet + gofmt check + no statements or receipts tracked

.PHONY: data-guard
data-guard: ## Fail if a tracked file looks like a statement, a receipt, a database or a credential
	@scripts/data-guard tracked

.PHONY: hooks
hooks: ## Install the pre-commit guard that keeps statements and account numbers out of git
	@ln -sf ../../scripts/data-guard-hook .git/hooks/pre-commit
	@echo "installed .git/hooks/pre-commit"

.PHONY: vuln-check
vuln-check: ## Check dependencies against the Go vulnerability database (needs network)
	@$(GO) tool govulncheck ./...

.PHONY: test-unit
test-unit: ## Run unit tests, with the race detector
	@GO=$(GO) scripts/go-test -race ./...

.PHONY: test
test: test-unit lint shell-test ## Full check: unit tests + lint + shell tests
	@echo "vuln-check needs the network and is run separately by CI"

.PHONY: cover
cover: ## Unit tests with a coverage summary
	@$(GO) test -cover ./...

.PHONY: tidy
tidy: ## Tidy go.mod
	@$(GO) mod tidy

# ---------------------------------------------------------------- build & run

.PHONY: build
build: ## Build the binary for this machine
	@$(GO) build -o $(APP) $(MAIN)

.PHONY: run
run: build ## Build and run against ./config.toml
	@if [ ! -f config.toml ]; then \
		echo "there is no config.toml; cp config.example.toml config.toml and edit it" >&2; \
		exit 1; \
	fi
	@./$(APP) -config config.toml

# The server has no Go toolchain and receives one file, so a build that quietly
# links this machine's glibc is a deploy that dies on the host with
# "GLIBC_2.xx not found". CGO stays off (modernc.org/sqlite is pure Go, for
# the day there is a database), and the assertion below is what makes that a fact rather than an intention.
.PHONY: release
release: ## Build the static linux/amd64 binary the server runs
	@CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
		$(GO) build -trimpath -ldflags='-s -w' -o $(APP)-linux-amd64 $(MAIN)
	@file $(APP)-linux-amd64 | grep -q 'statically linked' \
		|| { echo "$(APP)-linux-amd64 is not statically linked" >&2; exit 1; }
	@echo "$(APP)-linux-amd64 is static"

# ---------------------------------------------------------------- the server
#
# Everything below reads secrets.env, which is not in the repository. It lives
# in Bitwarden; see secrets.env.example for what goes in it and where each group
# is sent.
#
# These are for a person at a terminal. .claude/settings.json denies make
# deploy* and make prod-* to agents, which is why every target that touches
# GitHub's secrets or the server is named deploy-something. See CLAUDE.md §6.

.PHONY: deploy-status
deploy-status: ## Ready to deploy? secrets.env, GitHub, the server, DNS, TLS, the pipeline
	@scripts/secrets status

.PHONY: deploy-keygen
deploy-keygen: ## Mint this project's deploy ssh key, and print how to install it
	@scripts/secrets ssh-keygen

.PHONY: deploy-public-key
deploy-public-key: ## Print the public half of the deploy key in secrets.env, and how to install it
	@scripts/secrets public-key

.PHONY: bootstrap-secret
bootstrap-secret: ## Print a fresh BOOTSTRAP_SIGNIN_SECRET to paste into secrets.env
	@scripts/secrets bootstrap-secret

.PHONY: deploy-known-hosts
deploy-known-hosts: ## Pin the server's host key (paste the line into secrets.env)
	@scripts/secrets known-hosts

.PHONY: deploy-htaccess
deploy-htaccess: ## Install the Apache front end now, and check it from outside (CI does this on every push)
	@deploy/deploy.sh htaccess

.PHONY: deploy
deploy: ## Deploy from this machine. The ordinary path is a push to main
	@deploy/deploy.sh deploy

.PHONY: prod-status
prod-status: ## Is the live app well? Process, public checks, what is live, backups, log
	@deploy/deploy.sh status

.PHONY: prod-logs
prod-logs: ## The tail of the server's log (make prod-logs N=200)
	@deploy/deploy.sh logs $(or $(N),80)

.PHONY: prod-backup
prod-backup: ## Back up the live database now, once there is one (stops the app for a moment)
	@deploy/deploy.sh backup

.PHONY: prod-restart
prod-restart: ## Restart the live app
	@deploy/deploy.sh restart

.PHONY: deploy-send-secrets
deploy-send-secrets: ## Deploy key and addresses to GitHub; config.toml to the server
	@scripts/secrets push
	@scripts/secrets install

# ---------------------------------------------------------------- local dev

.PHONY: shell-test
shell-test: ## Run the shell tests
	@for t in scripts/*-test.sh; do \
		[ -f "$$t" ] || continue; \
		echo "--- $$t"; \
		bash "$$t" || exit 1; \
	done
