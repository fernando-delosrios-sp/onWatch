import type { ExtensionAPI, ExtensionCommandContext, ExtensionContext } from "@earendil-works/pi-coding-agent";
import { isFreeRouteCandidate } from "./onwatch-client.ts";
import { isJevRoutingShellCandidate, resolvePiModel } from "./presets.ts";
import {
  TIERS,
  type Effort,
  type FeedbackOutcome,
  type JevClient,
  type JevDecision,
  type OnWatchClient,
  type RouteCandidate,
  type RouteFeedback,
  type RouteResponse,
  type SessionRoutingState,
  type Tier,
} from "./types.ts";

export function createInitialSessionState(): SessionRoutingState {
  return {
    excludedKeys: new Set<string>(),
    excludedPools: new Set<string>(),
    fallbackActive: false,
  };
}

/** Clear per-turn routing state when the user selected a non-trigger model (manual provider control). */
export function clearManualRoutingSession(state: SessionRoutingState): void {
  state.currentTier = undefined;
  state.currentEffort = undefined;
  state.currentCandidate = undefined;
  state.currentModelKey = undefined;
  state.lastTurn = undefined;
  state.fallbackActive = false;
  state.fallbackReason = undefined;
}

export function candidateKey(candidate: Pick<RouteCandidate, "provider" | "model">): string {
  return `${candidate.provider}/${candidate.model}`;
}

export function extractPromptText(prompt: string): string {
  return prompt.trim();
}

export function filterEligibleTiers(
  route: RouteResponse,
  state: Pick<SessionRoutingState, "excludedKeys" | "excludedPools">,
  options?: { excludeFree?: boolean },
): Partial<Record<Tier, RouteCandidate[]>> {
  const excludeFree = options?.excludeFree ?? true;
  const out: Partial<Record<Tier, RouteCandidate[]>> = {};
  for (const tier of TIERS) {
    const list = route.tiers[tier] ?? [];
    const eligible = list.filter((c) => {
      if (isJevRoutingShellCandidate(c)) return false;
      if (excludeFree && isFreeRouteCandidate(c)) return false;
      if (c.status === "capped" || c.status === "cooling") return false;
      if (state.excludedKeys.has(candidateKey(c)) || state.excludedKeys.has(c.id)) return false;
      if (c.pool && state.excludedPools.has(c.pool)) return false;
      return true;
    });
    if (eligible.length > 0) out[tier] = eligible;
  }
  return out;
}

export function applyStickyPoolFilter(
  eligibleByTier: Partial<Record<Tier, RouteCandidate[]>>,
  stickyPool: string | undefined,
): {
  scopedByTier: Partial<Record<Tier, RouteCandidate[]>>;
  poolDropped: boolean;
} {
  if (!stickyPool) {
    return { scopedByTier: eligibleByTier, poolDropped: false };
  }

  let hasAnySticky = false;
  const stickyOnly: Partial<Record<Tier, RouteCandidate[]>> = {};

  for (const tier of TIERS) {
    const list = eligibleByTier[tier] ?? [];
    const matching = list.filter((c) => c.pool === stickyPool);
    if (matching.length > 0) {
      hasAnySticky = true;
      stickyOnly[tier] = matching;
    } else if (list.length > 0) {
      // Keep non-sticky candidates for tiers where the sticky pool has no model,
      // so frontier/lightweight escalation still works if sticky pool is tier-narrow.
      stickyOnly[tier] = list;
    }
  }

  if (!hasAnySticky) {
    return { scopedByTier: eligibleByTier, poolDropped: true };
  }

  return { scopedByTier: stickyOnly, poolDropped: false };
}

export interface RouteApplyResult {
  action: "unchanged" | "switched" | "fallback";
  candidate?: RouteCandidate;
  decision?: JevDecision;
  modelSwitched: boolean;
  thinkingSwitched: boolean;
  poolDropped: boolean;
  reason?: string;
}

export interface RouterDeps {
  onWatch: OnWatchClient;
  jev: JevClient;
  state: SessionRoutingState;
  pi: Pick<ExtensionAPI, "setModel" | "setThinkingLevel" | "getThinkingLevel">;
}

