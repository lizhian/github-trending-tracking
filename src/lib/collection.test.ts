import assert from "node:assert/strict";
import test from "node:test";
import type {
  ListResult,
  Project,
  SourceStatus,
  TrendSummary,
} from "../../shared/contracts.ts";
import {
  classifySource,
  deriveCollectionOverview,
} from "./collection.ts";

const NOW = Date.UTC(2026, 10, 23, 8, 0, 0);
const HOUR = 3_600_000;

function makeTrend(): TrendSummary {
  return {
    delta: 10,
    asOf: NOW,
    basis: "observed",
    state: "complete",
    start: NOW - 24 * HOUR,
    end: NOW,
    gaps: [],
  };
}

function makeProject(overrides: Partial<Project> = {}): Project {
  return {
    id: "github:1",
    type: "github",
    repoId: "1",
    name: "acme/alpha",
    description: "desc",
    homepage: null,
    language: "TypeScript",
    topics: [],
    stars: 100,
    enteredAt: NOW - 48 * HOUR,
    observedAt: NOW - HOUR,
    unavailableAt: 0,
    attemptedAt: NOW - 10 * 60_000,
    error: null,
    stale: false,
    trends: {
      24: makeTrend(),
      72: makeTrend(),
      168: makeTrend(),
      720: makeTrend(),
    },
    ...overrides,
  };
}

function makeSource(overrides: Partial<SourceStatus> = {}): SourceStatus {
  return {
    id: "github-daily",
    name: "GitHub 综合日榜",
    attemptedAt: NOW - HOUR,
    succeededAt: NOW - HOUR,
    succeeded: 25,
    failed: 0,
    error: null,
    admitted: 3,
    nextAt: NOW + HOUR,
    ...overrides,
  };
}

function makeListResult(overrides: Partial<ListResult> = {}): ListResult {
  return {
    sources: [makeSource()],
    items: [makeProject()],
    total: 1,
    languages: ["TypeScript"],
    topics: [],
    summary: {
      tracked: 1,
      fresh: 1,
      accumulating: 0,
      net24: 10,
      comparable24: 1,
    },
    status: {
      collection: {
        attemptedAt: NOW - 10 * 60_000,
        succeededAt: NOW - 10 * 60_000,
        succeeded: 1,
        failed: 0,
        error: null,
      },
      admission: {
        attemptedAt: NOW - 10 * 60_000,
        succeededAt: NOW - 10 * 60_000,
        succeeded: 2,
        failed: 0,
        error: null,
      },
      cleanup: {
        at: NOW - 4 * HOUR,
        removed: 0,
        remaining: 1,
        eligible: 1,
      },
    },
    asOf: NOW,
    ...overrides,
  };
}

test("classifySource distinguishes unrun, partial, failed, and ok states including language trending", () => {
  // Unrun language-trending source has languagesCompleted=0 and languagesTotal=24,
  // but must be classified as unrun rather than partial.
  const unrunLang = classifySource(
    makeSource({
      id: "github-language-daily",
      name: "GitHub 分语言日榜",
      attemptedAt: 0,
      succeededAt: null,
      succeeded: 0,
      failed: 0,
      admitted: undefined,
      nextAt: 0,
      languagesCompleted: 0,
      languagesTotal: 24,
    }),
    NOW,
  );
  assert.equal(unrunLang.health, "unrun");
  assert.equal(unrunLang.statusLabel, "尚未执行");
  assert.equal(unrunLang.parsedCountText, "—");
  assert.equal(unrunLang.admittedCountText, "—");
  assert.equal(unrunLang.attemptedAtText, "尚未执行");
  assert.equal(unrunLang.succeededAtText, "尚无成功记录");
  assert.equal(unrunLang.nextAtLabel, "下次执行");
  assert.equal(unrunLang.nextAtText, "等待调度");
  assert.deepEqual(unrunLang.languageProgress, {
    completed: 0,
    total: 24,
    percent: 0,
  });

  const deferred = classifySource(
    makeSource({
      languagesCompleted: 0,
      languagesTotal: 24,
      succeeded: 0,
      retryAfter: NOW + 5 * 60_000,
    }),
    NOW,
  );
  assert.equal(deferred.statusLabel, "等待重试");

  // Partial language-trending source with 23/24 done and 1 language error.
  const partialWithError = classifySource(
    makeSource({
      id: "github-language-daily",
      name: "GitHub 分语言日榜",
      attemptedAt: NOW - 5 * 60_000,
      succeededAt: null,
      succeeded: 101,
      failed: 1,
      error: "C#：GitHub Trending HTTP 503",
      admitted: 0,
      languagesCompleted: 23,
      languagesTotal: 24,
      retryAfter: NOW + 5 * 60_000,
      nextAt: NOW + 5 * 60_000,
    }),
    NOW,
  );
  assert.equal(partialWithError.health, "partial");
  assert.equal(partialWithError.statusLabel, "部分完成，等待重试");
  assert.equal(partialWithError.parsedCountText, "101");
  assert.equal(partialWithError.admittedCountText, "0");
  assert.equal(partialWithError.nextAtLabel, "下次重试");
  assert.equal(partialWithError.errorMessage, "C#：GitHub Trending HTTP 503");
  assert.deepEqual(partialWithError.languageProgress, {
    completed: 23,
    total: 24,
    percent: 96,
  });

  // All languages failed -> failed, not partial.
  const allLangFailed = classifySource(
    makeSource({
      id: "github-language-daily",
      name: "GitHub 分语言日榜",
      attemptedAt: NOW - 5 * 60_000,
      succeededAt: null,
      succeeded: 0,
      failed: 24,
      error: "C++：HTTP 503",
      admitted: 0,
      languagesCompleted: 0,
      languagesTotal: 24,
      retryAfter: NOW + 5 * 60_000,
      nextAt: NOW - 1000,
    }),
    NOW,
  );
  assert.equal(allLangFailed.health, "failed");
  assert.equal(allLangFailed.statusLabel, "异常，等待重试");
  assert.equal(allLangFailed.parsedCountText, "0");
  assert.equal(allLangFailed.nextAtText, "已到期，等待调度");

  // GitHub Search incomplete results gets friendly Chinese message.
  const incompleteSearch = classifySource(
    makeSource({
      id: "github-search",
      name: "GitHub Search 总榜",
      failed: 1,
      succeeded: 60,
      error: "GitHub Search incomplete results",
    }),
    NOW,
  );
  assert.equal(incompleteSearch.health, "failed");
  assert.equal(
    incompleteSearch.errorMessage,
    "搜索结果不完整，已保留返回的候选，将重试。",
  );
});

