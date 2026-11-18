PARENT_DIR := $(dir $(abspath $(lastword $(MAKEFILE_LIST))))

include $(PARENT_DIR).parent/parent.mk

SERVICE_NAME := cuemenu
SOURCE.DIR   := .

# Release binaries for each platform, plus their checksums, in $(BIN.DIR).
DIST.TARGETS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

.PHONY: dist
dist: clean
	@for target in $(DIST.TARGETS); do \
		os=$${target%/*}; arch=$${target#*/}; ext=; \
		if [ "$$os" = windows ]; then ext=.exe; fi; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 $(MAKE) --no-print-directory build \
			SERVICE_NAME=$(SERVICE_NAME)-$$os-$$arch$$ext || exit 1; \
	done
	@cd $(BIN.DIR) && sha256sum $(SERVICE_NAME)-* > checksums.txt
