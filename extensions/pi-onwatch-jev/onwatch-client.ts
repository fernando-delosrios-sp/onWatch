import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import {
  TIERS,
  type CandidateStatus,
  type OnWatchClient,
  type RouteCandidate,
  type RouteFeedback,
  type RouteQueryOptions,
  type RouteResponse,
  type Tier,
} from "./types.ts";

export const DEFAULT_ONWATCH_PORT = 9211;
export const DEFAULT_ONWATCH_TIMEOUT_MS = 3000;

export interface OnWatchCredentials {
  username: string;
  password: string;
}

export interface OnWatchClientConfig {
  baseUrl?: string;
  credentials?: OnWatchCredentials;
  timeoutMs?: number;
  fetchImpl?: typeof fetch;
  env?: NodeJS.ProcessEnv;
  home?: string;
  platform?: string;
}

export class OnWatchError extends Error {
  readonly kind: "unreachable" | "timeout" | "auth" | "http" | "invalid";
  readonly status?: number;
  readonly url: string;

  constructor(
    kind: "unreachable" | "timeout" | "auth" | "http" | "invalid",
    message: string,
    url: string,
    status?: number,
  ) {
    super(message);
    this.name = "OnWatchError";
    this.kind = kind;
    this.url = url;
    this.status = status;
  }
}

export function parsePort(text: string | undefined | null): number | undefined {
  if (!text) return undefined;
  const trimmed = text.trim();
  if (!/^\d{1,5}$/.test(trimmed)) return undefined;
  const port = Number(trimmed);
  return port >= 1 && port <= 65535 ? port : undefined;
}

export function normalizeUrl(input: string | undefined): string | undefined {
  const trimmed = (input ?? "").trim();
  if (!trimmed) return undefined;
  try {
    const parsed = new URL(trimmed);
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return undefined;
    parsed.search = "";
    parsed.hash = "";
    return parsed.toString().replace(/\/+$/, "");
  } catch {
    return undefined;
  }
}

export function portFileCandidates(
  platform: string = process.platform,
  env: NodeJS.ProcessEnv = process.env,
  home: string = homedir(),
): string[] {
  const candidates: string[] = [];
  if (platform === "win32") {
    const localAppData = (env.LOCALAPPDATA ?? "").trim();
    if (localAppData) candidates.push(join(localAppData, "onwatch", "port"));
  }
  candidates.push(join(home, ".onwatch", "port"));
  return candidates;
}

export function discoverOnWatchBaseUrl(
  env: NodeJS.ProcessEnv = process.env,
  home: string = homedir(),
  platform: string = process.platform,
): string {
  const fromEnvUrl = normalizeUrl(env.ONWATCH_URL) ?? normalizeUrl(env.PI_ONWATCH_URL);
  if (fromEnvUrl) return fromEnvUrl;

  for (const candidatePath of portFileCandidates(platform, env, home)) {
    try {
      const port = parsePort(readFileSync(candidatePath, "utf8"));
      if (port !== undefined) {
        return `http://127.0.0.1:${port}`;
      }
    } catch {
      // try next candidate
    }
  }

  const fromEnvPort = parsePort(env.ONWATCH_PORT);
  if (fromEnvPort !== undefined) {
    return `http://127.0.0.1:${fromEnvPort}`;
  }

  return `http://127.0.0.1:${DEFAULT_ONWATCH_PORT}`;
}

export function parseDotEnv(content: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const rawLine of content.split(/\r?\n/)) {
    const line = rawLine.trim();
    if (!line || line.startsWith("#")) continue;
    const eq = line.indexOf("=");
    if (eq <= 0) continue;
    const key = line.slice(0, eq).trim();
    let val = line.slice(eq + 1).trim();
    if ((val.startsWith('"') && val.endsWith('"')) || (val.startsWith("'") && val.endsWith("'"))) {
      val = val.slice(1, -1);
    }
    out[key] = val;
  }
  return out;
}

export function discoverOnWatchCredentials(
  env: NodeJS.ProcessEnv = process.env,
  home: string = homedir(),
): OnWatchCredentials | undefined {
  const envUser = (env.ONWATCH_USERNAME ?? env.ONWATCH_ADMIN_USER ?? "").trim();
  const envPass = env.ONWATCH_PASSWORD ?? env.ONWATCH_ADMIN_PASS ?? "";
  if (envUser && envPass) {
    return { username: envUser, password: envPass };
  }

  try {
    const parsed = parseDotEnv(readFileSync(join(home, ".onwatch", ".env"), "utf8"));
    const fileUser = (parsed.ONWATCH_ADMIN_USER ?? "").trim();
    const filePass = parsed.ONWATCH_ADMIN_PASS ?? "";
    if (fileUser && filePass) {
      return { username: fileUser, password: filePass };
    }
  } catch {
    // no .env file or unreadable
  }
  return undefined;
}

