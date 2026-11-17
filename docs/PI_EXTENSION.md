# Pi Extension (`pi-onwatch-jev`)

The `pi-onwatch-jev` Pi coding-agent extension converges OpenRouter preset registration, quota-aware model/pool routing via `onWatch`, prompt classification via Jev (`~typesafe/jev-latest` on OpenRouter SystemOne), and deterministic Linear webhook intake dispatching in a single extension.

Source lives in [`extensions/pi-onwatch-jev`](../extensions/pi-onwatch-jev).

## Overview

`pi-onwatch-jev` replaces the standalone `openrouter-presets.ts` extension and adds two `before_agent_start` routing branches:

- **Branch A — Normal Pi Sessions & Pi Work Threads**: Queries `onWatch` (`GET /api/route?harnesses=pi`) with a sticky session pool, classifies every prompt via Jev SystemOne (`~typesafe/jev-latest`), and calls `pi.setModel()` / `pi.setThinkingLevel()` **only** when the resolved model, tier, capability, or pool changes (preserving prompt caches across turns). Reports TTFT, total latency, HTTP 429s, errors, and manual `/bad` demotions back to `POST /api/route/feedback`.
- **Branch B — Linear Intake Sessions** (detected by the `webhook-task.md` prompt prefix): Keeps the intake thread pinned on its `$0` free/fast model (never calls `pi.setModel()` or `pi.setThinkingLevel()`), handles deterministic `linear-t3-dispatcher` flows (`stop`, existing `followup`, `project`-missing, and `folder`-elicitation) with **0 Jev calls**, and for bound projects queries `onWatch` across `pi,cursor,antigravity,grok,claudeAgent,codex` (never `opencode`), calls Jev once on the issue title/description, starts the work thread, settles the intake, disables tools, and finishes in one trivial no-tool turn.

## Requirements

