import { existsSync, readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import type { Api, Model } from "@earendil-works/pi-ai";
import type { ExtensionAPI, ModelRegistry, ProviderModelConfig } from "@earendil-works/pi-coding-agent";
import type { RouteCandidate } from "./types.ts";

export const OPENROUTER_BASE_URL = "https://openrouter.ai/api/v1";

export const OPENROUTER_COMPAT = {
  supportsDeveloperRole: false,
  thinkingFormat: "openrouter" as const,
  supportsStrictMode: true,
  sendSessionAffinityHeaders: true,
  sessionAffinityFormat: "openrouter" as const,
};

export interface PresetSpec {
  id: string;
  name: string;
  contextWindow: number;
  maxTokens: number;
  reasoning?: boolean;
  input?: ("text" | "image")[];
  /** OpenRouter `model` field when this preset is used for chat (shell presets only). */
  openRouterPresetId?: string;
}

/** Pi model id for the onWatch + Jev routing entry point (not a long-lived inference target). */
export const JEV_ROUTING_PRESET_ID = "@preset/jev-routing";

export const DEFAULT_ROUTING_TRIGGER_MODELS: readonly string[] = [
  `openrouter/${JEV_ROUTING_PRESET_ID}`,
  JEV_ROUTING_PRESET_ID,
];

/**
 * Simplified 5 per-provider presets plus @preset/code-auto,
 * and @preset/code-fast-free retained during transition for $0 intake sessions.
 */
export const DEFAULT_OPENROUTER_PRESETS: readonly PresetSpec[] = [
  {
    id: JEV_ROUTING_PRESET_ID,
    name: "Jev Routing (onWatch)",
    contextWindow: 1000000,
    maxTokens: 65536,
    openRouterPresetId: "@preset/code-auto",
  },
  {
    id: "@preset/code-auto",
    name: "Code Auto",
    contextWindow: 1000000,
    maxTokens: 65536,
  },
  {
    id: "@preset/code-nvidia",
    name: "NVIDIA Free ($0)",
    contextWindow: 256000,
    maxTokens: 32768,
  },
  {
    id: "@preset/code-xai",
    name: "xAI / SpaceXAI",
    contextWindow: 256000,
    maxTokens: 65536,
  },
  {
    id: "@preset/code-google",
    name: "Google AI Studio",
    contextWindow: 1048576,
    maxTokens: 65536,
  },
  {
    id: "@preset/code-chutes",
    name: "Chutes",
    contextWindow: 202752,
    maxTokens: 32768,
  },
  {
    id: "@preset/openrouter",
    name: "OpenRouter",
    contextWindow: 1000000,
    maxTokens: 65536,
  },
  {
    id: "@preset/code-fast-free",
    name: "Code Fast Free ($0)",
    contextWindow: 1000000,
    maxTokens: 32768,
  },
];

export const JEV_CLASSIFIER_MODEL_CONFIG: ProviderModelConfig = {
  type: "classifier",
  id: "~typesafe/jev-latest",
  name: "TypeSafe: Jev Latest",
  api: "typesafe-system-one",
  baseUrl: OPENROUTER_BASE_URL,
  input: ["text", "image"],
  cost: { input: 0.042, output: 0, cacheRead: 0, cacheWrite: 0 },
  contextWindow: 262144,
};

export const VETTED_OPENROUTER_MODELS: readonly ProviderModelConfig[] = [
  {
    type: "chat",
    id: "nvidia/nemotron-3-ultra-550b-a55b:free",
    name: "NVIDIA: Nemotron 3 Ultra (free)",
    api: "openai-completions",
    baseUrl: OPENROUTER_BASE_URL,
    provider: "openrouter",
    reasoning: true,
    thinkingLevelMap: {
      off: "none",
      minimal: null,
      low: null,
      medium: "medium",
      high: "high",
      xhigh: null,
      max: null,
    },
    input: ["text"],
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    contextWindow: 1000000,
    maxTokens: 65536,
    compat: {
      supportsDeveloperRole: false,
      thinkingFormat: "openrouter",
      supportsStrictMode: true,
      sendSessionAffinityHeaders: true,
    },
  },
  {
    type: "chat",
    id: "nvidia/nemotron-3.5-lightning:free",
    name: "NVIDIA: Nemotron 3.5 Lightning (free)",
    api: "openai-completions",
    baseUrl: OPENROUTER_BASE_URL,
    provider: "openrouter",
    reasoning: true,
    input: ["text"],
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    contextWindow: 1000000,
    maxTokens: 65536,
    compat: {
      supportsDeveloperRole: false,
      thinkingFormat: "openrouter",
      supportsStrictMode: true,
      sendSessionAffinityHeaders: true,
    },
  },
  {
    type: "chat",
    id: "nvidia/nemotron-3-nano-omni-30b-a3b-reasoning:free",
    name: "NVIDIA: Nemotron 3 Nano Omni (free)",
    api: "openai-completions",
    baseUrl: OPENROUTER_BASE_URL,
    provider: "openrouter",
    reasoning: true,
    input: ["text", "image"],
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    contextWindow: 256000,
    maxTokens: 65536,
    compat: {
      supportsDeveloperRole: false,
      thinkingFormat: "openrouter",
      supportsStrictMode: true,
      sendSessionAffinityHeaders: true,
    },
  },
  {
    type: "chat",
    id: "deepseek/deepseek-v4.1-flash-1017",
    name: "DeepSeek: DeepSeek V4.1 Flash 1017",
    api: "openai-completions",
    baseUrl: OPENROUTER_BASE_URL,
    provider: "openrouter",
    reasoning: true,
    thinkingLevelMap: {
      off: "none",
      minimal: null,
      low: "low",
      medium: null,
      high: "high",
      xhigh: null,
      max: "max",
    },
    input: ["text", "image"],
    cost: { input: 0.036, output: 0.5, cacheRead: 0.01, cacheWrite: 0 },
    contextWindow: 1048576,
    maxTokens: 943718,
    compat: {
      supportsDeveloperRole: false,
      thinkingFormat: "openrouter",
      supportsStrictMode: true,
      sendSessionAffinityHeaders: true,
    },
  },
  {
    type: "chat",
    id: "google/gemini-3.5-flash-lite",
    name: "Google: Gemini 3.5 Flash Lite",
    api: "openai-completions",
    baseUrl: OPENROUTER_BASE_URL,
    provider: "openrouter",
    reasoning: true,
    thinkingLevelMap: {
      off: null,
      minimal: "minimal",
      low: "low",
      medium: "medium",
      high: "high",
      xhigh: null,
      max: null,
    },
    input: ["text", "image"],
    cost: { input: 0.3, output: 2.5, cacheRead: 0.03, cacheWrite: 0.083333 },
    contextWindow: 1048576,
    maxTokens: 65536,
    compat: {
      supportsDeveloperRole: false,
      thinkingFormat: "openrouter",
      supportsStrictMode: true,
      sendSessionAffinityHeaders: true,
    },
  },
  {
    type: "chat",
    id: "moonshotai/kimi-k2.6",
    name: "MoonshotAI: Kimi K2.6",
    api: "openai-completions",
    baseUrl: OPENROUTER_BASE_URL,
    provider: "openrouter",
    reasoning: true,
    input: ["text", "image"],
    cost: { input: 0.3895, output: 1.64, cacheRead: 0.0656, cacheWrite: 0 },
    contextWindow: 262144,
    maxTokens: 235929,
    compat: {
      supportsDeveloperRole: false,
      thinkingFormat: "openrouter",
      supportsStrictMode: true,
      sendSessionAffinityHeaders: true,
    },
  },
  {
    type: "chat",
    id: "openai/gpt-6.1-terra",
    name: "OpenAI: GPT-6.1 Terra",
    api: "openai-completions",
    baseUrl: OPENROUTER_BASE_URL,
    provider: "openrouter",
    reasoning: true,
    thinkingLevelMap: {
      off: "none",
      minimal: null,
      low: "low",
      medium: "medium",
      high: "high",
      xhigh: "xhigh",
      max: "max",
    },
    input: ["text", "image"],
    cost: { input: 1, output: 5, cacheRead: 0.05, cacheWrite: 1.25 },
    contextWindow: 1050000,
    maxTokens: 128000,
    compat: {
      thinkingFormat: "openrouter",
      supportsStrictMode: true,
      sendSessionAffinityHeaders: true,
    },
  },
  {
    type: "chat",
    id: "qwen/qwen3.8-27b",
    name: "Qwen: Qwen3.8 27B",
    api: "openai-completions",
    baseUrl: OPENROUTER_BASE_URL,
    provider: "openrouter",
    reasoning: true,
    thinkingLevelMap: {
      off: "none",
      minimal: null,
      low: "low",
      medium: "medium",
      high: null,
      xhigh: "xhigh",
      max: null,
    },
    input: ["text", "image"],
    cost: { input: 0.035, output: 1.5, cacheRead: 0.0175, cacheWrite: 0 },
    contextWindow: 262144,
    maxTokens: 131072,
    compat: {
      supportsDeveloperRole: false,
      thinkingFormat: "openrouter",
      supportsStrictMode: true,
      sendSessionAffinityHeaders: true,
    },
  },
  {
    type: "chat",
    id: "typesafe/jev-router",
    name: "TypeSafe: Jev Router",
    api: "openai-completions",
    baseUrl: OPENROUTER_BASE_URL,
    provider: "openrouter",
    reasoning: true,
    input: ["text", "image"],
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    contextWindow: 1000000,
    maxTokens: 128000,
    compat: {
      supportsDeveloperRole: false,
      thinkingFormat: "openrouter",
      supportsStrictMode: true,
      sendSessionAffinityHeaders: true,
    },
  },
  {
    type: "chat",
    id: "x-ai/grok-4.7",
    name: "SpaceXAI: Grok 4.7",
    api: "openai-completions",
    baseUrl: OPENROUTER_BASE_URL,
    provider: "openrouter",
    reasoning: true,
    thinkingLevelMap: {
      off: null,
      minimal: null,
      low: "low",
      medium: "medium",
      high: "high",
      xhigh: "xhigh",
      max: null,
    },
    input: ["text", "image"],
    cost: { input: 2, output: 6, cacheRead: 0.5, cacheWrite: 0 },
    contextWindow: 500000,
    maxTokens: 450000,
    compat: {
      supportsDeveloperRole: false,
      thinkingFormat: "openrouter",
      supportsStrictMode: true,
      sendSessionAffinityHeaders: true,
    },
  },
  {
    type: "chat",
    id: "x-ai/grok-build-0.1",
    name: "SpaceXAI: Grok Build 0.1",
    api: "openai-completions",
    baseUrl: OPENROUTER_BASE_URL,
    provider: "openrouter",
    reasoning: true,
    input: ["text", "image"],
    cost: { input: 1, output: 2, cacheRead: 0.2, cacheWrite: 0 },
    contextWindow: 256000,
    maxTokens: 230400,
    compat: {
      supportsDeveloperRole: false,
      thinkingFormat: "openrouter",
      supportsStrictMode: true,
      sendSessionAffinityHeaders: true,
    },
  },
  {
    type: "chat",
    id: "z-ai/glm-5.1",
    name: "Z.ai: GLM 5.1",
    api: "openai-completions",
    baseUrl: OPENROUTER_BASE_URL,
    provider: "openrouter",
    reasoning: true,
    input: ["text"],
    cost: { input: 0.9, output: 2.9, cacheRead: 0.09, cacheWrite: 0 },
    contextWindow: 202752,
    maxTokens: 65535,
    compat: {
      supportsDeveloperRole: false,
      thinkingFormat: "openrouter",
      supportsStrictMode: true,
      sendSessionAffinityHeaders: true,
    },
  },
  {
    type: "chat",
    id: "google/gemini-4-carbon",
    name: "Google: Gemini 4 Carbon",
    api: "openai-completions",
    baseUrl: OPENROUTER_BASE_URL,
    provider: "openrouter",
    reasoning: true,
    thinkingLevelMap: {
      off: null,
      minimal: null,
      low: "low",
      medium: "medium",
      high: "high",
      xhigh: null,
      max: null,
    },
    input: ["text", "image"],
    cost: { input: 2, output: 10, cacheRead: 0.1, cacheWrite: 0.1875 },
    contextWindow: 1048576,
    maxTokens: 943718,
    compat: {
      supportsDeveloperRole: false,
      thinkingFormat: "openrouter",
      supportsStrictMode: true,
      sendSessionAffinityHeaders: true,
    },
  },
  {
    type: "chat",
    id: "google/gemini-4-hydrogen",
    name: "Google: Gemini 4 Hydrogen",
    api: "openai-completions",
    baseUrl: OPENROUTER_BASE_URL,
    provider: "openrouter",
    reasoning: true,
    thinkingLevelMap: {
      off: null,
      minimal: null,
      low: "low",
      medium: "medium",
      high: "high",
      xhigh: "xhigh",
      max: null,
    },
    input: ["text", "image"],
    cost: { input: 1.5, output: 7.5, cacheRead: 0.075, cacheWrite: 0.083333 },
    contextWindow: 1048576,
    maxTokens: 943718,
    compat: {
      supportsDeveloperRole: false,
      thinkingFormat: "openrouter",
      supportsStrictMode: true,
      sendSessionAffinityHeaders: true,
    },
  },
];

export function loadVettedOpenRouterModels(home: string = homedir()): ProviderModelConfig[] {
  const storePath = join(home, ".pi", "agent", "models-store.json");
  if (existsSync(storePath)) {
    try {
      const data = JSON.parse(readFileSync(storePath, "utf8")) as {
        openrouter?: { models?: ProviderModelConfig[] };
      };
      const models = data.openrouter?.models;
      if (Array.isArray(models) && models.length > 0) {
        const chatModels = models.filter(
          (m): m is ProviderModelConfig =>
            Boolean(m) &&
            m.type === "chat" &&
            typeof m.id === "string" &&
            !m.id.startsWith("@preset/"),
        );
        if (chatModels.length > 0) return chatModels;
      }
    } catch {
      // Fall through to static catalog
    }
  }
  return [...VETTED_OPENROUTER_MODELS];
}

export interface PresetsConfigFile {
  baseUrl?: string;
  presets?: PresetSpec[];
  /** When set, Jev/onWatch routing runs only while the active Pi model matches one of these keys. Use `["*"]` for legacy always-on routing; omit to default to Jev Routing only. */
  routingTriggerModels?: string[];
}

export function isJevRoutingShellModel(modelOrId: string | undefined): boolean {
  if (!modelOrId) return false;
  const trimmed = modelOrId.trim();
  return (
    trimmed === JEV_ROUTING_PRESET_ID ||
    trimmed === `openrouter/${JEV_ROUTING_PRESET_ID}` ||
    trimmed.endsWith(`/${JEV_ROUTING_PRESET_ID}`)
  );
}

export function isJevRoutingShellCandidate(candidate: Pick<RouteCandidate, "model" | "id">): boolean {
  return isJevRoutingShellModel(candidate.model) || isJevRoutingShellModel(candidate.id);
}

const ROUTING_TRIGGER_ENV = "PI_ONWATCH_JEV_ROUTING_TRIGGER_MODELS";

function parseRoutingTriggerModelsFromEnv(env: NodeJS.ProcessEnv): string[] | undefined {
  const raw = env[ROUTING_TRIGGER_ENV]?.trim();
  if (!raw) return undefined;
  const list = raw
    .split(",")
    .map((part) => part.trim())
    .filter(Boolean);
  return list.length > 0 ? list : [];
}

function parseRoutingTriggerModelsFromFile(raw: unknown): string[] | undefined {
  if (!Array.isArray(raw)) return undefined;
  const list = raw
    .filter((entry): entry is string => typeof entry === "string")
    .map((entry) => entry.trim())
    .filter(Boolean);
  return list;
}

export function piModelKey(model: Pick<Model, "provider" | "id"> | undefined): string | undefined {
  if (!model?.provider || !model.id) return undefined;
  return `${model.provider}/${model.id}`;
}

/**
 * Returns whether Branch A should run onWatch + Jev for the active session model.
 * `triggers` undefined = always route (backward compatible).
 * `triggers` [] = never route.
 */
export function modelMatchesRoutingTrigger(
  model: Pick<Model, "provider" | "id"> | undefined,
  triggers: readonly string[] | undefined,
): boolean {
  if (triggers === undefined) return true;
  if (triggers.length === 0) return false;
  if (triggers.some((entry) => entry.trim() === "*")) return true;
  if (!model) return false;

  const fullKey = piModelKey(model);
  const id = model.id.trim();
  for (const entry of triggers) {
    const trigger = entry.trim();
    if (!trigger) continue;
    if (fullKey && (trigger === fullKey || trigger.toLowerCase() === fullKey.toLowerCase())) return true;
    if (trigger === id) return true;
    if (!trigger.includes("/") && trigger === id) return true;
    if (trigger.includes("/")) {
      const slash = trigger.indexOf("/");
      const tProvider = trigger.slice(0, slash);
      const tModel = trigger.slice(slash + 1);
      if (tProvider.toLowerCase() === model.provider.toLowerCase() && tModel === id) return true;
    }
  }
  return false;
}

export function loadPresetsConfig(
  env: NodeJS.ProcessEnv = process.env,
  home: string = homedir(),
): { baseUrl: string; presets: PresetSpec[]; routingTriggerModels?: string[] } {
  const envTriggers = parseRoutingTriggerModelsFromEnv(env);
  const configPath = env.PI_ONWATCH_PRESETS_PATH?.trim() || join(home, ".pi", "agent", "pi-onwatch-jev.json");
  const defaultRoutingTriggers = [...DEFAULT_ROUTING_TRIGGER_MODELS];

  if (!existsSync(configPath)) {
    return {
      baseUrl: OPENROUTER_BASE_URL,
      presets: DEFAULT_OPENROUTER_PRESETS.map((p) => ({ ...p })),
      routingTriggerModels: envTriggers ?? defaultRoutingTriggers,
    };
  }
  try {
    const raw = JSON.parse(readFileSync(configPath, "utf8")) as PresetsConfigFile;
    const baseUrl = typeof raw.baseUrl === "string" && raw.baseUrl.trim() ? raw.baseUrl.trim().replace(/\/+$/, "") : OPENROUTER_BASE_URL;
    const presets = Array.isArray(raw.presets) && raw.presets.length > 0
      ? raw.presets.filter(
          (p): p is PresetSpec =>
            Boolean(p) &&
            typeof p.id === "string" &&
            p.id.trim().length > 0 &&
            typeof p.name === "string" &&
            typeof p.contextWindow === "number" &&
            p.contextWindow > 0 &&
            typeof p.maxTokens === "number" &&
            p.maxTokens > 0,
        )
      : DEFAULT_OPENROUTER_PRESETS.map((p) => ({ ...p }));
    const fileTriggers = parseRoutingTriggerModelsFromFile(raw.routingTriggerModels);
    const routingTriggerModels = envTriggers ?? fileTriggers ?? defaultRoutingTriggers;
    return { baseUrl, presets, routingTriggerModels };
  } catch {
    return {
      baseUrl: OPENROUTER_BASE_URL,
      presets: DEFAULT_OPENROUTER_PRESETS.map((p) => ({ ...p })),
      routingTriggerModels: envTriggers ?? defaultRoutingTriggers,
    };
  }
}

export function buildOpenRouterProviderModels(
  presets: readonly PresetSpec[],
  baseUrl: string = OPENROUTER_BASE_URL,
  vettedModels: readonly ProviderModelConfig[] = [],
): ProviderModelConfig[] {
  const chatModels: ProviderModelConfig[] = presets.map((p) => ({
    type: "chat" as const,
    id: p.id,
    name: p.name,
    reasoning: p.reasoning ?? true,
    input: p.input ?? (["text", "image"] as ("text" | "image")[]),
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    contextWindow: p.contextWindow,
    maxTokens: p.maxTokens,
    compat: {
      ...OPENROUTER_COMPAT,
      ...(p.openRouterPresetId
        ? ({ openRouterPresetId: p.openRouterPresetId } as typeof OPENROUTER_COMPAT)
        : {}),
    },
  }));

  const existingIds = new Set(chatModels.map((m) => m.id));
  const additionalVetted: ProviderModelConfig[] = [];
  for (const vm of vettedModels) {
    if (!existingIds.has(vm.id)) {
      existingIds.add(vm.id);
      additionalVetted.push({
        ...vm,
        baseUrl: vm.baseUrl ?? baseUrl,
      });
    }
  }

  const classifier: ProviderModelConfig = {
    ...JEV_CLASSIFIER_MODEL_CONFIG,
    baseUrl,
  };

  return [...chatModels, ...additionalVetted, classifier];
}

export function registerOpenRouterPresets(
  pi: Pick<ExtensionAPI, "registerProvider">,
  options?: { env?: NodeJS.ProcessEnv; home?: string },
): { baseUrl: string; presets: PresetSpec[]; routingTriggerModels?: string[] } {
  const config = loadPresetsConfig(options?.env, options?.home);
  const vettedModels = loadVettedOpenRouterModels(options?.home);
  pi.registerProvider("openrouter", {
    baseUrl: config.baseUrl,
    api: "openai-completions",
    models: buildOpenRouterProviderModels(config.presets, config.baseUrl, vettedModels),
  });
  return config;
}

const PROVIDER_ALIASES: Record<string, readonly string[]> = {
  claude: ["anthropic", "claude"],
  anthropic: ["anthropic", "claude"],
  codex: ["openai-codex", "openai", "codex"],
  "openai-codex": ["openai-codex", "openai"],
  antigravity: ["google-antigravity", "antigravity", "google"],
  "google-antigravity": ["google-antigravity", "antigravity"],
  gemini: ["google-gemini-cli", "google", "gemini"],
  google: ["google", "google-gemini-cli", "google-antigravity"],
  nvidia: ["openrouter", "nvidia"],
  openrouter: ["openrouter"],
};

function normalizeModelIdCandidates(provider: string, model: string): Array<{ provider: string; modelId: string }> {
  const pairs: Array<{ provider: string; modelId: string }> = [];
  const seen = new Set<string>();
  const push = (p: string, m: string) => {
    const key = `${p}\0${m}`;
    if (!seen.has(key) && p && m) {
      seen.add(key);
      pairs.push({ provider: p, modelId: m });
    }
  };

  push(provider, model);

  if (model.includes("/") && !model.startsWith("@preset/")) {
    const slashIdx = model.indexOf("/");
    const prefix = model.slice(0, slashIdx);
    const rest = model.slice(slashIdx + 1);
    push(prefix, rest);
    push(provider, rest);
  }

  const aliases = PROVIDER_ALIASES[provider.toLowerCase()] ?? [provider];
  for (const alias of aliases) {
    push(alias, model);
    if (model.includes("/") && !model.startsWith("@preset/")) {
      push(alias, model.slice(model.indexOf("/") + 1));
    }
  }

  return pairs;
}

export function resolvePiModel(
  modelRegistry: Pick<ModelRegistry, "find" | "getAvailable" | "hasConfiguredAuth">,
  candidate: Pick<RouteCandidate, "provider" | "model" | "id">,
): Model<Api> | undefined {
  let rawProvider = candidate.provider?.trim() ?? "";
  let rawModel = candidate.model?.trim() ?? "";

  if ((!rawProvider || !rawModel) && candidate.id.includes("/")) {
    const idx = candidate.id.indexOf("/");
    rawProvider = rawProvider || candidate.id.slice(0, idx);
    rawModel = rawModel || candidate.id.slice(idx + 1);
  }

  // Handle canonical ids like "openrouter/@preset/code-google" when model already has "@preset/..."
  if (rawProvider === "openrouter" && rawModel.startsWith("openrouter/")) {
    rawModel = rawModel.slice("openrouter/".length);
  }

  for (const { provider, modelId } of normalizeModelIdCandidates(rawProvider, rawModel)) {
    const found = modelRegistry.find(provider, modelId);
    if (found && modelRegistry.hasConfiguredAuth(found)) {
      return found;
    }
  }

  const available = modelRegistry.getAvailable();
  const byFullId = available.find((m) => `${m.provider}/${m.id}` === candidate.id || `${m.provider}/${m.id}` === `${rawProvider}/${rawModel}`);
  if (byFullId) {
    return byFullId;
  }

  const tailModel = rawModel.includes("/") && !rawModel.startsWith("@preset/")
    ? rawModel.slice(rawModel.indexOf("/") + 1)
    : rawModel;
  return available.find((m) => m.id === rawModel || m.id === tailModel);
}
