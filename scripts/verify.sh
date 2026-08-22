#!/bin/sh
set -eu

printf 'Using: '
go version

format_diff=$(mktemp "${TMPDIR:-/tmp}/po-gofmt.XXXXXX")
trap 'rm -f "$format_diff"' EXIT HUP INT TERM

if ! find . -type f -name '*.go' -not -path './.git/*' -exec gofmt -d {} + >"$format_diff"; then
	cat "$format_diff"
	printf '%s\n' 'gofmt check failed' >&2
	exit 1
fi
if [ -s "$format_diff" ]; then
	cat "$format_diff"
	printf '%s\n' 'gofmt check failed' >&2
	exit 1
fi

printf '%s\n' '== go mod verify =='
go mod verify

printf '%s\n' '== go mod tidy -diff =='
go mod tidy -diff

printf '%s\n' '== go test ./... =='
go test -count=1 ./...

printf '%s\n' '== go test -race ./... =='
go test -race -count=1 ./...

printf '%s\n' '== go vet ./... =='
go vet ./...
