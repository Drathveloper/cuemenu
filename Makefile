PARENT_DIR := $(dir $(abspath $(lastword $(MAKEFILE_LIST))))

include $(PARENT_DIR).parent/parent.mk

SERVICE_NAME := cuemenu
SOURCE.DIR   := .
