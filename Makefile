.PHONY: all build flatc

all: flatc build

flatc:
	flatc --go --grpc -o pkg/ fbs/mindb.fbs

build:
	go build -o bin/mindb-server cmd/mindb-server/main.go
