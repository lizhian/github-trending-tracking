import assert from "node:assert/strict";
import test from "node:test";
import type { Project, TrendSummary } from "../../shared/contracts.ts";
import { projectHeat, projectHeatBreakdown } from "../heat.ts";
import {
  formatChartAxisDate,
  formatChartIntervalTime,
  formatHeat,
} from "./format.ts";
import { fitTagCount } from "./tagFit.ts";
import {
  applyLanguageFilter,
  applyTagFilter,
  filterProjects,
  indexProjects,
  parseLocation,
  parseSearchQuery,
  rankProjects,
  removeKeywordsFromQuery,
  removeLanguageFromQuery,
  serializeLocation,
  type QueryState,
} from "./query.ts";

const HOUR = 3_600_000;
const DAY = 24 * HOUR;
const NOW = Date.UTC(2026, 10, 23, 8, 0, 0);

function makeTrend(
  delta: number | null,
  hours: number,
  basis: TrendSummary["basis"] = "observed",
  state: TrendSummary["state"] = "complete",
): TrendSummary {
  return {
    delta,
    asOf: NOW,
    basis,
    state,
    start: delta === null ? null : NOW - hours * HOUR,
    end: delta === null ? null : NOW,
    gaps: [],
  };
}

function makeProject(overrides: Partial<Project> = {}): Project {
  return {
    id: "github:1",
    type: "github",
    repoId: "1",
    name: "acme/alpha",
    description: "Fast agent toolkit",
    aiSummary: "一个高性能开源工具",
    aiTags: ["Agent", "CLI"],
    homepage: null,
    language: "Rust",
    topics: ["cli", "mcp"],
    stars: 1200,
    enteredAt: NOW - 10 * DAY,
    observedAt: NOW,
    unavailableAt: 0,
    attemptedAt: NOW,
    error: null,
    stale: false,
    trends: {
      24: makeTrend(100, 24),
      72: makeTrend(240, 72),
      168: makeTrend(350, 168),
      720: makeTrend(900, 720),
    },
    ...overrides,
  };
}

test("parseSearchQuery composes language tokens and keywords", () => {
  assert.deepEqual(parseSearchQuery("language:Rust agent mcp"), {
    language: "Rust",
    keywords: ["agent", "mcp"],
  });
  assert.deepEqual(
    parseSearchQuery('llm language:"Jupyter Notebook" evaluation'),
    {
      language: "Jupyter Notebook",
      keywords: ["llm", "evaluation"],
    },
  );
  assert.deepEqual(parseSearchQuery("  "), {
    language: null,
    keywords: [],
  });
});

test("language and tag query helpers preserve sibling filters", () => {
  assert.equal(applyLanguageFilter("agent", "Rust"), "language:Rust agent");
  assert.equal(
    applyLanguageFilter("language:Go agent", "Jupyter Notebook"),
    'language:"Jupyter Notebook" agent',
  );
  assert.equal(applyLanguageFilter("language:Go agent", ""), "agent");
  assert.equal(removeLanguageFromQuery("language:Rust agent mcp"), "agent mcp");
  assert.equal(removeKeywordsFromQuery("language:Rust agent mcp"), "language:Rust");
  assert.equal(applyTagFilter("language:Rust old", "Agent"), "language:Rust Agent");
  assert.equal(applyTagFilter("old", "Agent"), "Agent");
});

test("parseLocation and serializeLocation handle defaults and legacy params", () => {
  const migrated = parseLocation("?language=TypeScript&topic=mcp&tab=linuxdo&window=168");
  assert.deepEqual(migrated, {
    q: "language:TypeScript mcp",
    admitted: "",
    tab: "linuxdo",
    sort: "24",
    direction: "desc",
    project: "",
    window: 168,
  });

  const defaults: QueryState = {
    q: "",
    admitted: "",
    tab: "github",
    sort: "24",
    direction: "desc",
    project: "",
    window: 720,
  };
  assert.equal(serializeLocation(defaults, "/", ""), "/");

  const custom: QueryState = {
    q: 'language:"Jupyter Notebook" llm',
    admitted: "24",
    tab: "linuxdo",
    sort: "heat",
    direction: "asc",
    project: "linux.do:42",
    window: 24,
  };
  const url = serializeLocation(custom, "/", "#top");
  assert.deepEqual(parseLocation(new URL(`https://x.test${url}`).search), custom);
});