function basicAuthHeader(credentials: OnWatchCredentials | undefined): string | undefined {
  if (!credentials?.username) return undefined;
  return `Basic ${Buffer.from(`${credentials.username}:${credentials.password}`, "utf8").toString("base64")}`;
}

function asTier(value: unknown, fallback: Tier = "workhorse"): Tier {
  return value === "lightweight" || value === "workhorse" || value === "frontier" ? value : fallback;
}

function asStatus(value: unknown): CandidateStatus | undefined {
  if (value === "ready" || value === "ahead" || value === "capped" || value === "cooling" || value === "unknown") {
    return value;
  }
  return undefined;
}

function splitProviderAndModel(rawModel: string, rawProvider?: string): { provider: string; model: string } {
  const provider = (rawProvider ?? "").trim();
  const model = rawModel.trim();
  if (provider) {
    if (model.startsWith(`${provider}/`) && !model.slice(provider.length + 1).includes("/")) {
      return { provider, model: model.slice(provider.length + 1) };
    }
    if (provider === "openrouter" && model.startsWith("openrouter/")) {
      return { provider, model: model.slice("openrouter/".length) };
    }
    return { provider, model };
  }
  const slash = model.indexOf("/");
  if (slash > 0) {
    return { provider: model.slice(0, slash), model: model.slice(slash + 1) };
  }
  return { provider: "openrouter", model };
}

export function normalizeCandidate(raw: unknown, defaultTier: Tier = "workhorse"): RouteCandidate | undefined {
  if (!raw || typeof raw !== "object") return undefined;
  const r = raw as Record<string, unknown>;
  const rawModel = typeof r.model === "string" ? r.model : typeof r.id === "string" ? r.id : "";
  if (!rawModel.trim()) return undefined;

  const harness = typeof r.harness === "string" && r.harness.trim() ? r.harness.trim() : "pi";
  const rawProvider = typeof r.provider === "string" && r.provider.trim() ? r.provider.trim() : undefined;
  const { provider, model } = splitProviderAndModel(rawModel, rawProvider);
  const tier = asTier(r.tier, defaultTier);
  const status = asStatus(r.status);

  if (status === "capped" || status === "cooling") {
    return undefined;
  }

  const canonicalId =
    typeof r.id === "string" && r.id.trim()
      ? r.id.trim()
      : harness === "pi"
        ? `${provider}/${model}`
        : `${harness}/${model}`;

  const description =
    typeof r.description === "string" && r.description.trim()
      ? r.description.trim()
      : `${harness} ${provider}/${model}`;

  const pool = typeof r.pool === "string" && r.pool.trim() ? r.pool.trim() : undefined;
  const weight = typeof r.weight === "number" && Number.isFinite(r.weight) ? r.weight : undefined;

  const capsRaw = r.capabilities && typeof r.capabilities === "object" ? (r.capabilities as Record<string, unknown>) : undefined;
  const capabilities = capsRaw
    ? {
        reasoning: typeof capsRaw.reasoning === "boolean" ? capsRaw.reasoning : undefined,
        thinkingLevels: Array.isArray(capsRaw.thinkingLevels)
          ? capsRaw.thinkingLevels.filter((v): v is string => typeof v === "string")
          : undefined,
        contextWindow: typeof capsRaw.contextWindow === "number" ? capsRaw.contextWindow : undefined,
      }
    : undefined;

  return {
    harness,
    provider,
    model,
    id: canonicalId,
    tier,
    description,
    pool,
    status,
    weight,
    capabilities,
  };
}

export function normalizeRouteResponse(raw: unknown): RouteResponse {
  if (!raw || typeof raw !== "object") {
    throw new Error("Invalid /api/route JSON payload");
  }
  const obj = raw as Record<string, unknown>;
  const tiers: Partial<Record<Tier, RouteCandidate[]>> = {};

  if (obj.tiers && typeof obj.tiers === "object" && !Array.isArray(obj.tiers)) {
    const rawTiers = obj.tiers as Record<string, unknown>;
    for (const tier of TIERS) {
      const list = rawTiers[tier];
      if (!Array.isArray(list)) continue;
      const normalized = list
        .map((item) => normalizeCandidate(item, tier))
        .filter((item): item is RouteCandidate => Boolean(item));
      if (normalized.length > 0) tiers[tier] = normalized;
    }
  } else if (Array.isArray(obj.candidates)) {
    for (const item of obj.candidates) {
      const normalized = normalizeCandidate(item, "workhorse");
      if (!normalized) continue;
      tiers[normalized.tier] ??= [];
      tiers[normalized.tier]!.push(normalized);
    }
  }

  return {
    tiers,
    stickyPool: typeof obj.stickyPool === "string" && obj.stickyPool.trim() ? obj.stickyPool.trim() : undefined,
    stale: typeof obj.stale === "boolean" ? obj.stale : undefined,
    reason: typeof obj.reason === "string" && obj.reason.trim() ? obj.reason.trim() : undefined,
  };
}

