import { HOUR } from "./trends";

export interface HostState {
  startedAt: number;
  blockedUntil: number;
}
export type Hosts = Record<string, HostState>;
export interface Clock {
  now(): number;
  sleep(ms: number): Promise<void>;
}
export const upstreamClock: Clock = {
  now: () => Date.now(),
  sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
};

export class DeferredRequest extends Error {
  constructor(public readonly retryAt: number | null) {
    super(retryAt === null ? "Discovery budget exhausted" : "Upstream cooling down");
  }
}

export function safeError(error: unknown): string {
  // Persist only fixed diagnostics, never upstream bodies, headers or exception text.
  if (
    error instanceof Error &&
    /^(GitHub HTTP \d{3}|RSS HTTP \d{3}|HelloGithub HTTP \d{3}|OpenGithubs HTTP \d{3}|GitHub GraphQL unavailable|RSS invalid XML|RSS missing channel|HelloGithub invalid project|Upstream response too large|Upstream empty response|Upstream cooling down)$/.test(error.message)
  ) return error.message;
  return "Upstream timeout or unavailable";
}

async function boundedResponse(response: Response): Promise<string> {
  if (!response.body) throw new Error("Upstream empty response");
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let bytes = 0;
  let text = "";
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      bytes += value.length;
      if (bytes > 2_000_000) throw new Error("Upstream response too large");
      text += decoder.decode(value, { stream: true });
    }
    return text + decoder.decode();
  } finally {
    await reader.cancel();
  }
}

export class Upstream {
  constructor(
    readonly hosts: Hosts,
    readonly clock: Clock = upstreamClock,
  ) {}

  blockedUntil(url: string): number {
    return this.hosts[new URL(url).hostname]?.blockedUntil ?? 0;
  }

  cooldown(url: string, headers?: Headers, duration = HOUR): void {
    const host = new URL(url).hostname;
    const now = this.clock.now();
    const state = this.hosts[host] ??= { startedAt: 0, blockedUntil: 0 };
    const retry = headers?.get("Retry-After");
    const retryAt = retry && /^\d+$/.test(retry.trim())
      ? now + Number(retry) * 1000
      : retry ? Date.parse(retry) : 0;
    const resetAt = headers?.get("X-RateLimit-Remaining") === "0"
      ? Number(headers.get("X-RateLimit-Reset")) * 1000 : 0;
    state.blockedUntil = Math.max(
      state.blockedUntil, now + duration,
      Number.isFinite(retryAt) ? retryAt : 0,
      Number.isFinite(resetAt) ? resetAt : 0,
    );
  }

  async text(
    url: string,
    label: "RSS" | "HelloGithub" | "OpenGithubs" | "GitHub",
    init: RequestInit = {},
    deadline = Infinity,
  ): Promise<string> {
    const host = new URL(url).hostname;
    const state = this.hosts[host] ??= { startedAt: 0, blockedUntil: 0 };
    let now = this.clock.now();
    if (state.blockedUntil > now) throw new DeferredRequest(state.blockedUntil);
    const wait = Math.max(0, state.startedAt + 2000 - now);
    const timeout = label === "GitHub" ? 20_000 : 15_000;
    if (now + wait + timeout > deadline) throw new DeferredRequest(null);
    if (wait) await this.clock.sleep(wait);
    now = this.clock.now();
    if (now + timeout > deadline) throw new DeferredRequest(null);
    state.startedAt = now;
    // The timeout covers both headers and the bounded response body.
    const response = await fetch(url, {
      ...init,
      signal: AbortSignal.timeout(timeout),
    });
    // Successful GraphQL responses can also exhaust the quota or carry Retry-After.
    const limited = response.status === 429 ||
      response.headers.has("Retry-After") ||
      response.headers.get("X-RateLimit-Remaining") === "0";
    if (limited) this.cooldown(url, response.headers);
    if (!response.ok) {
      // GitHub can report secondary limits without rate-limit headers.
      if (label === "GitHub" && response.status === 403 && !limited) {
        const body = await boundedResponse(response);
        if (/secondary rate limit|API rate limit exceeded/i.test(body))
          this.cooldown(url, response.headers);
      } else await response.body?.cancel();
      throw new Error(`${label} HTTP ${response.status}`);
    }
    return boundedResponse(response);
  }
}
