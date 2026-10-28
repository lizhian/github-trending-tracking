export const HOUR = 3_600_000;
export const DAY = 24 * HOUR;
export const SAMPLING_HOURS = [1, 5, 9, 11, 13, 15, 17, 19, 21] as const;
export const SAMPLING_GRACE = 10 * 60_000;
export interface SamplingPolicy {
  version: 2;
  effectiveAt: number;
}

// All arithmetic is UTC with a fixed Beijing offset, independent of host TZ.
export function latestSlot(at: number): number {
  const day = Math.floor((at + 8 * HOUR) / DAY) * DAY - 8 * HOUR;
  for (let i = SAMPLING_HOURS.length - 1; i >= 0; i--) {
    const slot = day + SAMPLING_HOURS[i] * HOUR;
    if (slot <= at) return slot;
  }
  return day - DAY + 21 * HOUR;
}

export function nextSlot(at: number): number {
  const day = Math.floor((at + 8 * HOUR) / DAY) * DAY - 8 * HOUR;
  for (const hour of SAMPLING_HOURS) {
    const slot = day + hour * HOUR;
    if (slot > at) return slot;
  }
  return day + DAY + HOUR;
}

export function samplingSlot(at: number, policy?: SamplingPolicy): number {
  return !policy || at < policy.effectiveAt ? Math.floor(at / HOUR) * HOUR : latestSlot(at);
}

// Historical expected times are hourly before cutover, scheduled afterwards.
// Carry the final legacy slot until the first scheduled slot after cutover.
function observationSlot(at: number, policy?: SamplingPolicy): number {
  if (!policy || at < policy.effectiveAt) return Math.floor(at / HOUR) * HOUR;
  const slot = latestSlot(at);
  return slot >= policy.effectiveAt ? slot : Math.floor((policy.effectiveAt - 1) / HOUR) * HOUR;
}

function nextExpected(at: number, policy?: SamplingPolicy): number {
  const hourly = (Math.floor(at / HOUR) + 1) * HOUR;
  if (!policy || hourly < policy.effectiveAt) return hourly;
  return nextSlot(Math.max(at, policy.effectiveAt - 1));
}

export function missedSampling(from: number, to: number, policy?: SamplingPolicy): boolean {
  return nextExpected(observationSlot(from, policy), policy) < observationSlot(to, policy);
}

export function isStale(observedAt: number, now: number, policy?: SamplingPolicy): boolean {
  if (!policy || now < policy.effectiveAt) return now - observedAt > 2 * HOUR;
  // A longer new interval must not revive already-stale legacy observations.
  if (observedAt + 2 * HOUR < policy.effectiveAt) return true;
  // Anchor to the observation, not the current round: old data stays stale.
  return now > nextSlot(observedAt) + SAMPLING_GRACE;
}

export function completeWindow(start: number, end: number, hours: number, policy?: SamplingPolicy): boolean {
  const boundary = end - hours * HOUR;
  const latestStart = !policy || boundary < policy.effectiveAt
    ? boundary + HOUR : nextSlot(boundary - 1) + SAMPLING_GRACE;
  return start <= latestStart;
}