test("rankProjects preserves global rank after composable filtering and puts nulls last", () => {
  const p1 = makeProject({
    id: "github:1",
    name: "acme/rust-agent",
    language: "Rust",
    enteredAt: NOW - 12 * HOUR,
    trends: {
      24: makeTrend(300, 24),
      72: makeTrend(300, 72),
      168: makeTrend(300, 168),
      720: makeTrend(300, 720),
    },
  });
  const p2 = makeProject({
    id: "github:2",
    name: "acme/python-agent",
    language: "Python",
    enteredAt: NOW - 3 * DAY,
    trends: {
      24: makeTrend(200, 24),
      72: makeTrend(200, 72),
      168: makeTrend(200, 168),
      720: makeTrend(200, 720),
    },
  });
  const p3 = makeProject({
    id: "github:3",
    name: "acme/rust-mcp",
    language: "Rust",
    enteredAt: NOW - 5 * DAY,
    trends: {
      24: makeTrend(null, 24, "observed", "accumulating"),
      72: makeTrend(null, 72, "observed", "accumulating"),
      168: makeTrend(null, 168, "observed", "accumulating"),
      720: makeTrend(null, 720, "observed", "accumulating"),
    },
  });

  const indexed = indexProjects([p3, p2, p1]);
  const ranked = rankProjects(indexed, "24", "desc");
  assert.deepEqual(
    ranked.map((r) => [r.project.id, r.rank]),
    [
      ["github:1", 1],
      ["github:2", 2],
      ["github:3", 3],
    ],
  );

  // Language + keyword composition preserves original global rank (#1 and #3).
  assert.deepEqual(
    filterProjects(ranked, "language:Rust mcp", "", NOW).map((r) => [
      r.project.id,
      r.rank,
    ]),
    [
      ["github:1", 1],
      ["github:3", 3],
    ],
  );

  // Admission filter <1天 only matches p1.
  assert.deepEqual(
    filterProjects(ranked, "", "24", NOW).map((r) => r.project.id),
    ["github:1"],
  );
  // Admission filter >1天 excludes p1.
  assert.deepEqual(
    filterProjects(ranked, "", "gte24", NOW).map((r) => r.project.id),
    ["github:2", "github:3"],
  );
});

test("projectHeat and projectHeatBreakdown cover weights, short windows, missing/stale windows, and zero distinction", () => {
  const proj = makeProject();
  // 100/1*0.6 + 240/3*0.25 + 350/7*0.1 + 900/30*0.05 = 60 + 20 + 5 + 1.5 = 86.5
  const full = projectHeatBreakdown(proj);
  assert.equal(projectHeat(proj), 86.5);
  assert.equal(full.total, 86.5);
  assert.deepEqual(
    full.windows.map((w) => [w.hours, w.status, w.contribution]),
    [
      [24, "scored", 60],
      [72, "scored", 20],
      [168, "scored", 5],
      [720, "scored", 1.5],
    ],
  );

  // Negative and zero deltas are real scored zero contributions, not missing.
  const negProj = makeProject({
    trends: {
      24: makeTrend(-50, 24),
      72: makeTrend(0, 72),
      168: makeTrend(350, 168),
      720: makeTrend(900, 720),
    },
  });
  const negBreakdown = projectHeatBreakdown(negProj);
  assert.equal(projectHeat(negProj), 6.5);
  assert.equal(negBreakdown.total, 6.5);
  assert.equal(negBreakdown.windows[0].status, "scored");
  assert.equal(negBreakdown.windows[0].delta, -50);
  assert.equal(negBreakdown.windows[0].contribution, 0);
  assert.equal(negBreakdown.windows[1].status, "scored");
  assert.equal(negBreakdown.windows[1].delta, 0);
  assert.equal(negBreakdown.windows[1].contribution, 0);

  // Short windows (<24h) clamp denominator to 1 day.
  const youngProj = makeProject({
    trends: {
      24: makeTrend(10, 12, "observed", "partial"),
      72: makeTrend(10, 12, "observed", "partial"),
      168: makeTrend(10, 12, "observed", "partial"),
      720: makeTrend(10, 12, "observed", "partial"),
    },
  });
  const youngBreakdown = projectHeatBreakdown(youngProj);
  assert.equal(projectHeat(youngProj), 10);
  assert.equal(youngBreakdown.total, 10);
  assert.equal(youngBreakdown.windows[0].actualDays, 0.5);
  assert.equal(youngBreakdown.windows[0].effectiveDays, 1);
  assert.equal(youngBreakdown.windows[0].contribution, 6);

  // Missing or unknown single window contributes 0 while remaining windows still score.
  const partialMissingProj = makeProject({
    trends: {
      24: makeTrend(null, 24, "unknown"),
      72: makeTrend(null, 72, "observed", "accumulating"),
      168: makeTrend(350, 168),
      720: makeTrend(900, 720),
    },
  });
  const partialMissing = projectHeatBreakdown(partialMissingProj);
  assert.equal(projectHeat(partialMissingProj), 6.5);
  assert.deepEqual(
    partialMissing.windows.map((w) => [w.status, w.contribution]),
    [
      ["unknown", 0],
      ["missing", 0],
      ["scored", 5],
      ["scored", 1.5],
    ],
  );

  // All 4 windows missing or stale yield 0 (formatted as 0.0) with distinct statuses.
  const allMissingProj = makeProject({
    trends: {
      24: makeTrend(null, 24, "observed", "accumulating"),
      72: makeTrend(null, 72, "observed", "accumulating"),
      168: makeTrend(null, 168, "observed", "accumulating"),
      720: makeTrend(null, 720, "observed", "accumulating"),
    },
  });
  assert.equal(projectHeat(allMissingProj), 0);
  assert.equal(formatHeat(projectHeat(allMissingProj)), "0.0");
  assert.ok(
    projectHeatBreakdown(allMissingProj).windows.every((w) => w.status === "missing"),
  );

  const allStaleProj = makeProject({
    stale: true,
    trends: {
      24: makeTrend(null, 24, "unknown"),
      72: makeTrend(null, 72, "unknown"),
      168: makeTrend(null, 168, "unknown"),
      720: makeTrend(null, 720, "unknown"),
    },
  });
  assert.equal(projectHeat(allStaleProj), 0);
  assert.ok(
    projectHeatBreakdown(allStaleProj).windows.every((w) => w.status === "stale"),
  );
});

