import { expect, test } from "vitest";
import { completeWindow, HOUR, isStale, latestSlot, missedSampling, nextSlot, type SamplingPolicy } from "../worker/schedule";
import { summarize, windowTrend } from "../worker/trends";
import { WINDOWS, type Point, type WindowHours } from "../worker/contracts";

const bj = (time: string) => Date.parse(`${time}+08:00`);
const policy: SamplingPolicy = { version: 2, effectiveAt: bj("2026-09-01T00:00:00") };

test("has exactly nine Beijing slots and crosses day/night boundaries without duplicates", () => {
  let slot = nextSlot(bj("2026-10-01T00:00:00"));
  const hours: number[] = [];
  while (slot < bj("2026-10-02T00:00:00")) {
    hours.push(new Date(slot + 8 * HOUR).getUTCHours());
    expect(latestSlot(slot)).toBe(slot);
    expect(latestSlot(slot - 1)).toBeLessThan(slot);
    slot = nextSlot(slot);
  }
  expect(hours).toEqual([1, 5, 9, 11, 13, 15, 17, 19, 21]);
  expect(nextSlot(bj("2026-10-01T21:00:00"))).toBe(bj("2026-10-02T01:00:00"));
  expect(nextSlot(bj("2026-10-01T09:00:00"))).toBe(bj("2026-10-01T11:00:00"));
});

test("normal two/four-hour intervals are continuous; skipped rounds are missing", () => {
  for (const [from, to] of [
    ["2026-10-01T09:04:00", "2026-10-01T11:04:00"],
    ["2026-10-01T21:04:00", "2026-10-02T01:04:00"],
    ["2026-10-02T01:04:00", "2026-10-02T05:04:00"],
  ]) expect(missedSampling(bj(from), bj(to), policy)).toBe(false);
  expect(missedSampling(bj("2026-10-01T09:04:00"), bj("2026-10-01T13:04:00"), policy)).toBe(true);
  expect(missedSampling(bj("2026-10-01T21:04:00"), bj("2026-10-02T05:04:00"), policy)).toBe(true);
});

test("night observations stay fresh through the next due slot plus ten minutes, without reviving old data", () => {
  const observed = bj("2026-10-01T01:04:00");
  expect(isStale(observed, bj("2026-10-01T04:00:00"), policy)).toBe(false);
  expect(isStale(observed, bj("2026-10-01T05:10:00"), policy)).toBe(false);
  expect(isStale(observed, bj("2026-10-01T05:10:01"), policy)).toBe(true);
  expect(isStale(observed, bj("2026-10-01T09:01:00"), policy)).toBe(true);
  const cutover: SamplingPolicy = { version: 2, effectiveAt: bj("2026-10-01T23:30:00") };
  const legacy = bj("2026-10-01T21:04:00");
  expect(isStale(legacy, bj("2026-10-01T23:29:00"), cutover)).toBe(true);
  expect(isStale(legacy, bj("2026-10-01T23:31:00"), cutover)).toBe(true);
  expect(isStale(bj("2026-10-02T01:04:00"), bj("2026-10-02T04:00:00"), cutover)).toBe(false);
});

test("window completeness follows expected times and preserves legacy gaps across cutover", () => {
  const end = bj("2026-10-02T00:04:00");
  expect(completeWindow(bj("2026-10-01T01:04:00"), end, 24, policy)).toBe(true);
  expect(completeWindow(bj("2026-10-01T05:04:00"), end, 24, policy)).toBe(false);
  const transition: SamplingPolicy = { version: 2, effectiveAt: bj("2026-10-01T12:30:00") };
  expect(missedSampling(bj("2026-10-01T08:04:00"), bj("2026-10-01T10:04:00"), transition)).toBe(true);
  expect(missedSampling(bj("2026-10-01T11:04:00"), bj("2026-10-01T13:04:00"), transition)).toBe(true);
  expect(missedSampling(bj("2026-10-01T13:04:00"), bj("2026-10-01T15:04:00"), transition)).toBe(false);
  const points: Point[] = [
    [bj("2026-10-01T08:04:00"), 100], [bj("2026-10-01T10:04:00"), 90],
    [bj("2026-10-01T13:04:00"), 110], [bj("2026-10-01T15:04:00"), 105],
  ];
  const actual = windowTrend(points, 24, points[3][0], undefined, transition);
  expect(actual.gaps).toEqual([[points[0][0], points[1][0]], [points[1][0], points[2][0]]]);
  const first = Object.fromEntries(WINDOWS.map((hours) => [hours, points[0]])) as Record<WindowHours, Point>;
  expect(summarize(first, points[3], actual.gaps, transition)[24]).toEqual({
    delta: 5, state: actual.state, start: points[0][0], end: points[3][0], gaps: actual.gaps,
  });
});
