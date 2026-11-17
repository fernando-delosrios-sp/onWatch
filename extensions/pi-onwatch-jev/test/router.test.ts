import assert from "node:assert/strict";
import test from "node:test";
import {
  createInitialSessionState,
  handleBadCommand,
  handleRerouteCommand,
  recordTurnEndFeedback,
  routePiTurn,
} from "../router.ts";
import type { JevClient, OnWatchClient, RouteCandidate, RouteFeedback, RouteResponse } from "../types.ts";

const NVIDIA: RouteCandidate = {
  harness: "pi",
  provider: "openrouter",
  model: "@preset/code-nvidia",
  id: "openrouter/@preset/code-nvidia",
  tier: "lightweight",
  description: "NVIDIA Free",
  pool: "nvidia",
  status: "ready",
};

const GOOGLE: RouteCandidate = {
  harness: "pi",
  provider: "openrouter",
  model: "@preset/code-google",
  id: "openrouter/@preset/code-google",
  tier: "workhorse",
  description: "Google AI Studio",
  pool: "google",
  status: "ready",
};

const CHUTES: RouteCandidate = {
  harness: "pi",
  provider: "openrouter",
  model: "@preset/code-chutes",
  id: "openrouter/@preset/code-chutes",
  tier: "workhorse",
  description: "Chutes PAYG",
  pool: "chutes",
  status: "ready",
};

function makeFakePiContext(initialModel = { provider: "openrouter", id: "@preset/code-auto", reasoning: true }) {
  const models: Record<string, any> = {
    "openrouter/@preset/code-auto": initialModel,
    "openrouter/@preset/code-nvidia": { provider: "openrouter", id: "@preset/code-nvidia", reasoning: true },
    "openrouter/@preset/code-google": { provider: "openrouter", id: "@preset/code-google", reasoning: true },
    "openrouter/@preset/code-chutes": { provider: "openrouter", id: "@preset/code-chutes", reasoning: true },
  };
  let currentModel: any = initialModel;
  let currentThinking = "medium";
  const calls: string[] = [];
  const notifications: Array<{ msg: string; level?: string }> = [];

  const pi = {
    async setModel(model: any) {
      calls.push(`setModel:${model.provider}/${model.id}`);
      currentModel = model;
      // Simulate Pi's reset of thinking level during setModel
      currentThinking = "medium";
      return true;
    },
    setThinkingLevel(level: string) {
      calls.push(`setThinkingLevel:${level}`);
      currentThinking = level;
    },
    getThinkingLevel() {
      return currentThinking;
    },
  };

  const ctx = {
    get model() {
      return currentModel;
    },
    get thinkingLevel() {
      return currentThinking;
    },
    hasUI: true,
    ui: {
      setStatus() {},
      notify(msg: string, level?: string) {
        notifications.push({ msg, level });
      },
    },
    modelRegistry: {
      find(provider: string, id: string) {
        return models[`${provider}/${id}`];
      },
      getAvailable() {
        return Object.values(models);
      },
      hasConfiguredAuth() {
        return true;
      },
    },
  };

  return { pi, ctx, calls, notifications };
}

test("Branch A preserves prompt cache when tier/model/effort are unchanged across turns", async () => {
  const { pi, ctx, calls } = makeFakePiContext();
  const state = createInitialSessionState();

  const onWatch: OnWatchClient = {
    async getRoute(): Promise<RouteResponse> {
      return { tiers: { lightweight: [NVIDIA], workhorse: [GOOGLE] } };
    },
    async postFeedback() {},
  };

  const jev: JevClient = {
    async classify() {
      return { tier: "workhorse", effort: "high", candidate: GOOGLE };
    },
  };

  // Turn 1: switches from code-auto -> code-google, and applies high thinking AFTER setModel
  const r1 = await routePiTurn({ prompt: "Add retry logic", ctx: ctx as any, deps: { onWatch, jev, state, pi: pi as any } });
  assert.equal(r1.action, "switched");
  assert.deepEqual(calls, [
    "setModel:openrouter/@preset/code-google",
    "setThinkingLevel:high",
  ]);
  assert.equal(state.stickyPool, "google");

  // Turn 2: same tier/model/effort -> ZERO calls to setModel or setThinkingLevel
  calls.length = 0;
  const r2 = await routePiTurn({ prompt: "Also add unit test", ctx: ctx as any, deps: { onWatch, jev, state, pi: pi as any } });
  assert.equal(r2.action, "unchanged");
  assert.deepEqual(calls, []);

  // Turn 3: effort drops to low on same model -> only setThinkingLevel
  const jevLow: JevClient = {
    async classify() {
      return { tier: "workhorse", effort: "low", candidate: GOOGLE };
    },
  };
  const r3 = await routePiTurn({ prompt: "Format this line", ctx: ctx as any, deps: { onWatch, jev: jevLow, state, pi: pi as any } });
  assert.equal(r3.action, "switched");
  assert.deepEqual(calls, ["setThinkingLevel:low"]);
});

