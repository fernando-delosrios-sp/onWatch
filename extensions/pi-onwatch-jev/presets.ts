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
}

/**
 * Simplified 5 per-provider / PAYG presets plus @preset/code-auto,
 * and @preset/code-fast-free retained during transition for $0 intake sessions.
 */
export const DEFAULT_OPENROUTER_PRESETS: readonly PresetSpec[] = [
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
    name: "Chutes PAYG",
    contextWindow: 202752,
    maxTokens: 32768,
  },
  {
    id: "@preset/code-payg",
    name: "OpenRouter PAYG",
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

export interface PresetsConfigFile {
  baseUrl?: string;
  presets?: PresetSpec[];
}

export function loadPresetsConfig(
  env: NodeJS.ProcessEnv = process.env,
  home: string = homedir(),
): { baseUrl: string; presets: PresetSpec[] } {
  const configPath = env.PI_ONWATCH_PRESETS_PATH?.trim() || join(home, ".pi", "agent", "pi-onwatch-jev.json");
  if (!existsSync(configPath)) {
    return {
      baseUrl: OPENROUTER_BASE_URL,
      presets: DEFAULT_OPENROUTER_PRESETS.map((p) => ({ ...p })),
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
    return { baseUrl, presets };
  } catch {
    return {
      baseUrl: OPENROUTER_BASE_URL,
      presets: DEFAULT_OPENROUTER_PRESETS.map((p) => ({ ...p })),
    };
  }
}

export function buildOpenRouterProviderModels(presets: readonly PresetSpec[], baseUrl: string = OPENROUTER_BASE_URL): ProviderModelConfig[] {
  const chatModels: ProviderModelConfig[] = presets.map((p) => ({
    type: "chat" as const,
    id: p.id,
    name: p.name,
    reasoning: p.reasoning ?? true,
    input: p.input ?? (["text", "image"] as ("text" | "image")[]),
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    contextWindow: p.contextWindow,
    maxTokens: p.maxTokens,
    compat: OPENROUTER_COMPAT,
  }));

  const classifier: ProviderModelConfig = {
    ...JEV_CLASSIFIER_MODEL_CONFIG,
    baseUrl,
  };

  return [...chatModels, classifier];
}

export function registerOpenRouterPresets(
  pi: Pick<ExtensionAPI, "registerProvider">,
  options?: { env?: NodeJS.ProcessEnv; home?: string },
): { baseUrl: string; presets: PresetSpec[] } {
  const config = loadPresetsConfig(options?.env, options?.home);
  pi.registerProvider("openrouter", {
    baseUrl: config.baseUrl,
    api: "openai-completions",
    models: buildOpenRouterProviderModels(config.presets, config.baseUrl),
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
