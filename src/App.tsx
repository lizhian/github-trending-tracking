import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import type {
  CleanupResult,
  ListResult,
  Project,
  RunState,
} from "../shared/contracts.ts";
import { CollectionDialog } from "./CollectionDialog.tsx";
import { Icon } from "./Icons.tsx";
import { MobileProjectList } from "./MobileProjectList.tsx";
import { ProjectInspector } from "./ProjectInspector.tsx";
import { projectProblems } from "./ProjectProblems.tsx";
import { ProjectTable } from "./ProjectTable.tsx";
import { SearchSelect } from "./SearchSelect.tsx";
import { SubmissionDialog } from "./SubmissionDialog.tsx";
import { VisitStats } from "./VisitStats.tsx";
import { formatNumber, formatSigned, formatTime } from "./lib/format.ts";
import {
  ADMISSION_OPTIONS,
  admissionFilterFor,
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
  type SortField,
} from "./lib/query.ts";
import { useTheme } from "./lib/theme.ts";
import { apiBase, useApi } from "./lib/useApi.ts";

export function App() {
  const [query, setQuery] = useState<QueryState>(() =>
    parseLocation(location.search),
  );
  const [now, setNow] = useState(Date.now);
  const [draft, setDraft] = useState(query.q);
  const [refresh, setRefresh] = useState(0);
  const [submissionOpen, setSubmissionOpen] = useState(false);
  const [mobileDetailsOpen, setMobileDetailsOpen] = useState(false);
  const [isMobile, setIsMobile] = useState(
    () => matchMedia("(max-width:760px)").matches,
  );
  const { mode: themeMode, cycleTheme } = useTheme();

  const messagesDialog = useRef<HTMLDialogElement>(null);
  const mobileDetails = useRef<HTMLDialogElement>(null);
  const pushNext = useRef(false);
  const listing = useRef<HTMLDivElement>(null);
  const tableScroll = useRef<HTMLDivElement>(null);
  const inspector = useRef<HTMLElement>(null);
  const masthead = useRef<HTMLElement>(null);
  const viewTabs = useRef<HTMLDivElement>(null);
  const searchInput = useRef<HTMLInputElement>(null);
  const [indicator, setIndicator] = useState({ x: 0, width: 0 });

  useEffect(() => {
    const media = matchMedia("(max-width:760px)");
    const update = () => {
      setIsMobile(media.matches);
      if (!media.matches) setMobileDetailsOpen(false);
    };
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);

  useEffect(() => {
    const dialog = mobileDetails.current;
    if (!isMobile || !mobileDetailsOpen || !dialog) return;
    dialog.showModal();
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "clip";
    return () => {
      dialog.close();
      document.body.style.overflow = previousOverflow;
    };
  }, [isMobile, mobileDetailsOpen]);

  const commit = useCallback((patch: Partial<QueryState>, push = true) => {
    pushNext.current = push;
    setQuery((previous) => ({ ...previous, ...patch }));
  }, []);

  const listPath = `/api/projects?type=${query.tab === "linuxdo" ? "linux.do" : "github"}`;
  const previousListPath = useRef(listPath);
  const resource = useApi<ListResult>(listPath, refresh);
  const data = resource.value;

  const needsTimeTick = useMemo(() => {
    if (query.admitted !== "") return true;
    return (data?.items ?? []).some(
      (project) => now - project.enteredAt < 24 * 3_600_000,
    );
  }, [query.admitted, data?.items, now]);

  useEffect(() => {
    if (!needsTimeTick) return;
    const timer = window.setInterval(() => setNow(Date.now()), 60_000);
    return () => window.clearInterval(timer);
  }, [needsTimeTick]);

  const indexedItems = useMemo(
    () => indexProjects(data?.items ?? []),
    [data?.items],
  );

  const rankedItems = useMemo(
    () => rankProjects(indexedItems, query.sort, query.direction),
    [indexedItems, query.sort, query.direction],
  );

  const items = useMemo(
    () => filterProjects(rankedItems, query.q, query.admitted, now),
    [rankedItems, query.q, query.admitted, now],
  );

  const parsedSearch = useMemo(() => parseSearchQuery(query.q), [query.q]);

  const matchedLanguage = useMemo(() => {
    if (!parsedSearch.language) return "";
    const lower = parsedSearch.language.toLowerCase();
    return (
      data?.languages.find((lang) => lang.toLowerCase() === lower) ??
      parsedSearch.language
    );
  }, [parsedSearch.language, data?.languages]);

  // Filtering changes the list, not the open detail or its request lifecycle.
  const selectedItem = useMemo(
    () =>
      indexedItems.find((item) => item.project.id === query.project) ??
      items[0] ??
      indexedItems[0],
    [indexedItems, items, query.project],
  );
  const selected = selectedItem?.project;

  useEffect(() => {
    const next = serializeLocation(query, location.pathname, location.hash);
    if (next !== `${location.pathname}${location.search}${location.hash}`) {
      if (pushNext.current) history.pushState(null, "", next);
      else history.replaceState(null, "", next);
    }
    pushNext.current = false;
  }, [query]);

  useEffect(() => {
    const restore = () => {
      const next = parseLocation(location.search);
      pushNext.current = false;
      setDraft(next.q);
      setQuery(next);
      setMobileDetailsOpen(false);
    };
    addEventListener("popstate", restore);
    return () => removeEventListener("popstate", restore);
  }, []);

  useEffect(() => {
    if (draft === query.q) return;
    const timer = setTimeout(() => commit({ q: draft }, false), 300);
    return () => clearTimeout(timer);
  }, [draft, query.q, commit]);

  useEffect(() => {
    if (!data || resource.loading || resource.error) return;
    const project = selected?.id ?? "";
    if (query.project !== project) commit({ project }, false);
  }, [
    data,
    resource.loading,
    resource.error,
    selected?.id,
    query.project,
    query.tab,
    commit,
  ]);

  useLayoutEffect(() => {
    if (inspector.current) inspector.current.scrollTop = 0;
  }, [selected?.id]);

  useLayoutEffect(() => {
    if (previousListPath.current === listPath) return;
    previousListPath.current = listPath;
    if (tableScroll.current) tableScroll.current.scrollTop = 0;
    if (matchMedia("(max-width:760px)").matches) {
      listing.current?.scrollIntoView({ block: "start", behavior: "instant" });
    }
  }, [listPath]);

  useLayoutEffect(() => {
    const header = masthead.current;
    if (!header) return;
    const measure = () =>
      document.documentElement.style.setProperty(
        "--masthead-height",
        `${header.getBoundingClientRect().height}px`,
      );
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(header);
    return () => observer.disconnect();
  }, []);

  useLayoutEffect(() => {
    const tabs = viewTabs.current;
    if (!tabs) return;
    const measure = () => {
      const active = tabs.querySelector<HTMLElement>('[aria-pressed="true"]');
      if (active) {
        setIndicator({ x: active.offsetLeft, width: active.offsetWidth });
      }
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(tabs);
    for (const button of tabs.querySelectorAll("button")) {
      observer.observe(button);
    }
    return () => observer.disconnect();
  }, [query.tab]);

  const searchTag = useCallback(
    (tag: string) => {
      setMobileDetailsOpen(false);
      const nextQ = applyTagFilter(query.q, tag);
      setDraft(nextQ);
      commit({ q: nextQ });
    },
    [query.q, commit],
  );

  const searchLanguage = useCallback(
    (language: string) => {
      const nextQ = applyLanguageFilter(query.q, language);
      setDraft(nextQ);
      commit({ q: nextQ });
    },
    [query.q, commit],
  );

  const clear = useCallback(() => {
    setDraft("");
    commit({ q: "", admitted: "" });
  }, [commit]);

  const clearLanguage = useCallback(() => {
    const nextQ = removeLanguageFromQuery(query.q);
    setDraft(nextQ);
    commit({ q: nextQ });
  }, [query.q, commit]);

  const clearKeywords = useCallback(() => {
    const nextQ = removeKeywordsFromQuery(query.q);
    setDraft(nextQ);
    commit({ q: nextQ });
  }, [query.q, commit]);

  const changeSort = useCallback(
    (sort: SortField) => {
      commit({
        sort,
        direction:
          query.sort === sort && query.direction === "desc" ? "asc" : "desc",
      });
    },
    [query.sort, query.direction, commit],
  );

  const selectProject = useCallback(
    (project: Project) => {
      commit({ project: project.id });
      if (inspector.current) inspector.current.scrollTop = 0;
      if (isMobile) setMobileDetailsOpen(true);
    },
    [commit, isMobile],
  );

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.ctrlKey || event.metaKey || event.altKey) return;
      const target = event.target as HTMLElement | null;
      if (
        !target ||
        /input|select|textarea/i.test(target.tagName) ||
        target.isContentEditable ||
        target.closest('[role="dialog"], dialog[open], .filter-popover, .project-problem-popup')
      ) {
        return;
      }
      if (submissionOpen || messagesDialog.current?.open) return;

      if (!isMobile && event.key === "/") {
        event.preventDefault();
        searchInput.current?.focus();
        return;
      }

      if (
        !isMobile &&
        items.length > 0 &&
        (event.key === "ArrowDown" ||
          event.key === "ArrowUp" ||
          event.key === "j" ||
          event.key === "k")
      ) {
        event.preventDefault();
        const step = event.key === "ArrowDown" || event.key === "j" ? 1 : -1;
        const currentIdx = items.findIndex(
          (entry) => entry.project.id === selected?.id,
        );
        const nextIdx =
          currentIdx === -1
            ? 0
            : Math.max(0, Math.min(items.length - 1, currentIdx + step));
        const nextProject = items[nextIdx]?.project;
        if (!nextProject || nextProject.id === selected?.id) return;
        commit({ project: nextProject.id }, false);
        if (inspector.current) inspector.current.scrollTop = 0;
        const row = tableScroll.current?.querySelector<HTMLElement>(
          `tr[data-project-id="${CSS.escape(nextProject.id)}"]`,
        );
        row?.scrollIntoView({ block: "nearest" });
      }
    };
    addEventListener("keydown", onKeyDown);
    return () => removeEventListener("keydown", onKeyDown);
  }, [isMobile, submissionOpen, items, selected?.id, commit]);

  const hasFilters = Boolean(query.q.trim() || query.admitted);

  const emptyList = (
    <>
      <strong>
        {resource.loading
          ? "正在连接观测数据"
          : resource.error && !data
            ? "暂时无法读取数据"
            : data?.summary.tracked
              ? "没有匹配的项目"
              : "暂无追踪项目"}
      </strong>
      <p>
        {resource.loading
          ? "请稍候…"
          : resource.error && !data
            ? "请重试列表查询。"
            : data?.summary.tracked
              ? "试试其他关键词、语言、Topic 或 AI 标签。"
              : "后台采集后，项目会自动出现在这里。"}
      </p>
      {!resource.loading && !!data?.summary.tracked && hasFilters && (
        <button type="button" onClick={clear}>
          清除全部筛选
        </button>
      )}
    </>
  );

  const failures = [
    data?.status.scheduler,
    data?.status.collection,
  ] as (RunState | undefined)[];
  const failure = failures.find((state) => state?.failed);
  const admission = data?.status.admission as RunState | undefined;
  const discovery = data?.status.discovery as RunState | undefined;
  const cleanup = data?.status.cleanup as CleanupResult | undefined;
  const problemProjects = useMemo(
    () =>
      data?.items.filter((project) => projectProblems(project).length > 0) ?? [],
    [data?.items],
  );
  const hasCollectionNotice = Boolean(
    problemProjects.length > 0 ||
      failure ||
      discovery?.failed ||
      admission?.failed ||
      (data && data.summary.fresh < data.summary.tracked),
  );

  const themeMeta =
    themeMode === "light"
      ? { icon: "sun" as const, label: "主题：浅色，点击切换深色", title: "浅色主题" }
      : themeMode === "dark"
        ? { icon: "moon" as const, label: "主题：深色，点击切换跟随系统", title: "深色主题" }
        : { icon: "monitor" as const, label: "主题：跟随系统，点击切换浅色", title: "主题：跟随系统" };

  const inspectorContent = (
    <aside className="inspector" ref={inspector} aria-label="选中项目详情">
      {selected && data ? (
        <ProjectInspector
          key={selected.id}
          project={selected}
          heat={selectedItem?.heat ?? null}
          windowHours={query.window}
          refresh={refresh}
          asOf={data.asOf}
          onWindow={(windowHours) => commit({ window: windowHours })}
          onTopic={searchTag}
          onRefresh={() => setRefresh((value) => value + 1)}
          onBack={isMobile ? () => setMobileDetailsOpen(false) : undefined}
        />
      ) : (
        <p className="detail-empty">
          {resource.loading
            ? "正在读取追踪项目…"
            : "从榜单中选择一个项目，查看它的 Star 变化。"}
        </p>
      )}
    </aside>
  );

  return (
    <div className="app-shell">
      <a className="skip-link" href="#rankings">
        跳转到项目榜单
      </a>

      <header className="masthead" id="top" ref={masthead}>
        <div className="frame masthead-inner">
          <a className="brand" href="/" aria-label="开源观察首页">
            <Icon name="mark" size={24} />
            <strong>开源观察</strong>
          </a>

          <div className="masthead-controls">
            <div className="view-tabs" ref={viewTabs} aria-label="趋势来源">
              <span
                className="view-indicator"
                aria-hidden="true"
                style={{
                  transform: `translateX(${indicator.x}px)`,
                  width: `${indicator.width}px`,
                }}
              />
              {(
                [
                  ["github", "GitHub 趋势"],
                  ["linuxdo", "Linux.do 开源趋势"],
                ] as const
              ).map(([tab, label]) => (
                <button
                  type="button"
                  key={tab}
                  aria-pressed={query.tab === tab}
                  onClick={() => {
                    setDraft("");
                    commit({ tab, q: "", admitted: "", project: "" });
                  }}
                >
                  {label}
                </button>
              ))}
            </div>

            {!isMobile && (
              <div className="filters">
                <label className="search-field">
                  <Icon name="search" />
                  <span className="visually-hidden">
                    搜索项目、描述、语言、Topic、AI 总结或 AI 标签
                  </span>
                  <input
                    ref={searchInput}
                    type="search"
                    name="q"
                    autoComplete="off"
                    value={draft}
                    onChange={(event) =>
                      setDraft(event.target.value.slice(0, 200))
                    }
                    placeholder="搜索项目、language:Rust、AI 标签…"
                  />
                </label>

                <SearchSelect
                  label="语言"
                  all="全部语言"
                  name="language"
                  value={matchedLanguage}
                  options={data?.languages ?? []}
                  searchable
                  onChange={searchLanguage}
                />

                <SearchSelect
                  label="入库时长"
                  all="全部时长"
                  name="admitted"
                  value={query.admitted}
                  options={ADMISSION_OPTIONS}
                  searchable={false}
                  optionLabel={(value) =>
                    admissionFilterFor(value)?.label ?? value
                  }
                  onChange={(value) =>
                    commit({
                      admitted: admissionFilterFor(value)?.value ?? "",
                    })
                  }
                />

                <button
                  type="button"
                  className="icon-button reset"
                  title="清除筛选"
                  aria-label="清除全部筛选"
                  disabled={!hasFilters}
                  onClick={clear}
                >
                  <Icon name="reset" />
                </button>
              </div>
            )}
          </div>

          <div className="masthead-end">
            <button
              type="button"
              className="submission-open"
              onClick={() => setSubmissionOpen(true)}
            >
              投稿
            </button>
            <button
              type="button"
              className="icon-button"
              aria-label={themeMeta.label}
              title={themeMeta.title}
              onClick={cycleTheme}
            >
              <Icon name={themeMeta.icon} size={18} />
            </button>
            <button
              type="button"
              className={`icon-button collection-messages${hasCollectionNotice ? " has-notice" : ""}`}
              aria-label={hasCollectionNotice ? "采集消息，有异常提示" : "采集消息"}
              aria-haspopup="dialog"
              aria-controls="collection-messages"
              title="采集消息"
              onClick={() => messagesDialog.current?.showModal()}
            >
              <Icon name="bell" size={19} />
            </button>
            <a
              className="icon-button source-link"
              href="https://github.com/lizhian/github-trending-tracking"
              target="_blank"
              rel="noreferrer"
              aria-label="查看本站 GitHub 仓库"
              title="GitHub 仓库"
            >
              <Icon name="github" size={19} />
            </a>
          </div>
        </div>
      </header>

      {submissionOpen && (
        <SubmissionDialog
          apiBase={apiBase}
          onClose={() => setSubmissionOpen(false)}
          onSuccess={() => setRefresh((value) => value + 1)}
        />
      )}

      <CollectionDialog
        dialogRef={messagesDialog}
        tab={query.tab}
        data={data}
        loading={resource.loading}
        error={resource.error}
        hasNotice={hasCollectionNotice}
        failure={failure}
        discovery={discovery}
        admission={admission}
        cleanup={cleanup}
        problemProjects={problemProjects}
      />

      <main className="frame app-main">
        <h1 className="visually-hidden">开源趋势追踪</h1>
        <section
          className="workspace"
          id="rankings"
          aria-label="趋势探索工作台"
        >
          <div className="listing" ref={listing}>
            {resource.error && (
              <p className="notice error" role="alert">
                {resource.error}{" "}
                <button
                  type="button"
                  onClick={() => setRefresh((value) => value + 1)}
                >
                  重试列表查询
                </button>
              </p>
            )}

            <div className="visually-hidden" role="status">
              {resource.loading
                ? "正在读取趋势数据…"
                : data
                  ? `共 ${items.length} 个项目`
                  : ""}
            </div>

            {hasFilters && data && (
              <div className="active-filters-bar" aria-label="当前筛选条件">
                <div className="active-filter-chips">
                  {parsedSearch.language && (
                    <button
                      type="button"
                      className="filter-chip"
                      aria-label={`移除语言筛选 ${matchedLanguage}`}
                      onClick={clearLanguage}
                    >
                      <span>语言：{matchedLanguage}</span>
                      <span aria-hidden="true">×</span>
                    </button>
                  )}
                  {parsedSearch.keywords.length > 0 && (
                    <button
                      type="button"
                      className="filter-chip"
                      aria-label={`移除关键词 ${parsedSearch.keywords.join(" ")}`}
                      onClick={clearKeywords}
                    >
                      <span>关键词：{parsedSearch.keywords.join(" ")}</span>
                      <span aria-hidden="true">×</span>
                    </button>
                  )}
                  {query.admitted && (
                    <button
                      type="button"
                      className="filter-chip"
                      aria-label={`移除入库时长筛选 ${admissionFilterFor(query.admitted)?.label ?? query.admitted}`}
                      onClick={() => commit({ admitted: "" })}
                    >
                      <span>
                        入库：{admissionFilterFor(query.admitted)?.label}
                      </span>
                      <span aria-hidden="true">×</span>
                    </button>
                  )}
                </div>
                <div className="active-filter-meta">
                  <span className="filter-count">
                    显示 {formatNumber(items.length)} /{" "}
                    {formatNumber(rankedItems.length)} 项
                  </span>
                  <button
                    type="button"
                    className="filter-clear-link"
                    onClick={clear}
                  >
                    清除全部
                  </button>
                </div>
              </div>
            )}

            {isMobile ? (
              <MobileProjectList
                items={items}
                now={now}
                selectedId={selected?.id}
                sort={query.sort}
                direction={query.direction}
                draft={draft}
                language={matchedLanguage}
                languages={data?.languages ?? []}
                admitted={query.admitted}
                loading={resource.loading}
                emptyContent={emptyList}
                onDraftChange={setDraft}
                onSortChange={(sort) => commit({ sort })}
                onToggleDirection={() =>
                  commit({
                    direction: query.direction === "desc" ? "asc" : "desc",
                  })
                }
                onLanguageChange={searchLanguage}
                onAdmittedChange={(admitted) => commit({ admitted })}
                onSelect={selectProject}
                onSearchLanguage={searchLanguage}
              />
            ) : (
              <ProjectTable
                items={items}
                now={now}
                selectedId={selected?.id}
                sort={query.sort}
                direction={query.direction}
                loading={resource.loading}
                refreshing={resource.refreshing}
                listPath={listPath}
                emptyContent={emptyList}
                scrollRef={tableScroll}
                onSort={changeSort}
                onSelect={selectProject}
                onSearchLanguage={searchLanguage}
                onSearchTag={searchTag}
              />
            )}

            <div className="list-footer">
              <span>
                {data
                  ? `列表生成于 ${formatTime(data.asOf)}`
                  : "正在读取列表"}
              </span>
              <VisitStats />
              <section className="footer-stats" aria-label="当前来源观测概览">
                <div>
                  <span className="metric-label-full">正在追踪</span>
                  <span className="metric-label-short" aria-hidden="true">
                    追踪
                  </span>
                  <strong>
                    {data
                      ? formatNumber(data.summary.tracked)
                      : resource.loading
                        ? "…"
                        : "—"}
                  </strong>
                </div>
                <div title="当前来源追踪项目已知的 1 天窗口净增量合计，含部分观测与负值；各项目实际区间可能不同。">
                  <span className="metric-label-full">1 天 Star 净增量</span>
                  <span className="metric-label-short" aria-hidden="true">
                    1天 净增量
                  </span>
                  <strong>
                    {data
                      ? formatSigned(data.summary.net24 ?? null)
                      : resource.loading
                        ? "…"
                        : "—"}
                  </strong>
                </div>
                <div>
                  <span className="metric-label-full">覆盖编程语言</span>
                  <span className="metric-label-short" aria-hidden="true">
                    语言
                  </span>
                  <strong>
                    {data
                      ? formatNumber(data.languages.length)
                      : resource.loading
                        ? "…"
                        : "—"}
                  </strong>
                </div>
              </section>
            </div>
          </div>

          {isMobile ? (
            <dialog
              className="mobile-details-dialog"
              ref={mobileDetails}
              aria-label="项目详情"
              onCancel={() => setMobileDetailsOpen(false)}
              onClose={() => setMobileDetailsOpen(false)}
            >
              {mobileDetailsOpen && inspectorContent}
            </dialog>
          ) : (
            inspectorContent
          )}
        </section>
      </main>
    </div>
  );
}
