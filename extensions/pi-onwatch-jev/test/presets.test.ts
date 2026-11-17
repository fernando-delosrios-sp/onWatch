import assert from "node:assert/strict";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import {
  DEFAULT_OPENROUTER_PRESETS,
  JEV_CLASSIFIER_MODEL_CONFIG,
  registerOpenRouterPresets,
  resolvePiModel,
} from "../presets.ts";

test("registerOpenRouterPresets registers simplified presets, transition fast-free, and jev classifier", () => {
  const registered: Array<{ name: string; config: any }> = [];
  const fakePi = {
    registerProvider(name: string, config: any) {
      registered.push({ name, config });
    },
  };

  registerOpenRouterPresets(fakePi as any, { env: {}, home: "/nonexistent-home" });
  assert.equal(registered.length, 1);
  assert.equal(registered[0].name, "openrouter");
  assert.equal(registered[0].config.baseUrl, "https://openrouter.ai/api/v1");
  assert.equal(registered[0].config.api, "openai-completions");

  const ids = registered[0].config.models.map((m: any) => m.id);
  for (const preset of DEFAULT_OPENROUTER_PRESETS) {
    assert.ok(ids.includes(preset.id), `missing preset ${preset.id}`);
  }
  assert.ok(ids.includes(JEV_CLASSIFIER_MODEL_CONFIG.id), "missing jev classifier model");
  assert.equal(ids.includes("@preset/code-daily"), false);
  assert.equal(ids.includes("@preset/code-architect"), false);
  assert.equal(ids.includes("@preset/code-longctx"), false);
});

test("registerOpenRouterPresets supports loading overrides from PI_ONWATCH_PRESETS_PATH", () => {
  const dir = mkdtempSync(join(tmpdir(), "pi-onwatch-presets-"));
  try {
    const configPath = join(dir, "presets.json");
    writeFileSync(
      configPath,
      JSON.stringify({
        baseUrl: "https://proxy.example.com/v1/",
        presets: [
          { id: "@preset/custom", name: "Custom", contextWindow: 128000, maxTokens: 16384 },
        ],
      }),
    );

    const registered: Array<{ name: string; config: any }> = [];
    registerOpenRouterPresets(
      {
        registerProvider(name: string, config: any) {
          registered.push({ name, config });
        },
      } as any,
      { env: { PI_ONWATCH_PRESETS_PATH: configPath } },
    );

    assert.equal(registered[0].config.baseUrl, "https://proxy.example.com/v1");
    assert.equal(registered[0].config.models[0].id, "@preset/custom");
    assert.equal(registered[0].config.models[1].id, JEV_CLASSIFIER_MODEL_CONFIG.id);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("resolvePiModel resolves OpenRouter presets and direct Pi-logged-in models with aliases", () => {
  const models: Record<string, any> = {
    "openrouter/@preset/code-google": { provider: "openrouter", id: "@preset/code-google", reasoning: true },
    "anthropic/claude-sonnet-4-6": { provider: "anthropic", id: "claude-sonnet-4-6", reasoning: true },
    "openai-codex/gpt-5.3-codex": { provider: "openai-codex", id: "gpt-5.3-codex", reasoning: true },
    "google-antigravity/gemini-4-argon": { provider: "google-antigravity", id: "gemini-4-argon", reasoning: true },
    "xai/grok-4.7": { provider: "xai", id: "grok-4.7", reasoning: false },
  };

  const registry = {
    find(provider: string, id: string) {
      return models[`${provider}/${id}`];
    },
    getAvailable() {
      return Object.values(models).filter((m) => m.provider !== "xai");
    },
    hasConfiguredAuth(model: any) {
      return model.provider !== "xai";
    },
  };

  assert.equal(
    resolvePiModel(registry as any, {
      provider: "openrouter",
      model: "@preset/code-google",
      id: "openrouter/@preset/code-google",
    })?.id,
    "@preset/code-google",
  );

  assert.equal(
    resolvePiModel(registry as any, {
      provider: "claude",
      model: "claude-sonnet-4-6",
      id: "claude/claude-sonnet-4-6",
    })?.provider,
    "anthropic",
  );

  assert.equal(
    resolvePiModel(registry as any, {
      provider: "codex",
      model: "gpt-5.3-codex",
      id: "codex/gpt-5.3-codex",
    })?.provider,
    "openai-codex",
  );

  assert.equal(
    resolvePiModel(registry as any, {
      provider: "antigravity",
      model: "gemini-4-argon",
      id: "antigravity/gemini-4-argon",
    })?.provider,
    "google-antigravity",
  );

  // xai has no configured auth -> should return undefined
  assert.equal(
    resolvePiModel(registry as any, {
      provider: "xai",
      model: "grok-4.7",
      id: "xai/grok-4.7",
    }),
    undefined,
  );
});
