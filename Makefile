SHELL := /bin/bash

DOMAIN ?= agentmailguard.dev
WORKER ?= agentmailguard
DB ?= agentmail
REMOTE_URL ?= https://agentmailguard.ivanrvpereira.workers.dev
LIMIT ?= 10
ID ?=
QUERY ?=
SENDER ?=
RISK ?=

.PHONY: help install dev typecheck validate migrate-local migrate-remote deploy tail health mcp-auth-check db-recent db-latest db-entities db-failed-vectors db-search db-get

help:
	@echo "AgentMailGuard commands"
	@echo ""
	@echo "Setup/dev:"
	@echo "  make install          npm install"
	@echo "  make dev              run local Worker"
	@echo "  make typecheck        TypeScript check"
	@echo "  make validate         wrangler deploy --dry-run"
	@echo ""
	@echo "Cloudflare:"
	@echo "  make migrate-local    apply D1 migrations locally"
	@echo "  make migrate-remote   apply D1 migrations remotely"
	@echo "  make deploy           deploy Worker"
	@echo "  make tail             tail remote Worker logs"
	@echo ""
	@echo "Demo checks:"
	@echo "  make health           hit deployed health endpoint"
	@echo "  make mcp-auth-check   verify MCP auth using .env"
	@echo "  make db-recent        show recent emails; LIMIT=10"
	@echo "  make db-latest        show latest email details"
	@echo "  make db-entities      show latest extracted entities"
	@echo "  make db-failed-vectors show vector indexing failures"
	@echo "  make db-search QUERY=receipt"
	@echo "  make db-get ID=<email-id>"

install:
	npm install

dev:
	npx wrangler dev --env-file .env

typecheck:
	npm run typecheck

validate:
	npx wrangler deploy --dry-run

migrate-local:
	npm run db:migrate:local

migrate-remote:
	npm run db:migrate:remote

deploy:
	npm run deploy

tail:
	npx wrangler tail $(WORKER)

health:
	curl -sS $(REMOTE_URL) | jq .

mcp-auth-check:
	@set -euo pipefail; \
	if [ ! -f .env ]; then echo ".env not found" >&2; exit 1; fi; \
	set -a; source ./.env; set +a; \
	if [ -z "$${MCP_SHARED_SECRET:-}" ]; then echo "MCP_SHARED_SECRET missing in .env" >&2; exit 1; fi; \
	echo "without auth:"; \
	curl -sS -o /tmp/agentmailguard-mcp-unauth.out -w "%{http_code}\n" $(REMOTE_URL)/mcp; \
	cat /tmp/agentmailguard-mcp-unauth.out; echo; \
	echo "with auth:"; \
	curl -sS --max-time 5 -o /tmp/agentmailguard-mcp-auth.out -w "%{http_code}\n" \
	  -H "Authorization: Bearer $${MCP_SHARED_SECRET}" \
	  -H "Accept: application/json, text/event-stream" \
	  $(REMOTE_URL)/mcp || true; \
	cat /tmp/agentmailguard-mcp-auth.out; echo

db-recent:
	npx wrangler d1 execute $(DB) --remote --command "SELECT id, received_at, sender_addr, subject, risk_level, labels, vector_status FROM emails ORDER BY received_at DESC LIMIT $(LIMIT);"

db-latest:
	npx wrangler d1 execute $(DB) --remote --command "SELECT id, received_at, sender_addr, sender_name, subject, risk_level, risk_reasons, labels, threat_flags, vector_status, vector_error FROM emails ORDER BY received_at DESC LIMIT 1;"

db-entities:
	npx wrangler d1 execute $(DB) --remote --command "SELECT e.email_id, e.entity_type, e.value, e.source_field FROM entities e JOIN emails m ON m.id = e.email_id ORDER BY m.received_at DESC LIMIT $(LIMIT);"

db-failed-vectors:
	npx wrangler d1 execute $(DB) --remote --command "SELECT id, received_at, subject, vector_error FROM emails WHERE vector_status = 'failed' ORDER BY received_at DESC LIMIT $(LIMIT);"

db-search:
	@test -n "$(QUERY)" || (echo "Usage: make db-search QUERY=receipt" >&2; exit 1)
	npx wrangler d1 execute $(DB) --remote --command "SELECT id, received_at, sender_addr, subject, risk_level FROM emails WHERE subject LIKE '%' || '$(QUERY)' || '%' OR cleaned_body LIKE '%' || '$(QUERY)' || '%' ORDER BY received_at DESC LIMIT $(LIMIT);"

db-get:
	@test -n "$(ID)" || (echo "Usage: make db-get ID=<email-id>" >&2; exit 1)
	npx wrangler d1 execute $(DB) --remote --command "SELECT id, received_at, sender_addr, subject, risk_level, risk_reasons, labels, threat_flags, cleaned_body FROM emails WHERE id = '$(ID)';"
