# The tunnel's cookies and same-site requests, on the real Worker
# (wrangler dev, local) with a real `reminal expose` (run.sh).
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
# go.mod's replace names a copy in the tree (third_party/ultraviolet): its
# go.mod is what `go mod download` reads of it.
COPY third_party/ultraviolet/go.mod third_party/ultraviolet/go.mod
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOFLAGS=-buildvcs=false go build -o /out/reminal ./cmd/reminal

FROM node:22-bookworm
RUN apt-get update && apt-get install -y --no-install-recommends python3 curl && rm -rf /var/lib/apt/lists/*
COPY cloudflare/package.json cloudflare/package-lock.json /w/cloudflare/
RUN cd /w/cloudflare && npm ci --silent
COPY cloudflare /w/cloudflare
# The viewer's source: wrangler builds the Worker's page from it (wrangler.toml [build]).
COPY internal/client/web /w/internal/client/web
COPY --from=build /out/reminal /usr/local/bin/reminal
COPY scripts/tunnel-cookie-test/backend.py scripts/tunnel-cookie-test/test.sh /t/
CMD ["sh", "/t/test.sh"]
