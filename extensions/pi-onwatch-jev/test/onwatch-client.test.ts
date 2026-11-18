import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import {
  discoverOnWatchBaseUrl,
  discoverOnWatchCredentials,
  HttpOnWatchClient,
  normalizeRouteResponse,
  OnWatchError,
} from "../onwatch-client.ts";

test("discoverOnWatchBaseUrl follows ONWATCH_URL -> port file -> ONWATCH_PORT -> default 9211", () => {
  const home = mkdtempSync(join(tmpdir(), "onwatch-home-"));
  try {
    mkdirSync(join(home, ".onwatch"), { recursive: true });
    assert.equal(discoverOnWatchBaseUrl({}, home, "darwin"), "http://127.0.0.1:9211");
    assert.equal(discoverOnWatchBaseUrl({ ONWATCH_PORT: "9333" }, home, "darwin"), "http://127.0.0.1:9333");

    writeFileSync(join(home, ".onwatch", "port"), "9444\n");
    assert.equal(discoverOnWatchBaseUrl({ ONWATCH_PORT: "9333" }, home, "darwin"), "http://127.0.0.1:9444");

    assert.equal(
      discoverOnWatchBaseUrl({ ONWATCH_URL: "http://127.0.0.1:9555/", ONWATCH_PORT: "9333" }, home, "darwin"),
      "http://127.0.0.1:9555",
    );
  } finally {
    rmSync(home, { recursive: true, force: true });
  }
});

test("discoverOnWatchCredentials reads env vars or ~/.onwatch/.env", () => {
  const home = mkdtempSync(join(tmpdir(), "onwatch-cred-"));
  try {
    mkdirSync(join(home, ".onwatch"), { recursive: true });
    writeFileSync(join(home, ".onwatch", ".env"), "ONWATCH_ADMIN_USER=admin\nONWATCH_ADMIN_PASS='secret-pass'\n");

    assert.deepEqual(discoverOnWatchCredentials({}, home), {
      username: "admin",
      password: "secret-pass",
    });

    assert.deepEqual(
      discoverOnWatchCredentials({ ONWATCH_USERNAME: "u2", ONWATCH_PASSWORD: "p2" }, home),
      { username: "u2", password: "p2" },
    );
  } finally {
    rmSync(home, { recursive: true, force: true });
  }
});

test("normalizeRouteResponse filters capped/cooling models and supports flat candidates", () => {
  const normalized = normalizeRouteResponse({
    stickyPool: "google",
    tiers: {
      lightweight: [
        { harness: "pi", provider: "openrouter", model: "@preset/code-nvidia", status: "ready", pool: "nvidia" },
        { harness: "pi", provider: "openrouter", model: "@preset/code-xai", status: "capped", pool: "xai" },
      ],
      workhorse: [
        { harness: "pi", provider: "openrouter", model: "@preset/code-google", status: "ready", pool: "google" },
        { harness: "pi", provider: "antigravity", model: "gemini-4-argon", status: "cooling", pool: "antigravity" },
      ],
    },
  });

  assert.equal(normalized.stickyPool, "google");
  assert.equal(normalized.tiers.lightweight?.length, 1);
  assert.equal(normalized.tiers.lightweight?.[0].model, "@preset/code-nvidia");
  assert.equal(normalized.tiers.workhorse?.length, 1);
  assert.equal(normalized.tiers.workhorse?.[0].model, "@preset/code-google");

  const flat = normalizeRouteResponse({
    candidates: [
      { harness: "cursor", model: "claude-sonnet-4-6", tier: "frontier", status: "ready" },
    ],
  });
  assert.equal(flat.tiers.frontier?.[0].id, "cursor/claude-sonnet-4-6");

  const goPayload = normalizeRouteResponse({
    generated_at: "2026-11-17T12:00:00Z",
    requested_harnesses: ["pi"],
    stale: false,
    tiers: {
      lightweight: [
        {
          id: "openrouter/@preset/code-nvidia",
          tier: "lightweight",
          harness: "pi",
          provider: "openrouter",
          model: "@preset/code-nvidia",
          overlay: "@preset/code-nvidia",
          underlying_model: "nvidia/nemotron-3.5-lightning:free",
          underlying_models: ["nvidia/nemotron-3.5-lightning:free"],
          description: "$0 fast Nemotron Lightning",
          pool: "nvidia-free",
          status: "ready",
          weight: 100,
        },
      ],
      workhorse: [],
      frontier: [],
    },
    tier_selections: {
      lightweight: { tier: "lightweight", available: true, pool: "nvidia-free" },
    },
    pools: [{ pool: { id: "nvidia-free" }, eligible: true }],
  });
  assert.equal(goPayload.stale, false);
  assert.equal(goPayload.tiers.lightweight?.[0].id, "openrouter/@preset/code-nvidia");
  assert.equal(goPayload.tiers.workhorse, undefined);
});

