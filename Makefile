MAGE_VERSION ?= v1.17.0
MAGE_RUN = go run github.com/magefile/mage@$(MAGE_VERSION)

DOCKER_IMAGE ?= skeeper
DOCKER_TAG ?= dev

.PHONY: deps fmt lint modernize test test-integration cover build install verify tools \
        bun-lint bun-fmt bun-fmt-check hooks-install release-snapshot docker-build help

deps:
	@$(MAGE_RUN) deps

fmt:
	@$(MAGE_RUN) fmt

lint:
	@$(MAGE_RUN) lint

modernize:
	@$(MAGE_RUN) modernize

test:
	@$(MAGE_RUN) test

test-integration:
	@$(MAGE_RUN) testIntegration

cover:
	@$(MAGE_RUN) cover

build:
	@$(MAGE_RUN) build

install: build
	@$(MAGE_RUN) install

verify:
	@$(MAGE_RUN) verify

tools:
	@$(MAGE_RUN) tools

bun-lint:
	@$(MAGE_RUN) bunLint

bun-fmt:
	@$(MAGE_RUN) bunFormat

bun-fmt-check:
	@$(MAGE_RUN) bunFormatCheck

hooks-install:
	@$(MAGE_RUN) hooksInstall

release-snapshot:
	@$(MAGE_RUN) releaseSnapshot

docker-build:
	docker build -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

help:
	@$(MAGE_RUN) -l
