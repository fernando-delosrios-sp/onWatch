import assert from "node:assert/strict";
import test from "node:test";
import {
  buildJevRequest,
  formatOpenRouterHttpError,
  HttpJevClient,
  isOpenRouterGuardrailBlock,
  JEV_MODEL_ID,
  JevError,
  parseJevResponse,
  resolveOpenRouterApiKey,
} from "../jev-client.ts";
import type { RouteCandidate } from "../types.ts";

const NVIDIA: RouteCandidate = {
  harness: "pi",
  provider: "openrouter",
  model: "@preset/code-nvidia",
  id: "openrouter/@preset/code-nvidia",
  tier: "lightweight",
  description: "Free fast NVIDIA NIM model for simple tasks",
  pool: "nvidia",
};

const GOOGLE: RouteCandidate = {
  harness: "pi",
  provider: "openrouter",
  model: "@preset/code-google",
  id: "openrouter/@preset/code-google",
  tier: "workhorse",
  description: "Gemini via Google AI Studio with 1M context",
  pool: "google",
};

const CHUTES: RouteCandidate = {
  harness: "pi",
  provider: "openrouter",
  model: "@preset/code-chutes",
  id: "openrouter/@preset/code-chutes",
  tier: "workhorse",
  description: "DeepSeek/Qwen via Chutes",
  pool: "chutes",
};

test("resolveOpenRouterApiKey uses OPENROUTER_API_KEY env first, then Pi modelRegistry auth.json", async () => {
  const fromEnv = await resolveOpenRouterApiKey({
    env: { OPENROUTER_API_KEY: "sk-or-env" },
    modelRegistry: { getApiKeyForProvider: async () => "sk-or-auth-json" },
  });
  assert.equal(fromEnv, "sk-or-env");

  const fromAuthJson = await resolveOpenRouterApiKey({
    env: {},
    modelRegistry: { getApiKeyForProvider: async () => "sk-or-auth-json" },
  });
  assert.equal(fromAuthJson, "sk-or-auth-json");
});

test("buildJevRequest creates parallel questions and adds <tier>_model only for tiers with >1 candidate", () => {
  const req = buildJevRequest({
    mode: "pi",
    prompt: "Refactor the HTTP client and add retry tests",
    cwd: "/repo",
    candidatesByTier: {
      lightweight: [NVIDIA],
      workhorse: [GOOGLE, CHUTES],
    },
  });

  assert.equal(req.model, JEV_MODEL_ID);
  assert.ok(req.questions.tier);
  assert.ok(req.questions.effort);
  assert.equal(req.questions.lightweight_model, undefined);
  assert.equal(req.questions.frontier_model, undefined);
  assert.ok(req.questions.workhorse_model);
  assert.deepEqual(req.questions.workhorse_model.criteria, {
    "openrouter/@preset/code-google": "Gemini via Google AI Studio with 1M context",
    "openrouter/@preset/code-chutes": "DeepSeek/Qwen via Chutes",
  });
});

test("parseJevResponse selects <tier>_model choice and falls back if chosen tier has no candidates", () => {
  const decision = parseJevResponse(
    {
      answers: {
        tier: { type: "choice", choice: "workhorse", probabilities: { workhorse: 0.9 }, confidence: 0.9 },
        effort: { type: "choice", choice: "high", probabilities: { high: 0.8 }, confidence: 0.8 },
        workhorse_model: {
          type: "choice",
          choice: "openrouter/@preset/code-chutes",
          probabilities: { "openrouter/@preset/code-chutes": 0.7 },
          confidence: 0.7,
        },
      },
    },
    { candidatesByTier: { lightweight: [NVIDIA], workhorse: [GOOGLE, CHUTES] } },
  );

  assert.equal(decision.tier, "workhorse");
  assert.equal(decision.effort, "high");
  assert.equal(decision.candidate?.id, "openrouter/@preset/code-chutes");

  // Frontier chosen by Jev, but frontier has 0 candidates -> falls back to workhorse
  const fallbackDecision = parseJevResponse(
    {
      answers: {
        tier: { type: "choice", choice: "frontier" },
        effort: { type: "choice", choice: "high" },
      },
    },
    { candidatesByTier: { lightweight: [NVIDIA], workhorse: [GOOGLE] } },
  );
  assert.equal(fallbackDecision.tier, "workhorse");
  assert.equal(fallbackDecision.candidate?.id, "openrouter/@preset/code-google");
});