test("HttpOnWatchClient sends query params, basic auth, and feedback", async () => {
  const calls: Array<{ url: string; init: RequestInit }> = [];
  const client = new HttpOnWatchClient({
    baseUrl: "http://127.0.0.1:9211",
    credentials: { username: "admin", password: "pw" },
    fetchImpl: (async (url: string | URL | Request, init?: RequestInit) => {
      calls.push({ url: String(url), init: init ?? {} });
      if (String(url).includes("/api/route?")) {
        return new Response(
          JSON.stringify({
            tiers: {
              workhorse: [
                {
                  harness: "pi",
                  provider: "openrouter",
                  model: "@preset/code-google",
                  id: "openrouter/@preset/code-google",
                  description: "Google AI Studio",
                  pool: "google",
                  status: "ready",
                },
              ],
            },
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        );
      }
      return new Response(JSON.stringify({ ok: true }), { status: 200 });
    }) as typeof fetch,
  });

  const route = await client.getRoute({
    harnesses: ["pi"],
    pool: "google",
    exclude: ["openrouter/@preset/code-xai"],
    excludeFree: true,
  });
  assert.equal(route.tiers.workhorse?.[0].pool, "google");
  assert.ok(calls[0].url.includes("harnesses=pi"));
  assert.ok(calls[0].url.includes("pool=google"));
  assert.ok(calls[0].url.includes("exclude=openrouter%2F%40preset%2Fcode-xai"));
  assert.ok(calls[0].url.includes("exclude_free=true"));
  const headers = calls[0].init.headers as Record<string, string>;
  assert.equal(headers.authorization, `Basic ${Buffer.from("admin:pw").toString("base64")}`);

  await client.postFeedback({
    harness: "pi",
    provider: "openrouter",
    model: "@preset/code-google",
    pool: "google",
    outcome: "ok",
    ttftMs: 210,
    latencyMs: 900,
  });
  assert.equal(calls[1].url, "http://127.0.0.1:9211/api/route/feedback");
  assert.equal(calls[1].init.method, "POST");
});

test("HttpOnWatchClient maps network failures and 401s to typed OnWatchError", async () => {
  const authClient = new HttpOnWatchClient({
    baseUrl: "http://127.0.0.1:9211",
    fetchImpl: (async () => new Response("nope", { status: 401 })) as typeof fetch,
  });
  await assert.rejects(
    () => authClient.getRoute({ harnesses: ["pi"] }),
    (err: unknown) => err instanceof OnWatchError && err.kind === "auth",
  );

  const downClient = new HttpOnWatchClient({
    baseUrl: "http://127.0.0.1:9211",
    fetchImpl: (async () => {
      throw new Error("ECONNREFUSED");
    }) as typeof fetch,
  });
  await assert.rejects(
    () => downClient.getRoute({ harnesses: ["pi"] }),
    (err: unknown) => err instanceof OnWatchError && err.kind === "unreachable",
  );
});

test("normalizeRouteResponse with excludeFree promotes non-free tier_selections fallback", () => {
  const normalized = normalizeRouteResponse(
    {
      stickyPool: "nvidia-free",
      tiers: {
        lightweight: [
          {
            id: "openrouter/@preset/code-nvidia",
            tier: "lightweight",
            harness: "pi",
            provider: "openrouter",
            model: "@preset/code-nvidia",
            overlay: "@preset/code-nvidia",
            pool: "nvidia-free",
            billing: "free",
            status: "ready",
          },
        ],
      },
      tier_selections: {
        lightweight: {
          tier: "lightweight",
          available: true,
          pool: "nvidia-free",
          fallbacks: [
            {
              pool: "xai",
              category: "subscription",
              harness: "pi",
              provider: "openrouter",
              overlay: "@preset/code-xai",
              candidates: [
                {
                  id: "openrouter/x-ai/grok-build-0.1",
                  tier: "lightweight",
                  model: "x-ai/grok-build-0.1",
                  pool: "xai",
                  harness: "pi",
                  provider: "openrouter",
                  overlay: "@preset/code-xai",
                  billing: "byok",
                  status: "ready",
                },
              ],
            },
          ],
        },
      },
    },
    { excludeFree: true },
  );

  assert.equal(normalized.stickyPool, undefined);
  assert.equal(normalized.tiers.lightweight?.length, 1);
  assert.equal(normalized.tiers.lightweight?.[0].id, "openrouter/@preset/code-xai");
  assert.equal(normalized.tiers.lightweight?.[0].pool, "xai");
});
