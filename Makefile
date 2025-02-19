#!/usr/bin/make -f

PWD=$(shell pwd)

build:
	go build ./cmd/nova

install:
	go install ./cmd/nova