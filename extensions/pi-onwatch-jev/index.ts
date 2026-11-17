import type { AssistantMessage } from "@earendil-works/pi-ai";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";
import { HttpJevClient, resolveOpenRouterApiKey } from "./jev-client.ts";
import { handleLinearIntake, isLinearIntakePrompt, SpawnDispatcherCli } from "./linear-intake.ts";
import { HttpOnWatchClient } from "./onwatch-client.ts";
import { registerOpenRouterPresets } from "./presets.ts";
import {
  createInitialSessionState,
  handleBadCommand,
  handleRerouteCommand,
  recordTurnEndFeedback,
  routePiTurn,
} from "./router.ts";
import {
  ALLOWED_LINEAR_HARNESSES,
  TIERS,
  type DispatcherCli,
  type JevClient,
  type OnWatchClient,
  type RouteCandidate,
  type Tier,
} from "./types.ts";

export interface ExtensionDependencies {
  onWatch?: OnWatchClient;
  jev?: JevClient;
  cli?: DispatcherCli;
}

function extractPromptFromBeforeAgentStart(event: {
  messageType: "user" | "custom";
  prompt?: string;
  message?: { content: unknown };
}): string {
  if (event.messageType === "user") {
    return event.prompt ?? "";
  }
  const content = event.message?.content;
  if (typeof content === "string") return content;
  if (Array.isArray(content)) {
    return content
      .filter((part): part is { type: string; text?: string } => Boolean(part) && typeof part === "object")
      .map((part) => (part.type === "text" && typeof part.text === "string" ? part.text : ""))
      .filter(Boolean)
      .join("\n");
  }
  return "";
}

function filterCandidatesForPick(
  tiers: Partial<Record<Tier, RouteCandidate[]>>,
  harnesses: readonly string[],
  exclude: readonly string[],
): Partial<Record<Tier, RouteCandidate[]>> {
  const allowed = new Set(harnesses.filter((h) => h !== "opencode"));
  const excluded = new Set(exclude);
  const out: Partial<Record<Tier, RouteCandidate[]>> = {};

  for (const tier of TIERS) {
    const list = (tiers[tier] ?? []).filter(
      (c) =>
        allowed.has(c.harness) &&
        c.harness !== "opencode" &&
        c.status !== "capped" &&
        c.status !== "cooling" &&
        !excluded.has(c.id) &&
        !excluded.has(`${c.provider}/${c.model}`) &&
        !excluded.has(`${c.harness}/${c.model}`),
    );
    if (list.length > 0) out[tier] = list;
  }
  return out;
}

