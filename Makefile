.PHONY: build install test relay run clean

build:
	./scripts/build.sh

install: build
	cp dist/reminal /usr/local/bin/reminal

test:
	go test ./...

relay: build
	./dist/reminal relay

run: build
	REMINAL_RELAY=ws://localhost:8080/ws ./dist/reminal

clean:
	rm -rf dist/