test("Branch A re-picks pool only when sticky pool drops out and /bad demotes & reroutes", async () => {
  const { pi, ctx } = makeFakePiContext();
  const state = createInitialSessionState();
  const feedbacks: RouteFeedback[] = [];
  let googleAlive = true;

  const onWatch: OnWatchClient = {
    async getRoute(): Promise<RouteResponse> {
      return {
        tiers: {
          workhorse: googleAlive ? [GOOGLE, CHUTES] : [CHUTES],
        },
      };
    },
    async postFeedback(fb) {
      feedbacks.push(fb);
    },
  };

  const jev: JevClient = {
    async classify(input) {
      const workhorse = input.candidatesByTier.workhorse ?? [];
      return { tier: "workhorse", effort: "medium", candidate: workhorse[0] };
    },
  };

  // Turn 1 pins stickyPool = google
  await routePiTurn({ prompt: "Implement X", ctx: ctx as any, deps: { onWatch, jev, state, pi: pi as any } });
  assert.equal(state.stickyPool, "google");

  // Turn 2: google drops out -> poolDropped triggers switch to chutes
  googleAlive = false;
  const r2 = await routePiTurn({ prompt: "Continue X", ctx: ctx as any, deps: { onWatch, jev, state, pi: pi as any } });
  assert.equal(r2.poolDropped, true);
  assert.equal(r2.candidate?.id, "openrouter/@preset/code-chutes");
  assert.equal(state.stickyPool, "chutes");

  // /bad demotes chutes and switches back to google once google is back
  googleAlive = true;
  const badRes = await handleBadCommand({ args: "hallucinated API", ctx: ctx as any, deps: { onWatch, jev, state, pi: pi as any } });
  assert.equal(feedbacks.at(-1)?.outcome, "demoted");
  assert.equal(feedbacks.at(-1)?.model, "@preset/code-chutes");
  assert.equal(badRes.candidate?.id, "openrouter/@preset/code-google");

  // /reroute --reset clears excluded keys
  await handleRerouteCommand({ args: "--reset", ctx: ctx as any, deps: { onWatch, jev, state, pi: pi as any } });
  assert.equal(state.excludedKeys.size, 0);
});

test("Branch A telemetry reports 429 rate limits and excludes failed pool", async () => {
  const state = createInitialSessionState();
  state.stickyPool = "google";
  state.currentCandidate = GOOGLE;
  state.lastTurn = {
    startMs: 1000,
    firstTokenMs: 1250,
    statusCode: 429,
    candidate: GOOGLE,
  };

  const feedbacks: RouteFeedback[] = [];
  await recordTurnEndFeedback({
    state,
    onWatch: {
      async getRoute() {
        return { tiers: {} };
      },
      async postFeedback(fb) {
        feedbacks.push(fb);
      },
    },
    errorMessage: "429 Too Many Requests",
    nowMs: 2500,
  });

  assert.equal(feedbacks.length, 1);
  assert.equal(feedbacks[0].outcome, "rate_limit");
  assert.equal(feedbacks[0].ttftMs, 250);
  assert.equal(feedbacks[0].latencyMs, 1500);
  assert.equal(state.stickyPool, undefined);
  assert.ok(state.excludedPools.has("google"));
});

test("Branch A falls back cleanly and notifies user when onWatch or Jev is unreachable", async () => {
  const { pi, ctx, calls, notifications } = makeFakePiContext();
  const state = createInitialSessionState();

  const brokenOnWatch: OnWatchClient = {
    async getRoute() {
      throw new Error("ECONNREFUSED");
    },
    async postFeedback() {},
  };
  const jev: JevClient = {
    async classify() {
      throw new Error("should not be called");
    },
  };

  const res = await routePiTurn({ prompt: "Hello", ctx: ctx as any, deps: { onWatch: brokenOnWatch, jev, state, pi: pi as any } });
  assert.equal(res.action, "fallback");
  assert.equal(calls.length, 0);
  assert.equal(state.fallbackActive, true);
  assert.ok(notifications.some((n) => n.msg.includes("fallback")));
});
