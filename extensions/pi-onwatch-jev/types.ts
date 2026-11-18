export type Tier = "lightweight" | "workhorse" | "frontier";
export type Effort = "low" | "medium" | "high";

export type CandidateStatus = "ready" | "ahead" | "capped" | "cooling" | "unknown";

export const TIERS: readonly Tier[] = ["lightweight", "workhorse", "frontier"] as const;
export const EFFORTS: readonly Effort[] = ["low", "medium", "high"] as const;

export const ALLOWED_LINEAR_HARNESSES: readonly string[] = [
  "pi",
  "cursor",
  "antigravity",
  "grok",
  "claudeAgent",
  "codex",
] as const;

export interface RouteCandidate {
  harness: string;
  provider: string;
  model: string;
  id: string;
  tier: Tier;
  description: string;
  pool?: string;
  overlay?: string;
  billing?: string;
  underlyingModel?: string;
  underlyingModels?: string[];
  status?: CandidateStatus;
  weight?: number;
  capabilities?: {
    reasoning?: boolean;
    thinkingLevels?: string[];
    contextWindow?: number;
  };
}

export interface RouteResponse {
  tiers: Partial<Record<Tier, RouteCandidate[]>>;
  stickyPool?: string;
  stale?: boolean;
  reason?: string;
}

export type FeedbackOutcome = "ok" | "rate_limit" | "error" | "demoted";

export interface RouteFeedback {
  harness: string;
  provider: string;
  model: string;
  pool?: string;
  outcome: FeedbackOutcome;
  statusCode?: number;
  ttftMs?: number;
  latencyMs?: number;
  reason?: string;
}

export interface RouteQueryOptions {
  harnesses: readonly string[];
  tier?: Tier;
  pool?: string;
  exclude?: readonly string[];
  excludeFree?: boolean;
  signal?: AbortSignal;
}

export interface OnWatchClient {
  getRoute(options: RouteQueryOptions): Promise<RouteResponse>;
  postFeedback(feedback: RouteFeedback, signal?: AbortSignal): Promise<void>;
}

export interface JevClassifyInput {
  mode: "pi" | "linear_dispatch";
  prompt: string;
  title?: string;
  description?: string;
  cwd?: string;
  stickyPool?: string;
  currentTier?: Tier;
  candidatesByTier: Partial<Record<Tier, RouteCandidate[]>>;
  signal?: AbortSignal;
}

export interface JevDecision {
  tier: Tier;
  effort: Effort;
  chosenModelId?: string;
  candidate?: RouteCandidate;
  tierProbabilities?: Record<string, number>;
  effortProbabilities?: Record<string, number>;
  modelProbabilities?: Record<string, number>;
  confidence?: {
    tier?: number;
    effort?: number;
    model?: number;
  };
  rawAnswers?: Record<string, unknown>;
}

export interface JevClient {
  classify(input: JevClassifyInput): Promise<JevDecision>;
}

export interface TurnTelemetry {
  startMs: number;
  firstTokenMs?: number;
  statusCode?: number;
  errorMessage?: string;
  candidate?: RouteCandidate;
}

export interface SessionRoutingState {
  stickyPool?: string;
  currentTier?: Tier;
  currentEffort?: Effort;
  currentCandidate?: RouteCandidate;
  currentModelKey?: string;
  excludedKeys: Set<string>;
  excludedPools: Set<string>;
  fallbackActive: boolean;
  fallbackReason?: string;
  lastTurn?: TurnTelemetry;
  pendingIntakeSummary?: string;
}

export interface CliResult {
  code: number;
  stdout: string;
  stderr: string;
}

export interface DispatcherCli {
  run(args: string[], options?: { stdin?: string; timeoutMs?: number; signal?: AbortSignal }): Promise<CliResult>;
}