function notifyFallback(ctx: Pick<ExtensionContext, "hasUI" | "ui">, reason: string): void {
  if (ctx.hasUI) {
    ctx.ui.setStatus("pi-onwatch-jev", `fallback: ${reason}`);
    ctx.ui.notify(`pi-onwatch-jev fallback (${reason}); using harness default model.`, "warning");
  }
}

function updateStatus(
  ctx: Pick<ExtensionContext, "hasUI" | "ui">,
  candidate: RouteCandidate | undefined,
  decision: JevDecision | undefined,
): void {
  if (!ctx.hasUI || !candidate || !decision) return;
  const poolPart = candidate.pool ? ` [${candidate.pool}]` : "";
  ctx.ui.setStatus(
    "pi-onwatch-jev",
    `${decision.tier}/${decision.effort} → ${candidate.provider}/${candidate.model}${poolPart}`,
  );
}

export async function routePiTurn(options: {
  prompt: string;
  cwd?: string;
  ctx: Pick<ExtensionContext, "model" | "modelRegistry" | "thinkingLevel" | "hasUI" | "ui">;
  deps: RouterDeps;
  forceReroute?: boolean;
  signal?: AbortSignal;
}): Promise<RouteApplyResult> {
  const { prompt, cwd, ctx, deps, forceReroute = false, signal } = options;
  const { onWatch, jev, state, pi } = deps;

  if (forceReroute || state.stickyPool === "nvidia-free" || state.stickyPool === "nvidia") {
    state.stickyPool = undefined;
  }

  let route: RouteResponse;
  try {
    route = await onWatch.getRoute({
      harnesses: ["pi"],
      pool: state.stickyPool,
      exclude: [...state.excludedKeys],
      excludeFree: true,
      signal,
    });
  } catch (err) {
    const reason = err instanceof Error ? err.message : "onWatch unreachable";
    state.fallbackActive = true;
    state.fallbackReason = reason;
    notifyFallback(ctx, reason);
    return { action: "fallback", modelSwitched: false, thinkingSwitched: false, poolDropped: false, reason };
  }

  if (route.stale) {
    const reason = route.reason || "onWatch quota data is stale";
    state.fallbackActive = true;
    state.fallbackReason = reason;
    notifyFallback(ctx, reason);
    return { action: "fallback", modelSwitched: false, thinkingSwitched: false, poolDropped: false, reason };
  }

  const eligibleByTier = filterEligibleTiers(route, state, { excludeFree: true });
  const totalEligible = TIERS.reduce((acc, t) => acc + (eligibleByTier[t]?.length ?? 0), 0);
  if (totalEligible === 0) {
    const reason = "onWatch returned no eligible Pi candidates";
    state.fallbackActive = true;
    state.fallbackReason = reason;
    notifyFallback(ctx, reason);
    return { action: "fallback", modelSwitched: false, thinkingSwitched: false, poolDropped: false, reason };
  }

  const { scopedByTier, poolDropped } = applyStickyPoolFilter(eligibleByTier, state.stickyPool);
  if (poolDropped) {
    state.stickyPool = undefined;
  } else if (!state.stickyPool && route.stickyPool && !state.excludedPools.has(route.stickyPool)) {
    state.stickyPool = route.stickyPool;
  }

  let decision: JevDecision;
  try {
    decision = await jev.classify({
      mode: "pi",
      prompt: extractPromptText(prompt),
      cwd,
      stickyPool: state.stickyPool,
      currentTier: state.currentTier,
      candidatesByTier: scopedByTier,
      signal,
    });
  } catch (err) {
    const reason = err instanceof Error ? err.message : "Jev classifier failed";
    state.fallbackActive = true;
    state.fallbackReason = reason;
    notifyFallback(ctx, reason);
    return { action: "fallback", modelSwitched: false, thinkingSwitched: false, poolDropped, reason };
  }

  // Try the chosen candidate first, then remaining candidates in the resolved tier, then other tiers.
  const orderedCandidates = buildCandidateFallbackList(decision, scopedByTier);
  for (const candidate of orderedCandidates) {
    const piModel = resolvePiModel(ctx.modelRegistry, candidate);
    if (!piModel) continue;

    const targetKey = `${piModel.provider}/${piModel.id}`;
    const currentKey = ctx.model ? `${ctx.model.provider}/${ctx.model.id}` : undefined;
    const modelChanged = currentKey !== targetKey;
    const currentThinking = ctx.thinkingLevel ?? pi.getThinkingLevel();
    const effortChanged = currentThinking !== decision.effort;
    const capabilityOrTierChanged = state.currentTier !== decision.tier || modelChanged || poolDropped;

    let modelSwitched = false;
    let thinkingSwitched = false;

    if (modelChanged) {
      const ok = await pi.setModel(piModel);
      if (!ok) {
        // Missing auth at runtime: try next candidate
        continue;
      }
      modelSwitched = true;
      if (piModel.reasoning) {
        pi.setThinkingLevel(decision.effort);
        thinkingSwitched = true;
      }
    } else if (piModel.reasoning && (effortChanged || (capabilityOrTierChanged && currentThinking !== decision.effort))) {
      pi.setThinkingLevel(decision.effort);
      thinkingSwitched = true;
    }

    state.fallbackActive = false;
    state.fallbackReason = undefined;
    state.currentTier = decision.tier;
    state.currentEffort = decision.effort;
    state.currentCandidate = candidate;
    state.currentModelKey = targetKey;
    if (candidate.pool) {
      state.stickyPool = candidate.pool;
    }

    updateStatus(ctx, candidate, decision);
    return {
      action: modelSwitched || thinkingSwitched ? "switched" : "unchanged",
      candidate,
      decision,
      modelSwitched,
      thinkingSwitched,
      poolDropped,
    };
  }

  const reason = "No resolved Pi model had valid authentication";
  state.fallbackActive = true;
  state.fallbackReason = reason;
  notifyFallback(ctx, reason);
  return { action: "fallback", modelSwitched: false, thinkingSwitched: false, poolDropped, reason };
}