test("rankProjects sorts all projects numerically by heat including short-window and all-zero projects", () => {
  const full = makeProject({ id: "github:1", name: "acme/full" }); // 86.5
  const young = makeProject({
    id: "github:2",
    name: "acme/young",
    trends: {
      24: makeTrend(10, 12, "observed", "partial"),
      72: makeTrend(10, 12, "observed", "partial"),
      168: makeTrend(10, 12, "observed", "partial"),
      720: makeTrend(10, 12, "observed", "partial"),
    },
  }); // 10
  const zeroB = makeProject({
    id: "github:3",
    name: "acme/zero-b",
    trends: {
      24: makeTrend(null, 24, "observed", "accumulating"),
      72: makeTrend(null, 72, "observed", "accumulating"),
      168: makeTrend(null, 168, "observed", "accumulating"),
      720: makeTrend(null, 720, "observed", "accumulating"),
    },
  }); // 0
  const zeroA = makeProject({
    id: "github:4",
    name: "acme/zero-a",
    stale: true,
    trends: {
      24: makeTrend(null, 24, "unknown"),
      72: makeTrend(null, 72, "unknown"),
      168: makeTrend(null, 168, "unknown"),
      720: makeTrend(null, 720, "unknown"),
    },
  }); // 0

  const indexed = indexProjects([zeroB, young, zeroA, full]);
  assert.deepEqual(
    rankProjects(indexed, "heat", "desc").map((r) => [r.project.id, r.heat, r.rank]),
    [
      ["github:1", 86.5, 1],
      ["github:2", 10, 2],
      ["github:4", 0, 3],
      ["github:3", 0, 4],
    ],
  );
  assert.deepEqual(
    rankProjects(indexed, "heat", "asc").map((r) => [r.project.id, r.heat, r.rank]),
    [
      ["github:4", 0, 1],
      ["github:3", 0, 2],
      ["github:2", 10, 3],
      ["github:1", 86.5, 4],
    ],
  );
});

test("chart date formatters use Asia/Shanghai without stray suffix", () => {
  // 2026-11-23 14:00 Asia/Shanghai == 2026-11-23 06:00 UTC
  const from = Date.UTC(2026, 10, 23, 5, 0, 0);
  const to = Date.UTC(2026, 10, 23, 6, 0, 0);
  assert.deepEqual(formatChartIntervalTime(from, to), {
    date: "2026-11-23 14时",
    duration: "1h",
  });

  // 2026-11-24 00:00 Asia/Shanghai == 2026-11-23 16:00 UTC
  const dayFrom = Date.UTC(2026, 10, 22, 16, 0, 0);
  const dayTo = Date.UTC(2026, 10, 23, 16, 0, 0);
  assert.deepEqual(formatChartIntervalTime(dayFrom, dayTo), {
    date: "2026-11-24",
    duration: "1d",
  });
  assert.equal(formatChartAxisDate(dayTo), "11/24");
});

test("fitTagCount shows all tags when fitting and collapses cleanly to +N when tight", () => {
  const widths = [50, 50, 50, 50]; // total with gaps (4px): 200 + 12 = 212
  const overflow = (hidden: number) => 24 + String(hidden).length * 6; // +1=30, +2=30, +4=30

  assert.equal(fitTagCount([], overflow, 200, 4), 0);
  assert.equal(fitTagCount(widths, overflow, 212, 4), 4);
  assert.equal(fitTagCount(widths, overflow, 211, 4), 3); // 3*50 + 3*4 + 30 = 192 <= 211
  assert.equal(fitTagCount(widths, overflow, 150, 4), 2); // 2*50 + 2*4 + 30 = 138 <= 150
  assert.equal(fitTagCount(widths, overflow, 85, 4), 1);  // 1*50 + 1*4 + 30 = 84 <= 85
  assert.equal(fitTagCount(widths, overflow, 60, 4), 0);  // cannot fit 1 tag + +3
});