export class HttpOnWatchClient implements OnWatchClient {
  private readonly baseUrl: string;
  private readonly credentials?: OnWatchCredentials;
  private readonly timeoutMs: number;
  private readonly fetchImpl: typeof fetch;

  constructor(config: OnWatchClientConfig = {}) {
    const env = config.env ?? process.env;
    const home = config.home ?? homedir();
    const platform = config.platform ?? process.platform;
    this.baseUrl = normalizeUrl(config.baseUrl) ?? discoverOnWatchBaseUrl(env, home, platform);
    this.credentials = config.credentials ?? discoverOnWatchCredentials(env, home);
    this.timeoutMs = config.timeoutMs ?? DEFAULT_ONWATCH_TIMEOUT_MS;
    this.fetchImpl = config.fetchImpl ?? globalThis.fetch;
  }

  getBaseUrl(): string {
    return this.baseUrl;
  }

  async getRoute(options: RouteQueryOptions): Promise<RouteResponse> {
    const params = new URLSearchParams();
    if (options.harnesses.length > 0) {
      params.set("harnesses", options.harnesses.join(","));
    }
    if (options.tier) {
      params.set("tier", options.tier);
    }
    if (options.pool) {
      params.set("pool", options.pool);
    }
    if (options.exclude && options.exclude.length > 0) {
      params.set("exclude", options.exclude.join(","));
    }

    const url = `${this.baseUrl}/api/route?${params.toString()}`;
    const headers: Record<string, string> = { accept: "application/json" };
    const auth = basicAuthHeader(this.credentials);
    if (auth) headers.authorization = auth;

    const signal = combineSignals(options.signal, this.timeoutMs);
    let response: Response;
    try {
      response = await this.fetchImpl(url, { method: "GET", headers, signal });
    } catch (err) {
      throw classifyFetchError(err, url);
    }

    if (response.status === 401 || response.status === 403) {
      throw new OnWatchError("auth", `onWatch authentication rejected (${response.status})`, url, response.status);
    }
    if (!response.ok) {
      throw new OnWatchError("http", `onWatch /api/route returned HTTP ${response.status}`, url, response.status);
    }

    let body: unknown;
    try {
      body = await response.json();
    } catch {
      throw new OnWatchError("invalid", "onWatch /api/route returned invalid JSON", url, response.status);
    }

    try {
      return normalizeRouteResponse(body);
    } catch (err) {
      throw new OnWatchError(
        "invalid",
        err instanceof Error ? err.message : "Invalid /api/route payload",
        url,
        response.status,
      );
    }
  }

  async postFeedback(feedback: RouteFeedback, callerSignal?: AbortSignal): Promise<void> {
    const url = `${this.baseUrl}/api/route/feedback`;
    const headers: Record<string, string> = {
      "content-type": "application/json",
      accept: "application/json",
    };
    const auth = basicAuthHeader(this.credentials);
    if (auth) headers.authorization = auth;

    const signal = combineSignals(callerSignal, this.timeoutMs);
    let response: Response;
    try {
      response = await this.fetchImpl(url, {
        method: "POST",
        headers,
        body: JSON.stringify(feedback),
        signal,
      });
    } catch (err) {
      throw classifyFetchError(err, url);
    }

    if (response.status === 401 || response.status === 403) {
      throw new OnWatchError("auth", `onWatch feedback auth rejected (${response.status})`, url, response.status);
    }
    if (!response.ok) {
      throw new OnWatchError("http", `onWatch /api/route/feedback returned HTTP ${response.status}`, url, response.status);
    }
  }
}

function combineSignals(callerSignal: AbortSignal | undefined, timeoutMs: number): AbortSignal {
  const timeout = AbortSignal.timeout(timeoutMs);
  return callerSignal ? AbortSignal.any([callerSignal, timeout]) : timeout;
}

function classifyFetchError(err: unknown, url: string): OnWatchError {
  if (err instanceof OnWatchError) return err;
  const e = err as { name?: string; message?: string; cause?: { code?: string; message?: string } };
  if (e?.name === "TimeoutError" || e?.name === "AbortError") {
    return new OnWatchError("timeout", `Timed out contacting onWatch at ${url}`, url);
  }
  const detail = e?.cause?.code ?? e?.cause?.message ?? e?.message ?? String(err);
  return new OnWatchError("unreachable", `Could not reach onWatch at ${url} (${detail})`, url);
}
