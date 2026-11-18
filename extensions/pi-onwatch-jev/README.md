# pi-onwatch-jev

Converged [Pi coding-agent](https://github.com/earendil-works/pi) extension combining:

- OpenRouter preset registration (`@preset/jev-routing` shell, `@preset/code-auto`, `@preset/code-nvidia`, `@preset/code-xai`, `@preset/code-google`, `@preset/code-chutes`, `@preset/openrouter`, plus transition `@preset/code-fast-free`) together with Pi's 14 vetted chat models (e.g. `nvidia/nemotron-3-ultra-550b-a55b:free`) and direct Pi-authenticated models
- Quota-aware routing via `onWatch` (`GET /api/route`, `POST /api/route/feedback`), with exact model IDs (e.g. `openrouter/nvidia/nemotron-3-ultra-550b-a55b:free` without `@preset` overlays for free pools) and `Jev Routing (onWatch)` excluding `$0` free pools/models (`exclude_free=true`)
- Single-request parallel classification (`tier`, `effort`, `<tier>_model`) via Jev SystemOne (`~typesafe/jev-latest`, reusing Pi's `~/.pi/agent/auth.json` OpenRouter key automatically)
- Sticky session pools with prompt-cache preservation, TTFT/error/429 feedback, `/bad`, `/reroute`, and `jev_pick`
- Deterministic Linear webhook intake fast-path (`webhook-task.md`) using `linear-t3-dispatcher`

See [docs/PI_EXTENSION.md](../../docs/PI_EXTENSION.md) for full architecture, API contract, configuration, and troubleshooting.

## Quick Start

```bash
# Preview installation changes
./install.sh --dry-run

# Symlink into ~/.pi/agent/extensions/pi-onwatch-jev and back up openrouter-presets.ts
./install.sh

# Run unit & integration tests (Node 24+)
node --test test/*.test.ts
```

## Files

| File | Responsibility |
|---|---|
| `index.ts` | Pi extension entrypoint, lifecycle hooks, `/bad`, `/reroute`, and `jev_pick` tool |
| `presets.ts` | OpenRouter preset registration, optional `pi-onwatch-jev.json` overrides, and Pi model resolution |
| `onwatch-client.ts` | `onWatch` port/auth discovery and mockable `/api/route` + `/api/route/feedback` client |
| `jev-client.ts` | OpenRouter SystemOne client (`~typesafe/jev-latest`) and parallel-question builder/parser |
| `router.ts` | Branch A sticky-pool routing, prompt-cache-preserving switches, TTFT/429 feedback, `/bad`, `/reroute` |
| `linear-intake.ts` | Branch B `webhook-task.md` detector, deterministic `linear-t3-dispatcher` orchestration |
| `types.ts` | Shared TypeScript interfaces for `onWatch`, Jev, session state, and CLI boundaries |
| `install.sh` | Idempotent symlink & legacy backup script (`--dry-run`, `--uninstall`) |
| `test/*.test.ts` | Node 24 test suite with mocked `OnWatchClient`, `JevClient`, and `DispatcherCli` |
