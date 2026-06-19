# reminal relay (Cloudflare Workers)

Free hosted relay for reminal. Uses Cloudflare Workers + Durable Objects for WebSocket session pairing.

## Deploy (one time, free)

Requires a Cloudflare **free** account. Durable Objects use the SQLite backend (`new_sqlite_classes`), which is required on the free plan.

```bash
cd cloudflare
npm install
npx wrangler login   # or: export CLOUDFLARE_API_TOKEN=...
npm run deploy
```

After deploy, wrangler prints your URL, e.g. `https://reminal-relay.<account>.workers.dev`.

Update the default in `internal/config/config.go`:

```go
DefaultCloudRelay = "wss://reminal-relay.<account>.workers.dev/ws"
DefaultCloudWeb   = "https://reminal-relay.<account>.workers.dev"
```

Or set at build time:

```bash
go build -ldflags "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://your-url/ws -X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://your-url" -o reminal ./cmd/reminal
```

Optional: add a custom domain in the Cloudflare dashboard (free).

## Local dev

```bash
npm run dev
# In another terminal:
REMINAL_RELAY=ws://localhost:8787/ws REMINAL_WEB=http://localhost:8787 reminal
```

## Cost

Cloudflare Workers free tier includes 100,000 requests/day and Durable Objects with generous limits — more than enough for personal use.