function buildCandidateFallbackList(
  decision: JevDecision,
  scopedByTier: Partial<Record<Tier, RouteCandidate[]>>,
): RouteCandidate[] {
  const result: RouteCandidate[] = [];
  const seen = new Set<string>();
  const push = (c: RouteCandidate | undefined) => {
    if (!c) return;
    const key = `${c.harness}:${c.provider}/${c.model}`;
    if (!seen.has(key)) {
      seen.add(key);
      result.push(c);
    }
  };

  push(decision.candidate);
  for (const c of scopedByTier[decision.tier] ?? []) push(c);
  for (const tier of TIERS) {
    if (tier === decision.tier) continue;
    for (const c of scopedByTier[tier] ?? []) push(c);
  }
  return result;
}

export function isRateLimitError(statusCode: number | undefined, errorMessage: string | undefined): boolean {
  if (statusCode === 429) return true;
  if (!errorMessage) return false;
  return /\b429\b|rate[_ -]?limit|too many requests|quota exceeded/i.test(errorMessage);
}

export async function recordTurnEndFeedback(options: {
  state: SessionRoutingState;
  onWatch: OnWatchClient;
  outcomeOverride?: FeedbackOutcome;
  errorMessage?: string;
  statusCode?: number;
  nowMs?: number;
}): Promise<void> {
  const { state, onWatch, outcomeOverride, errorMessage, statusCode, nowMs = Date.now() } = options;
  const turn = state.lastTurn;
  const candidate = turn?.candidate ?? state.currentCandidate;
  if (!candidate) return;

  const effectiveStatus = statusCode ?? turn?.statusCode;
  const effectiveError = errorMessage ?? turn?.errorMessage;

  let outcome: FeedbackOutcome = outcomeOverride ?? "ok";
  if (!outcomeOverride) {
    if (isRateLimitError(effectiveStatus, effectiveError)) {
      outcome = "rate_limit";
    } else if (effectiveError || (effectiveStatus !== undefined && effectiveStatus >= 400)) {
      outcome = "error";
    }
  }

  if (outcome === "rate_limit" || outcome === "error" || outcome === "demoted") {
    state.excludedKeys.add(candidateKey(candidate));
    if (candidate.pool && candidate.pool === state.stickyPool) {
      state.excludedPools.add(candidate.pool);
      state.stickyPool = undefined;
    }
  }

  const latencyMs = turn?.startMs ? Math.max(0, nowMs - turn.startMs) : undefined;
  const ttftMs = turn?.startMs && turn?.firstTokenMs ? Math.max(0, turn.firstTokenMs - turn.startMs) : undefined;

  const feedback: RouteFeedback = {
    harness: candidate.harness,
    provider: candidate.provider,
    model: candidate.model,
    pool: candidate.pool,
    outcome,
    statusCode: effectiveStatus,
    ttftMs,
    latencyMs,
    reason: effectiveError,
  };

  try {
    await onWatch.postFeedback(feedback);
  } catch {
    // Feedback is best-effort and must never break a turn.
  } finally {
    state.lastTurn = undefined;
  }
}

