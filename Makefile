PARENT_DIR := $(dir $(abspath $(lastword $(MAKEFILE_LIST))))

include $(PARENT_DIR).parent/parent.mk

SERVICE_NAME := cuemenu
SOURCE.DIR   := .

# Release binaries of each program (name=package) for each platform, plus
# their checksums, in $(BIN.DIR).
DIST.TARGETS  := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64
DIST.PROGRAMS := cuemenu=. cuegen=./cmd/cuegen

.PHONY: dist
dist: clean
	@for program in $(DIST.PROGRAMS); do \
		name=$${program%%=*}; pkg=$${program#*=}; \
		for target in $(DIST.TARGETS); do \
			os=$${target%/*}; arch=$${target#*/}; ext=; \
			if [ "$$os" = windows ]; then ext=.exe; fi; \
			GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 $(MAKE) --no-print-directory build \
				SERVICE_NAME=$$name-$$os-$$arch$$ext SOURCE.DIR=$$pkg || exit 1; \
		done; \
	done
	@cd $(BIN.DIR) && sha256sum $(foreach program,$(DIST.PROGRAMS),$(firstword $(subst =, ,$(program)))-*) > checksums.txt
