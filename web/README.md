# kouja admin UI

Preact + TypeScript + Vite frontend for the local kouja admin listener.

```sh
npm ci
npm run dev      # proxy /api to http://127.0.0.1:9444
npm run build    # typecheck and build into ../internal/adminui/assets
npm run typecheck
```

`make build` builds this UI and then the Go binary. `make build-go` builds the
Go binary without Node and serves the fallback page.