test("formatOpenRouterHttpError explains OpenRouter guardrail blocks on Jev", () => {
  const body = JSON.stringify({
    error: {
      message: "0 endpoints out of 1 requested are available matching your guardrail restrictions",
      metadata: {
        ineligibility_reasons: [{ reason: "model-ignored-by-guardrail", configure_url: "https://openrouter.ai/guardrails" }],
      },
    },
  });
  assert.equal(isOpenRouterGuardrailBlock(404, body), true);
  const msg = formatOpenRouterHttpError(404, body);
  assert.match(msg, /guardrails/i);
  assert.match(msg, /https:\/\/openrouter\.ai\/guardrails/);
  assert.match(msg, /jev-latest/i);
});

test("HttpJevClient retries another OpenRouter key when the first is guardrail-blocked", async () => {
  const calls: string[] = [];
  const guardrailBody = JSON.stringify({
    error: {
      message: "Model blocked by guardrail",
      metadata: { ineligibility_reasons: [{ reason: "model-ignored-by-guardrail" }] },
    },
  });

  const client = new HttpJevClient({
    env: { OPENROUTER_API_KEY: "sk-or-blocked" },
    resolveApiKey: async () => "sk-or-works",
    fetchImpl: (async (_url, init?: RequestInit) => {
      const auth = (init?.headers as Record<string, string>).authorization;
      calls.push(auth);
      if (auth === "Bearer sk-or-blocked") {
        return new Response(guardrailBody, { status: 404, headers: { "content-type": "application/json" } });
      }
      return new Response(
        JSON.stringify({
          answers: {
            tier: { type: "choice", choice: "lightweight", confidence: 1 },
            effort: { type: "choice", choice: "low", confidence: 1 },
          },
        }),
        { status: 200, headers: { "content-type": "application/json" } },
      );
    }) as typeof fetch,
  });

  const result = await client.classify({
    mode: "pi",
    prompt: "hello",
    candidatesByTier: { lightweight: [NVIDIA] },
  });
  assert.equal(result.tier, "lightweight");
  assert.equal(calls.length, 2);
  assert.equal(calls[0], "Bearer sk-or-blocked");
  assert.equal(calls[1], "Bearer sk-or-works");
});

test("HttpJevClient posts to OpenRouter /systemone with Pi OpenRouter auth", async () => {
  let capturedUrl = "";
  let capturedAuth = "";
  let capturedBody: any;

  const client = new HttpJevClient({
    resolveApiKey: async () => "sk-or-from-pi-auth",
    fetchImpl: (async (url: string | URL | Request, init?: RequestInit) => {
      capturedUrl = String(url);
      capturedAuth = (init?.headers as Record<string, string>).authorization;
      capturedBody = JSON.parse(String(init?.body));
      return new Response(
        JSON.stringify({
          answers: {
            tier: { type: "choice", choice: "lightweight", probabilities: {}, confidence: 0.95 },
            effort: { type: "choice", choice: "low", probabilities: {}, confidence: 0.9 },
          },
        }),
        { status: 200, headers: { "content-type": "application/json" } },
      );
    }) as typeof fetch,
  });

  const result = await client.classify({
    mode: "pi",
    prompt: "Fix typo in README",
    candidatesByTier: { lightweight: [NVIDIA], workhorse: [GOOGLE] },
  });

  assert.equal(capturedUrl, "https://openrouter.ai/api/v1/systemone");
  assert.equal(capturedAuth, "Bearer sk-or-from-pi-auth");
  assert.equal(capturedBody.model, "~typesafe/jev-latest");
  assert.equal(result.tier, "lightweight");
  assert.equal(result.effort, "low");
  assert.equal(result.candidate?.id, "openrouter/@preset/code-nvidia");

  const unauthClient = new HttpJevClient({
    env: {},
    home: "/tmp/pi-onwatch-jev-no-onwatch-db",
    resolveApiKey: async () => undefined,
  });
  await assert.rejects(
    () =>
      unauthClient.classify({
        mode: "pi",
        prompt: "hi",
        candidatesByTier: { lightweight: [NVIDIA] },
      }),
    (err: unknown) => err instanceof JevError && err.kind === "auth",
  );
});
