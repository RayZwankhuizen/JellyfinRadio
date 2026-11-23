GO_CACHE ?= /tmp/jellyfin-radio-go-cache
COMPOSE_DEV = docker compose --env-file .env -f deploy/docker-compose.dev.yml
.PHONY: build test lint start dev dev-down docker-build release deploy-check deploy

build:
	mkdir -p backend/bin
	cd backend && GOCACHE=$(GO_CACHE) go build -trimpath -o bin/smart-radio ./cmd/smart-radio
	cd audio && .venv/bin/python -m compileall -q app

test:
	cd backend && GOCACHE=$(GO_CACHE) go test ./...
	docker compose -f deploy/docker-compose.dev.yml build smart-radio-audio
	docker run --rm --cpus=0.5 --memory=768m \
		--mount type=bind,source=$(CURDIR)/audio/tests,target=/tests,readonly \
		--mount type=bind,source=$(CURDIR)/scripts,target=/scripts,readonly \
		--env AUDIO_EVALUATOR=/scripts/evaluate-audio.py \
		jellyfin-smart-radio-audio:dev python -m unittest discover -s /tests -v

lint:
	cd backend && GOCACHE=$(GO_CACHE) go vet ./...
	python3 -m py_compile audio/app/*.py audio/tests/*.py
	python3 -m py_compile scripts/test-audio-container.py
	node --check web/app.js
	bash -n scripts/*.sh

start dev:
	bash scripts/start-dev.sh

dev-down:
	$(COMPOSE_DEV) down

docker-build:
	$(COMPOSE_DEV) build

release: build test lint
	mkdir -p dist
	tar -czf dist/smart-radio-release.tar.gz --exclude=.git --exclude=.ijfw --exclude=.agents --exclude=.codex --exclude=.aws --exclude=ijfw --exclude='*/.venv' --exclude='*/__pycache__' --exclude=backend/bin --exclude=data --exclude=playlists --exclude=dist --exclude=.release.env --exclude=./.env --exclude=./deploy/.env .

deploy-check:
	bash scripts/deploy-check.sh

deploy:
	bash scripts/deploy.sh