test("deriveCollectionOverview preserves missing vs 0 semantics and separates admission completion from source admitted", () => {
  // Missing status objects display em-dash, not 0.
  const missing = deriveCollectionOverview(
    makeListResult({
      status: {},
      sources: [
        makeSource({
          attemptedAt: 0,
          succeededAt: null,
          succeeded: 0,
          admitted: undefined,
        }),
      ],
    }),
  );
  assert.equal(missing.sampling.hasRun, false);
  assert.equal(missing.sampling.succeededText, "—");
  assert.equal(missing.sampling.failedText, "—");
  assert.equal(missing.admission.hasRun, false);
  assert.equal(missing.admission.succeededText, "—");
  assert.equal(missing.admission.failedText, "—");
  assert.equal(missing.cleanup.hasRun, false);
  assert.equal(missing.cleanup.removedText, "—");
  assert.equal(missing.cleanup.atText, "尚未执行");
  assert.equal(missing.hasNotice, false);

  // Explicit 0 values on executed runs display "0", not em-dash.
  const zeros = deriveCollectionOverview(
    makeListResult({
      status: {
        collection: {
          attemptedAt: NOW,
          succeededAt: NOW,
          succeeded: 0,
          failed: 0,
          error: null,
        },
        admission: {
          attemptedAt: NOW,
          succeededAt: NOW,
          succeeded: 0,
          failed: 0,
          error: null,
        },
        cleanup: {
          at: NOW,
          removed: 0,
          remaining: 1,
          eligible: 0,
        },
      },
    }),
  );
  assert.equal(zeros.sampling.hasRun, true);
  assert.equal(zeros.sampling.succeededText, "0");
  assert.equal(zeros.sampling.failedText, "0");
  assert.equal(zeros.admission.hasRun, true);
  assert.equal(zeros.admission.succeededText, "0");
  assert.equal(zeros.admission.failedText, "0");
  assert.equal(zeros.cleanup.hasRun, true);
  assert.equal(zeros.cleanup.removedText, "0");
  assert.equal(zeros.hasNotice, false);
});

test("deriveCollectionOverview flags partial sources and builds expandable problem project details with attempt and observation times", () => {
  const broken = makeProject({
    id: "github:2",
    name: "acme/broken",
    unavailableAt: NOW - 2 * HOUR,
    stale: true,
    error: "GitHub HTTP 503",
    attemptedAt: 0,
    observedAt: 0,
  });
  const overview = deriveCollectionOverview(
    makeListResult({
      items: [makeProject(), broken],
      summary: {
        tracked: 2,
        fresh: 1,
        accumulating: 0,
        net24: 10,
        comparable24: 2,
      },
      sources: [
        makeSource({
          id: "github-language-daily",
          name: "GitHub 分语言日榜",
          attemptedAt: NOW - 60_000,
          succeededAt: null,
          succeeded: 50,
          failed: 0,
          languagesCompleted: 10,
          languagesTotal: 24,
          retryAfter: NOW + 5 * 60_000,
        }),
      ],
    }),
  );

  assert.equal(overview.hasNotice, true);
  assert.equal(overview.staleCount, 1);
  assert.equal(overview.problemProjects.length, 1);
  assert.deepEqual(
    overview.problemProjects[0].tags.map((tag) => tag.kind),
    ["unavailable", "stale", "error"],
  );
  assert.equal(overview.problemProjects[0].reasons.length, 3);
  assert.equal(overview.problemProjects[0].attemptedAtText, "尚无尝试记录");
  assert.equal(overview.problemProjects[0].observedAtText, "尚无成功记录");
  assert.ok(overview.problemProjects[0].unavailableAtText);
});
