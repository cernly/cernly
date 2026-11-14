TAG ?= dev

.PHONY: worker
worker:
	@docker build -f backend/Dockerfile --target worker -t acme/worker:$(TAG) .