export async function handleBadCommand(options: {
  args: string;
  ctx: Pick<ExtensionCommandContext, "model" | "modelRegistry" | "thinkingLevel" | "hasUI" | "ui">;
  deps: RouterDeps;
}): Promise<RouteApplyResult> {
  const { args, ctx, deps } = options;
  const { state, onWatch } = deps;
  const reason = args.trim() || "user ran /bad";

  const demoted = state.currentCandidate ?? (ctx.model
    ? {
        harness: "pi",
        provider: ctx.model.provider,
        model: ctx.model.id,
        id: `${ctx.model.provider}/${ctx.model.id}`,
        tier: state.currentTier ?? "workhorse",
        description: ctx.model.name ?? ctx.model.id,
        pool: state.stickyPool,
      }
    : undefined);

  if (demoted) {
    state.excludedKeys.add(candidateKey(demoted));
    state.excludedKeys.add(demoted.id);
    if (demoted.pool) state.excludedPools.add(demoted.pool);
    state.stickyPool = undefined;

    try {
      await onWatch.postFeedback({
        harness: demoted.harness,
        provider: demoted.provider,
        model: demoted.model,
        pool: demoted.pool,
        outcome: "demoted",
        reason,
      });
    } catch {
      // ignore feedback failure during /bad
    }
  }

  const result = await routePiTurn({
    prompt: `Previous model failed quality check (${reason}). Select an alternative model.`,
    ctx,
    deps,
    forceReroute: true,
  });

  if (ctx.hasUI) {
    if (result.candidate) {
      ctx.ui.notify(`Demoted previous route; switched to ${result.candidate.provider}/${result.candidate.model}`, "info");
    } else {
      ctx.ui.notify(`Demoted previous route; no replacement found (${result.reason ?? "fallback"}).`, "warning");
    }
  }
  return result;
}

export async function handleRerouteCommand(options: {
  args: string;
  ctx: Pick<ExtensionCommandContext, "model" | "modelRegistry" | "thinkingLevel" | "hasUI" | "ui">;
  deps: RouterDeps;
}): Promise<RouteApplyResult> {
  const { args, ctx, deps } = options;
  const { state } = deps;

  if (args.trim() === "--reset") {
    state.excludedKeys.clear();
    state.excludedPools.clear();
  }
  state.stickyPool = undefined;

  const result = await routePiTurn({
    prompt: args.trim() && args.trim() !== "--reset"
      ? args.trim()
      : "Re-evaluate active routing pool and select best available model.",
    ctx,
    deps,
    forceReroute: true,
  });

  if (ctx.hasUI) {
    if (result.candidate) {
      ctx.ui.notify(
        `Rerouted to ${result.candidate.provider}/${result.candidate.model} (${result.decision?.tier}/${result.decision?.effort})`,
        "info",
      );
    } else {
      ctx.ui.notify(`Reroute fell back to current model (${result.reason ?? "unavailable"}).`, "warning");
    }
  }
  return result;
}