export function createExtension(deps: ExtensionDependencies = {}) {
  return function piOnWatchJev(pi: ExtensionAPI): void {
    registerOpenRouterPresets(pi);

    const state = createInitialSessionState();
    const onWatch: OnWatchClient = deps.onWatch ?? new HttpOnWatchClient();
    const cli: DispatcherCli = deps.cli ?? new SpawnDispatcherCli();

    let activeRegistry: ExtensionContext["modelRegistry"] | undefined;

    const jev: JevClient =
      deps.jev ??
      new HttpJevClient({
        resolveApiKey: () => resolveOpenRouterApiKey({ modelRegistry: activeRegistry }),
      });

    const resetSessionState = () => {
      state.stickyPool = undefined;
      state.currentTier = undefined;
      state.currentEffort = undefined;
      state.currentCandidate = undefined;
      state.currentModelKey = undefined;
      state.excludedKeys.clear();
      state.excludedPools.clear();
      state.fallbackActive = false;
      state.fallbackReason = undefined;
      state.lastTurn = undefined;
      state.pendingIntakeSummary = undefined;
    };

    pi.on("session_start", async (event, ctx) => {
      activeRegistry = ctx.modelRegistry;
      if (event.reason === "startup" || event.reason === "new" || event.reason === "fork") {
        resetSessionState();
      }
    });

    pi.on("before_agent_start", async (event, ctx) => {
      activeRegistry = ctx.modelRegistry;
      const prompt = extractPromptFromBeforeAgentStart(event as any);

      // Branch B: Linear intake sessions matched by webhook-task.md prefix.
      // Never call pi.setModel() or pi.setThinkingLevel() on intake turns.
      if (isLinearIntakePrompt(prompt)) {
        state.pendingIntakeSummary = undefined;
        const result = await handleLinearIntake({
          prompt,
          cli,
          onWatch,
          jev,
          signal: ctx.signal,
        });

        if (result.handled && result.summary) {
          state.pendingIntakeSummary = result.summary;
          event.systemPromptOptions.selectedTools = [];
          return {
            systemPrompt:
              "You are finalizing an already-completed Linear intake dispatch. Reply with only the exact one-line summary from the user message, with no tool calls.",
          };
        }
        return;
      }

      // Branch A: Normal Pi sessions & Pi work threads.
      state.pendingIntakeSummary = undefined;
      await routePiTurn({
        prompt,
        cwd: ctx.cwd,
        ctx,
        deps: { onWatch, jev, state, pi },
        signal: ctx.signal,
      });
    });

    pi.on("context", async (event) => {
      if (!state.pendingIntakeSummary) return;
      return {
        messages: [
          {
            role: "user" as const,
            content: [
              {
                type: "text" as const,
                text: `Linear intake completed deterministically. Repeat this one-line confirmation:\n${state.pendingIntakeSummary}`,
              },
            ],
            timestamp: Date.now(),
          },
        ],
      };
    });

    pi.on("turn_start", async () => {
      if (state.pendingIntakeSummary) return;
      state.lastTurn = {
        startMs: Date.now(),
        candidate: state.currentCandidate,
      };
    });

    pi.on("message_update", async (event) => {
      if (state.lastTurn && state.lastTurn.firstTokenMs === undefined && event.message.role === "assistant") {
        state.lastTurn.firstTokenMs = Date.now();
      }
    });

    pi.on("after_provider_response", async (event) => {
      if (state.lastTurn) {
        state.lastTurn.statusCode = event.status;
      }
    });

    pi.on("agent_end", async (event) => {
      if (state.pendingIntakeSummary) {
        state.pendingIntakeSummary = undefined;
        return;
      }

      const lastAssistant = [...event.messages]
        .reverse()
        .find((m): m is AssistantMessage => m.role === "assistant");

      const errorMessage =
        lastAssistant?.stopReason === "error" ? lastAssistant.errorMessage || "assistant error" : undefined;

      await recordTurnEndFeedback({
        state,
        onWatch,
        errorMessage,
      });
    });

    pi.registerCommand("bad", {
      description: "Demote current model/pool in onWatch and reroute immediately",
      handler: async (args, ctx) => {
        activeRegistry = ctx.modelRegistry;
        await handleBadCommand({
          args,
          ctx,
          deps: { onWatch, jev, state, pi },
        });
      },
    });

    pi.registerCommand("reroute", {
      description: "Force a fresh onWatch + Jev route pick (use --reset to clear session demotions)",
      handler: async (args, ctx) => {
        activeRegistry = ctx.modelRegistry;
        await handleRerouteCommand({
          args,
          ctx,
          deps: { onWatch, jev, state, pi },
        });
      },
    });

    pi.registerTool({
      name: "jev_pick",
      label: "Jev Route Picker",
      description:
        "Query onWatch quota availability across allowed harnesses and use Jev (~typesafe/jev-latest) to select the best (harness, model, tier, effort) for a task.",
      promptSnippet: "Select the best harness, model, tier, and reasoning effort via onWatch + Jev.",
      parameters: {
        type: "object",
        properties: {
          prompt: {
            type: "string",
            description: "Task title, description, or prompt to evaluate.",
          },
          harnesses: {
            type: "array",
            items: { type: "string" },
            description: "Optional list of harnesses to consider (defaults to pi,cursor,antigravity,grok,claudeAgent,codex; opencode is always excluded).",
          },
          exclude: {
            type: "array",
            items: { type: "string" },
            description: "Optional candidate IDs or provider/model keys to skip.",
          },
        },
        required: ["prompt"],
        additionalProperties: false,
      } as any,
      async execute(_toolCallId, params: { prompt: string; harnesses?: string[]; exclude?: string[] }, signal, _onUpdate, ctx) {
        activeRegistry = ctx.modelRegistry;
        const requestedHarnesses =
          Array.isArray(params.harnesses) && params.harnesses.length > 0
            ? params.harnesses.filter((h) => h !== "opencode")
            : [...ALLOWED_LINEAR_HARNESSES];
        const exclude = Array.isArray(params.exclude) ? params.exclude : [];

        let filtered: Partial<Record<Tier, RouteCandidate[]>> = {};
        let fallbackReason: string | undefined;

        try {
          const route = await onWatch.getRoute({
            harnesses: requestedHarnesses,
            exclude,
            signal,
          });
          if (route.stale) {
            fallbackReason = route.reason || "onWatch quota data is stale";
          } else {
            filtered = filterCandidatesForPick(route.tiers, requestedHarnesses, exclude);
          }
        } catch (err) {
          fallbackReason = err instanceof Error ? err.message : "onWatch unreachable";
        }

        const hasCandidates = TIERS.some((t) => (filtered[t]?.length ?? 0) > 0);
        if (!hasCandidates) {
          const payload = {
            fallback: true,
            reason: fallbackReason || "No eligible candidates returned by onWatch",
          };
          return {
            content: [{ type: "text", text: JSON.stringify(payload, null, 2) }],
            details: payload,
          };
        }

        try {
          const decision = await jev.classify({
            mode: "linear_dispatch",
            prompt: params.prompt,
            cwd: ctx.cwd,
            candidatesByTier: filtered,
            signal,
          });
          const picked = decision.candidate;
          const payload = picked
            ? {
                fallback: false,
                harness: picked.harness,
                provider: picked.provider,
                model: picked.model,
                id: picked.id,
                tier: decision.tier,
                effort: decision.effort,
                pool: picked.pool,
                description: picked.description,
              }
            : {
                fallback: true,
                reason: "Jev returned no matching candidate",
                tier: decision.tier,
                effort: decision.effort,
              };
          return {
            content: [{ type: "text", text: JSON.stringify(payload, null, 2) }],
            details: payload,
          };
        } catch (err) {
          const fallbackTier = TIERS.find((t) => (filtered[t]?.length ?? 0) > 0) ?? "workhorse";
          const first = filtered[fallbackTier]?.[0];
          const payload = {
            fallback: true,
            reason: err instanceof Error ? err.message : "Jev failed",
            harness: first?.harness,
            provider: first?.provider,
            model: first?.model,
            id: first?.id,
            tier: fallbackTier,
            effort: "medium",
            pool: first?.pool,
          };
          return {
            content: [{ type: "text", text: JSON.stringify(payload, null, 2) }],
            details: payload,
          };
        }
      },
    });
  };
}

export default createExtension();