- [Pi coding agent](https://github.com/earendil-works/pi) (`1.5.x+`)
- `onWatch` daemon running locally (or reachable via `ONWATCH_URL`) with `/api/route` and `/api/route/feedback`
- OpenRouter credentials configured in Pi (`~/.pi/agent/auth.json` via `/login openrouter` or `OPENROUTER_API_KEY`)
- Optional for Branch B: `linear-t3-dispatcher` on `PATH`

## Authentication & API Keys (No Duplicate Key Needed)

**You do not need to save your OpenRouter API key in a second place.**

For Jev SystemOne requests (`POST https://openrouter.ai/api/v1/systemone` with `model: "~typesafe/jev-latest"`), `pi-onwatch-jev` resolves credentials in this order:

1. `OPENROUTER_API_KEY` environment variable (if set)
2. Pi's existing OpenRouter credential via `ctx.modelRegistry.getApiKeyForProvider("openrouter")`, which reads `~/.pi/agent/auth.json` automatically

Direct Pi-logged-in providers returned by `onWatch` (such as `anthropic`, `openai-codex`, `google-antigravity`, etc.) also reuse Pi's existing authentication in `~/.pi/agent/auth.json`.

For `onWatch` endpoints protected by Basic Auth, `pi-onwatch-jev` checks:

1. `ONWATCH_USERNAME` / `ONWATCH_PASSWORD` (or `ONWATCH_ADMIN_USER` / `ONWATCH_ADMIN_PASS`) in the environment
2. `ONWATCH_ADMIN_USER` / `ONWATCH_ADMIN_PASS` in `~/.onwatch/.env`

## Install

From the repository root:

```bash
# Preview changes without modifying ~/.pi/agent/
./extensions/pi-onwatch-jev/install.sh --dry-run

# Symlink into ~/.pi/agent/extensions/pi-onwatch-jev and back up openrouter-presets.ts
./extensions/pi-onwatch-jev/install.sh
```

What `install.sh` does:

1. Symlinks `extensions/pi-onwatch-jev` → `~/.pi/agent/extensions/pi-onwatch-jev`.
2. Moves `~/.pi/agent/extensions/openrouter-presets.ts` (if present) to `~/.pi/agent/openrouter-presets.ts.bak` so Pi does not load duplicate `openrouter` provider registrations.

To revert:

```bash
./extensions/pi-onwatch-jev/install.sh --uninstall
```

## Registered OpenRouter Presets

On startup, `pi-onwatch-jev` registers the `openrouter` provider (`https://openrouter.ai/api/v1`, `openai-completions`, OpenRouter session-affinity compat) with:

| Preset ID | Display Name | Context Window | Max Tokens | Purpose |
|---|---|---|---|---|
| `@preset/code-auto` | Code Auto | 1,000,000 | 65,536 | Default/fallback OpenRouter route |
| `@preset/code-nvidia` | NVIDIA Free ($0) | 256,000 | 32,768 | `$0` free pool / intake |
| `@preset/code-xai` | xAI / SpaceXAI | 256,000 | 65,536 | xAI pool |
| `@preset/code-google` | Google AI Studio | 1,048,576 | 65,536 | Google AI Studio pool |
| `@preset/code-chutes` | Chutes PAYG | 202,752 | 32,768 | Chutes PAYG pool |
| `@preset/code-payg` | OpenRouter PAYG | 1,000,000 | 65,536 | General OpenRouter PAYG pool |
| `@preset/code-fast-free` | Code Fast Free ($0) | 1,000,000 | 32,768 | Retained during transition for `$0` intake |

It also keeps the `classifier:~typesafe/jev-latest` entry registered on `openrouter`. You can override `baseUrl` or the preset list without editing code by creating `~/.pi/agent/pi-onwatch-jev.json` (or setting `PI_ONWATCH_PRESETS_PATH`):

```json
{
  "baseUrl": "https://openrouter.ai/api/v1",
  "presets": [
    {
      "id": "@preset/code-auto",
      "name": "Code Auto",
      "contextWindow": 1000000,
      "maxTokens": 65536
    }
  ]
}
```

## How Daemon Discovery Works

Matches [`extensions/vscode`](../extensions/vscode/src/discovery.ts):

1. `ONWATCH_URL` (or `PI_ONWATCH_URL`) environment variable (full `http://` or `https://` base URL)
2. `~/.onwatch/port` (`%LOCALAPPDATA%\onwatch\port` on Windows)
3. `ONWATCH_PORT` environment variable
4. Default `http://127.0.0.1:9211`

## Jev Parallel Classification (`~typesafe/jev-latest`)

On every routed prompt (Branch A) or new work-thread dispatch (Branch B), `pi-onwatch-jev` makes one `POST https://openrouter.ai/api/v1/systemone` request asking in parallel:

- `tier`: `lightweight` | `workhorse` | `frontier`
- `effort`: `low` | `medium` | `high`
- `<tier>_model`: dynamically added for any tier where `onWatch` returned `> 1` candidate model, using each candidate's `description` from `onWatch` as its selection criterion.

## Branch A: Normal Pi Sessions

1. Queries `GET /api/route?harnesses=pi` (including `pool` when a sticky pool is active and `exclude` for any session-demoted models).
2. Keeps the session sticky to its current pool until that pool caps, cools down, errors, hits HTTP 429, or is demoted via `/bad`.
3. Runs Jev `~typesafe/jev-latest` on the prompt.
4. Resolves both OpenRouter presets (`openrouter/@preset/...`) and direct Pi-authenticated models (`anthropic`, `openai-codex`, `google-antigravity`, etc.).
5. Calls `pi.setModel()` only if `provider/id` changed, and `pi.setThinkingLevel()` only if reasoning effort changed (or immediately after `pi.setModel()`, which resets thinking level in Pi core). When tier/model/effort are unchanged, neither setter is called so the provider's prompt cache remains intact.
6. Tracks `turn_start` → first `message_update` (TTFT), `after_provider_response` (HTTP status / `429`), and `agent_end`, reporting telemetry to `POST /api/route/feedback`.
7. If `onWatch` is unreachable/stale or Jev fails, leaves the current/default Pi model active and notifies the user in-session.

### Commands

| Command | Description |
|---|---|
| `/bad [reason]` | Demote the current model/pool (`outcome: "demoted"`), exclude it for this session, and immediately reroute. |
| `/reroute [--reset]` | Clear the sticky pool and force a fresh `onWatch` + Jev pick. Pass `--reset` to also clear session demotions. |

## Branch B: Linear Intake Sessions & `jev_pick`

When a prompt begins with the `webhook-task.md` prefix (`You are the Linear dispatcher for this machine.`), `pi-onwatch-jev` never changes the intake session's pinned `$0` model or thinking level:

- **Stop signal / `"stop"`**: Runs `rename-thread`, `stop`, `say error`, `settle-thread` (**0 Jev calls**).
- **Existing `binding issue`**: Pipes the message to `followup`, posts `say thought`, runs `settle-thread` (**0 Jev calls**).
- **Unbound project (`issue` has no `folder`)**: Posts `say elicitation` and stores `pending --set` (**0 Jev calls**; or errors & settles if no Linear project is attached).
- **`prompted` reply with `pending` record**: Falls through unmodified to the intake LLM following `linear-dispatcher` `SKILL.md`, which can call the `jev_pick` tool for step 7.
- **Bound project needing a new work thread**: Queries `GET /api/route?harnesses=pi,cursor,antigravity,grok,claudeAgent,codex` (falls back to `linear-t3-dispatcher economy` if `onWatch` is unavailable; `opencode` is always excluded), calls Jev once on the issue title/description, runs `linear-t3-dispatcher start`, posts `POST /api/route/feedback`, posts `say thought`, and settles the intake.

All deterministically handled Branch B outcomes clear active tools for the turn (`selectedTools = []`) and replace the context with a single one-line confirmation so the pinned `$0` model finishes in one no-tool turn without seeing raw webhook JSON.

### `jev_pick` Tool

Exposed for explicit or fallback multi-harness selection (e.g., from `linear-dispatcher/SKILL.md` after a `pending` folder setup):

- **Parameters**: `prompt` (required string), `harnesses` (optional string array; `opencode` is always excluded), `exclude` (optional string array).
- **Returns**: JSON `{ fallback, harness, provider, model, id, tier, effort, pool, description }`.

## `onWatch` Routing API Contract

`pi-onwatch-jev` expects the following endpoints on the `onWatch` daemon:

### `GET /api/route`

Query parameters:
- `harnesses`: comma-separated harness list (`pi` or `pi,cursor,antigravity,grok,claudeAgent,codex`)
- `pool` (optional): preferred sticky pool name
- `exclude` (optional): comma-separated model/id keys to omit
- `tier` (optional): specific tier filter

Response JSON:

```json
{
  "stickyPool": "google",
  "stale": false,
  "tiers": {
    "lightweight": [
      {
        "harness": "pi",
        "provider": "openrouter",
        "model": "@preset/code-nvidia",
        "id": "openrouter/@preset/code-nvidia",
        "tier": "lightweight",
        "description": "NVIDIA Free ($0) fast model for quick edits and Q&A",
        "pool": "nvidia",
        "status": "ready"
      }
    ],
    "workhorse": [
      {
        "harness": "pi",
        "provider": "openrouter",
        "model": "@preset/code-google",
        "id": "openrouter/@preset/code-google",
        "tier": "workhorse",
        "description": "Google AI Studio 1M context coding workhorse",
        "pool": "google",
        "status": "ready"
      }
    ],
    "frontier": [
      {
        "harness": "pi",
        "provider": "anthropic",
        "model": "claude-opus-4-6",
        "id": "anthropic/claude-opus-4-6",
        "tier": "frontier",
        "description": "Claude Opus for complex architecture and deep debugging",
        "pool": "anthropic",
        "status": "ready"
      }
    ]
  }
}
```

### `POST /api/route/feedback`

Request JSON:

```json
{
  "harness": "pi",
  "provider": "openrouter",
  "model": "@preset/code-google",
  "pool": "google",
  "outcome": "ok",
  "statusCode": 200,
  "ttftMs": 310,
  "latencyMs": 1420,
  "reason": "optional error or /bad reason"
}
```

`outcome` is one of `"ok"`, `"rate_limit"`, `"error"`, or `"demoted"`.

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `ONWATCH_URL` / `PI_ONWATCH_URL` | auto-discovered | Override full `onWatch` base URL |
| `ONWATCH_PORT` | `9211` | Fallback port if `~/.onwatch/port` is absent |
| `ONWATCH_USERNAME` / `ONWATCH_PASSWORD` | `~/.onwatch/.env` | Basic Auth credentials for `onWatch` |
| `OPENROUTER_API_KEY` | Pi `auth.json` | Optional override for OpenRouter Jev SystemOne auth |
| `PI_ONWATCH_JEV_URL` / `OPENROUTER_BASE_URL` | `https://openrouter.ai/api/v1` | Override Jev SystemOne base URL |
| `PI_ONWATCH_PRESETS_PATH` | `~/.pi/agent/pi-onwatch-jev.json` | Optional custom OpenRouter presets config file |

## Testing

All tests run natively on Node 24+ using mocked `OnWatchClient`, `JevClient`, `DispatcherCli`, and Pi `ExtensionAPI` stubs:

```bash
node --test extensions/pi-onwatch-jev/test/*.test.ts
```

## Troubleshooting

- **Status shows `fallback: Could not reach onWatch...`**: Start `onwatch` (or check `~/.onwatch/port` / `ONWATCH_URL`). Pi stays on your configured default model until `onWatch` responds.
- **Status shows `fallback: No OpenRouter API key available`**: Run `/login openrouter` in Pi (saved in `~/.pi/agent/auth.json`) or export `OPENROUTER_API_KEY`.
- **Duplicate OpenRouter models in Pi**: Ensure `~/.pi/agent/extensions/openrouter-presets.ts` was moved out of `extensions/` (re-run `./extensions/pi-onwatch-jev/install.sh`).
