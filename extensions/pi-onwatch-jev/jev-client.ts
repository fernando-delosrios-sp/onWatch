import { execFileSync } from "node:child_process";
import { existsSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import type { ModelRegistry } from "@earendil-works/pi-coding-agent";
import { OPENROUTER_BASE_URL } from "./presets.ts";
import {
  EFFORTS,
  TIERS,
  type Effort,
  type JevClassifyInput,
  type JevClient,
  type JevDecision,
  type RouteCandidate,
  type Tier,
} from "./types.ts";

export const JEV_MODEL_ID = "~typesafe/jev-latest";
export const DEFAULT_JEV_TIMEOUT_MS = 5000;

export class JevError extends Error {
  readonly kind: "auth" | "timeout" | "unreachable" | "http" | "invalid";
  readonly status?: number;

  constructor(kind: "auth" | "timeout" | "unreachable" | "http" | "invalid", message: string, status?: number) {
    super(message);
    this.name = "JevError";
    this.kind = kind;
    this.status = status;
  }
}

export interface SystemOneChoiceQuestion {
  type: "choice";
  instructions: string;
  criteria: Record<string, string>;
}

export interface SystemOneRequest {
  model: string;
  state: Record<string, unknown>;
  questions: Record<string, SystemOneChoiceQuestion>;
}

export interface JevClientConfig {
  endpointUrl?: string;
  apiKey?: string;
  resolveApiKey?: () => Promise<string | undefined>;
  timeoutMs?: number;
  fetchImpl?: typeof fetch;
  env?: NodeJS.ProcessEnv;
  /** Home directory for ~/.onwatch SQLite fallback lookup (defaults to os.homedir()). */
  home?: string;
}

export function readOnWatchOpenRouterApiKey(home: string = homedir()): string | undefined {
  const dbPath = join(home, ".onwatch", "data", "onwatch.db");
  if (!existsSync(dbPath)) return undefined;
  try {
    const row = execFileSync(
      "sqlite3",
      [dbPath, "SELECT value FROM settings WHERE key='provider_settings' LIMIT 1;"],
      { encoding: "utf8", timeout: 2000 },
    ).trim();
    if (!row) return undefined;
    const parsed = JSON.parse(row) as { openrouter?: { api_key?: string } };
    const key = parsed?.openrouter?.api_key?.trim();
    return key || undefined;
  } catch {
    return undefined;
  }
}

function pushUniqueKey(keys: string[], key: string | undefined): void {
  const trimmed = key?.trim();
  if (!trimmed) return;
  if (!keys.includes(trimmed)) keys.push(trimmed);
}

/** Ordered OpenRouter keys for Jev SystemOne (env, Pi auth.json, onWatch dashboard key). */
export async function resolveOpenRouterApiKeys(options: {
  env?: NodeJS.ProcessEnv;
  modelRegistry?: Pick<ModelRegistry, "getApiKeyForProvider">;
  home?: string;
}): Promise<string[]> {
  const env = options.env ?? process.env;
  const keys: string[] = [];
  pushUniqueKey(keys, env.OPENROUTER_API_KEY);
  if (options.modelRegistry) {
    try {
      pushUniqueKey(keys, await options.modelRegistry.getApiKeyForProvider("openrouter"));
    } catch {
      // fall through
    }
  }
  pushUniqueKey(keys, readOnWatchOpenRouterApiKey(options.home));
  return keys;
}

export async function resolveOpenRouterApiKey(options: {
  env?: NodeJS.ProcessEnv;
  modelRegistry?: Pick<ModelRegistry, "getApiKeyForProvider">;
  home?: string;
}): Promise<string | undefined> {
  const keys = await resolveOpenRouterApiKeys(options);
  return keys[0];
}

export function isOpenRouterGuardrailBlock(status: number, bodyText: string): boolean {
  if (status !== 404) return false;
  const lower = bodyText.toLowerCase();
  return lower.includes("guardrail") || lower.includes("model-ignored-by-guardrail") || lower.includes("data policy");
}

export function formatOpenRouterHttpError(status: number, bodyText: string): string {
  if (isOpenRouterGuardrailBlock(status, bodyText)) {
    let configureUrl = "https://openrouter.ai/settings/privacy";
    try {
      const parsed = JSON.parse(bodyText) as {
        error?: { message?: string; metadata?: { ineligibility_reasons?: { configure_url?: string }[] } };
      };
      const reasons = parsed?.error?.metadata?.ineligibility_reasons;
      const fromMeta = reasons?.find((r) => typeof r.configure_url === "string")?.configure_url;
      if (fromMeta) configureUrl = fromMeta;
    } catch {
      // use default help URL
    }
    return (
      `OpenRouter blocked ~typesafe/jev-latest on this API key (workspace guardrails/data policy). ` +
      `Allow TypeSafe/Jev in OpenRouter guardrails (${configureUrl}) or set OPENROUTER_API_KEY to an unblocked key`
    );
  }
  return `Jev SystemOne returned HTTP ${status}${bodyText ? `: ${bodyText.slice(0, 200)}` : ""}`;
}

export function buildJevRequest(input: JevClassifyInput): SystemOneRequest {
  const questions: Record<string, SystemOneChoiceQuestion> = {
    tier: {
      type: "choice",
      instructions: "Classify the required coding model capability tier for this request.",
      criteria: {
        lightweight:
          "Simple edits, boilerplate, quick Q&A, single-file tweaks, formatting, or straightforward explanations.",
        workhorse:
          "Standard multi-file implementation, tests, refactoring, bug fixes, API wiring, or typical feature work.",
        frontier:
          "Complex architecture, subtle multi-system debugging, ambiguous tradeoffs, large refactors, or high-risk changes.",
      },
    },
    effort: {
      type: "choice",
      instructions: "Classify the reasoning effort needed before responding or editing.",
      criteria: {
        low: "Direct answer or mechanical edit needing minimal deliberation.",
        medium: "Balanced reasoning for standard implementation, verification, or debugging.",
        high: "Deep analysis across edge cases, concurrency, architecture, or hard debugging.",
      },
    },
  };

  for (const tier of TIERS) {
    const candidates = input.candidatesByTier[tier] ?? [];
    if (candidates.length > 1) {
      const criteria: Record<string, string> = {};
      for (const candidate of candidates) {
        criteria[candidate.id] = candidate.description || `${candidate.harness} ${candidate.provider}/${candidate.model}`;
      }
      questions[`${tier}_model`] = {
        type: "choice",
        instructions: `Select the best ${tier} candidate model for this task based on each candidate's strengths and fit.`,
        criteria,
      };
    }
  }

  const state: Record<string, unknown> = {
    mode: input.mode,
    prompt: input.prompt.slice(0, 12000),
  };
  if (input.title) state.title = input.title;
  if (input.description) state.description = input.description.slice(0, 8000);
  if (input.cwd) state.cwd = input.cwd;
  if (input.stickyPool) state.stickyPool = input.stickyPool;
  if (input.currentTier) state.currentTier = input.currentTier;

  return {
    model: JEV_MODEL_ID,
    state,
    questions,
  };
}

const TIER_FALLBACK_ORDER: Record<Tier, readonly Tier[]> = {
  lightweight: ["lightweight", "workhorse", "frontier"],
  workhorse: ["workhorse", "frontier", "lightweight"],
  frontier: ["frontier", "workhorse", "lightweight"],
};

function isTier(value: unknown): value is Tier {
  return value === "lightweight" || value === "workhorse" || value === "frontier";
}

function isEffort(value: unknown): value is Effort {
  return value === "low" || value === "medium" || value === "high";
}

function extractChoice(answer: unknown): {
  choice?: string;
  probabilities?: Record<string, number>;
  confidence?: number;
} {
  if (!answer || typeof answer !== "object") return {};
  const obj = answer as Record<string, unknown>;
  const choice = typeof obj.choice === "string" ? obj.choice : undefined;
  const confidence = typeof obj.confidence === "number" && Number.isFinite(obj.confidence) ? obj.confidence : undefined;
  let probabilities: Record<string, number> | undefined;
  if (obj.probabilities && typeof obj.probabilities === "object" && !Array.isArray(obj.probabilities)) {
    probabilities = {};
    for (const [k, v] of Object.entries(obj.probabilities as Record<string, unknown>)) {
      if (typeof v === "number" && Number.isFinite(v)) probabilities[k] = v;
    }
  }
  return { choice, probabilities, confidence };
}

export function resolveCandidateFromTier(
  tier: Tier,
  candidatesByTier: Partial<Record<Tier, RouteCandidate[]>>,
  modelAnswerChoice: string | undefined,
  stickyPool?: string,
): { resolvedTier: Tier; candidate?: RouteCandidate } {
  for (const fallbackTier of TIER_FALLBACK_ORDER[tier]) {
    const list = candidatesByTier[fallbackTier] ?? [];
    if (list.length === 0) continue;

    if (fallbackTier === tier && modelAnswerChoice) {
      const exact = list.find((c) => c.id === modelAnswerChoice || c.model === modelAnswerChoice);
      if (exact) return { resolvedTier: fallbackTier, candidate: exact };
    }

    if (stickyPool) {
      const sticky = list.find((c) => c.pool === stickyPool);
      if (sticky) return { resolvedTier: fallbackTier, candidate: sticky };
    }

    return { resolvedTier: fallbackTier, candidate: list[0] };
  }

  return { resolvedTier: tier, candidate: undefined };
}

export function parseJevResponse(
  body: unknown,
  input: Pick<JevClassifyInput, "candidatesByTier" | "stickyPool">,
): JevDecision {
  if (!body || typeof body !== "object") {
    throw new JevError("invalid", "Jev SystemOne returned a non-object response");
  }
  const obj = body as Record<string, unknown>;
  const answers = obj.answers;
  if (!answers || typeof answers !== "object") {
    throw new JevError("invalid", "Jev SystemOne response missing answers");
  }
  const answerMap = answers as Record<string, unknown>;

  const tierAns = extractChoice(answerMap.tier);
  const effortAns = extractChoice(answerMap.effort);

  const rawTier: Tier = isTier(tierAns.choice) ? tierAns.choice : "workhorse";
  const effort: Effort = isEffort(effortAns.choice) && EFFORTS.includes(effortAns.choice) ? effortAns.choice : "medium";

  const modelAns = extractChoice(answerMap[`${rawTier}_model`]);
  const { resolvedTier, candidate } = resolveCandidateFromTier(
    rawTier,
    input.candidatesByTier,
    modelAns.choice,
    input.stickyPool,
  );

  return {
    tier: resolvedTier,
    effort,
    chosenModelId: candidate?.id ?? modelAns.choice,
    candidate,
    tierProbabilities: tierAns.probabilities,
    effortProbabilities: effortAns.probabilities,
    modelProbabilities: modelAns.probabilities,
    confidence: {
      tier: tierAns.confidence,
      effort: effortAns.confidence,
      model: modelAns.confidence,
    },
    rawAnswers: answerMap,
  };
}

export class HttpJevClient implements JevClient {
  private readonly endpointUrl: string;
  private readonly explicitKey?: string;
  private readonly resolveKeyFn?: () => Promise<string | undefined>;
  private readonly timeoutMs: number;
  private readonly fetchImpl: typeof fetch;
  private readonly env: NodeJS.ProcessEnv;
  private readonly home: string;

  constructor(config: JevClientConfig = {}) {
    const env = config.env ?? process.env;
    const base = (env.PI_ONWATCH_JEV_URL ?? env.OPENROUTER_BASE_URL ?? OPENROUTER_BASE_URL).trim().replace(/\/+$/, "");
    this.endpointUrl = config.endpointUrl ?? (base.endsWith("/systemone") ? base : `${base}/systemone`);
    this.explicitKey = config.apiKey;
    this.resolveKeyFn = config.resolveApiKey;
    this.timeoutMs = config.timeoutMs ?? DEFAULT_JEV_TIMEOUT_MS;
    this.fetchImpl = config.fetchImpl ?? globalThis.fetch;
    this.env = env;
    this.home = config.home ?? homedir();
  }

  async classify(input: JevClassifyInput): Promise<JevDecision> {
    const apiKeys: string[] = [];
    pushUniqueKey(apiKeys, this.explicitKey);
    pushUniqueKey(apiKeys, this.env.OPENROUTER_API_KEY);
    if (this.resolveKeyFn) {
      const resolved = await this.resolveKeyFn();
      pushUniqueKey(apiKeys, resolved);
    }
    pushUniqueKey(apiKeys, readOnWatchOpenRouterApiKey(this.home));

    if (apiKeys.length === 0) {
      throw new JevError(
        "auth",
        "No OpenRouter API key available (OPENROUTER_API_KEY, Pi auth.json, or onWatch OpenRouter settings)",
      );
    }

    const payload = buildJevRequest(input);
    const timeout = AbortSignal.timeout(this.timeoutMs);
    const signal = input.signal ? AbortSignal.any([input.signal, timeout]) : timeout;

    let lastHttpError: JevError | undefined;

    for (const apiKey of apiKeys) {
      let response: Response;
      try {
        response = await this.fetchImpl(this.endpointUrl, {
          method: "POST",
          headers: {
            authorization: `Bearer ${apiKey}`,
            "content-type": "application/json",
            accept: "application/json",
          },
          body: JSON.stringify(payload),
          signal,
        });
      } catch (err) {
        const e = err as { name?: string; message?: string };
        if (e?.name === "TimeoutError" || e?.name === "AbortError") {
          throw new JevError("timeout", `Jev SystemOne timed out after ${this.timeoutMs}ms`);
        }
        throw new JevError("unreachable", `Jev SystemOne request failed: ${e?.message ?? String(err)}`);
      }

      if (response.status === 401 || response.status === 403) {
        throw new JevError("auth", `Jev SystemOne rejected OpenRouter auth (${response.status})`, response.status);
      }
      if (!response.ok) {
        const text = await response.text().catch(() => "");
        const formatted = formatOpenRouterHttpError(response.status, text);
        lastHttpError = new JevError("http", formatted, response.status);
        if (isOpenRouterGuardrailBlock(response.status, text) && apiKeys.length > 1) {
          continue;
        }
        throw lastHttpError;
      }

      let body: unknown;
      try {
        body = await response.json();
      } catch {
        throw new JevError("invalid", "Jev SystemOne returned invalid JSON", response.status);
      }

      return parseJevResponse(body, input);
    }

    throw lastHttpError ?? new JevError("http", "Jev SystemOne request failed");
  }
}
