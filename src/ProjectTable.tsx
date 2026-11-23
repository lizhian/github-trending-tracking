import React, { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { WINDOWS, type Project, type WindowHours } from "../shared/contracts.ts";
import { AITags } from "./AITags.tsx";
import { LanguageIcon } from "./LanguageIcon.tsx";
import { ProjectProblemIndicator, projectProblems } from "./ProjectProblems.tsx";
import {
  HeatValue,
  ProjectButton,
  StarValue,
  TrendValue,
} from "./ProjectValues.tsx";
import { formatNewAdmission, WINDOW_LABELS } from "./lib/format.ts";
import type { RankedProject, SortField } from "./lib/query.ts";
import { clearTextWidthCache, computeVisibleTagCount } from "./lib/tagFit.ts";

interface ProjectRowProps {
  project: Project;
  heat: number | null;
  rank: number;
  selected: boolean;
  sort: SortField;
  admissionLabel: string | null;
  cellContentWidth: number;
  fontVersion: number;
  onSelect: (project: Project) => void;
  onSearchLanguage: (language: string) => void;
  onSearchTag: (tag: string) => void;
}

const ProjectTableRow = React.memo(function ProjectTableRow({
  project,
  heat,
  rank,
  selected,
  sort,
  admissionLabel,
  cellContentWidth,
  fontVersion,
  onSelect,
  onSearchLanguage,
  onSearchTag,
}: ProjectRowProps) {
  const aiTags = project.aiTags ?? [];
  const hasProblems = projectProblems(project).length > 0;
  const visibleTagCount = useMemo(
    () =>
      computeVisibleTagCount({
        tags: aiTags,
        repoName: project.name,
        admissionLabel,
        hasProblems,
        cellContentWidth,
      }),
    [aiTags, project.name, admissionLabel, hasProblems, cellContentWidth, fontVersion],
  );

  return (
    <tr
      data-project-id={project.id}
      onClick={() => onSelect(project)}
      className={selected ? "selected" : undefined}
    >
      <td className="rank">{String(rank).padStart(2, "0")}</td>
      <td className="repo-cell">
        <div className="repo-title">
          <ProjectButton
            project={project}
            admissionLabel={admissionLabel}
            selected={selected}
          />
          <LanguageIcon language={project.language} onSearch={onSearchLanguage} />
          <AITags
            tags={aiTags}
            visibleCount={visibleTagCount}
            onTag={onSearchTag}
          />
          <ProjectProblemIndicator project={project} />
        </div>
        <p
          className="repo-description"
          title={project.aiSummary ?? project.description ?? undefined}
        >
          {project.aiSummary || project.description || "暂无项目介绍"}
        </p>
      </td>
      {WINDOWS.map((hours) => (
        <td
          className={`numeric${sort === String(hours) ? " sorted-col" : ""}`}
          key={hours}
        >
          <TrendValue trend={project.trends[hours]} />
        </td>
      ))}
      <td
        className={`numeric stars-col${sort === "stars" ? " sorted-col" : ""}`}
      >
        <StarValue stars={project.stars} />
      </td>
      <td className={`numeric heat-col${sort === "heat" ? " sorted-col" : ""}`}>
        <HeatValue project={project} heat={heat} />
      </td>
    </tr>
  );
});

export function ProjectTable({
  items,
  now,
  selectedId,
  sort,
  direction,
  loading,
  refreshing,
  listPath,
  emptyContent,
  scrollRef,
  onSort,
  onSelect,
  onSearchLanguage,
  onSearchTag,
}: {
  items: RankedProject[];
  now: number;
  selectedId: string | undefined;
  sort: SortField;
  direction: "asc" | "desc";
  loading: boolean;
  refreshing: boolean;
  listPath: string;
  emptyContent: React.ReactNode;
  scrollRef: React.RefObject<HTMLDivElement | null>;
  onSort: (field: SortField) => void;
  onSelect: (project: Project) => void;
  onSearchLanguage: (language: string) => void;
  onSearchTag: (tag: string) => void;
}) {
  const nameColRef = useRef<HTMLTableCellElement>(null);
  const [cellContentWidth, setCellContentWidth] = useState(0);
  const [fontVersion, setFontVersion] = useState(0);

  // Single ResizeObserver on the name header measures column width once for all rows.
  useLayoutEffect(() => {
    const header = nameColRef.current;
    if (!header) return;
    let raf = 0;
    const measure = () => {
      const next = Math.max(0, Math.floor(header.getBoundingClientRect().width - 16));
      setCellContentWidth((prev) => (Math.abs(prev - next) >= 2 ? next : prev));
    };
    measure();
    const observer = new ResizeObserver(() => {
      cancelAnimationFrame(raf);
      raf = requestAnimationFrame(measure);
    });
    observer.observe(header);
    return () => {
      cancelAnimationFrame(raf);
      observer.disconnect();
    };
  }, [loading]);

  useEffect(() => {
    let active = true;
    document.fonts?.ready?.then(() => {
      if (!active) return;
      clearTextWidthCache();
      setFontVersion((v) => v + 1);
    });
    return () => {
      active = false;
    };
  }, []);

  const sortHeading = (
    field: SortField,
    label: string,
    className = "numeric",
    ref?: React.Ref<HTMLTableCellElement>,
  ) => {
    const active = sort === field;
    return (
      <th
        ref={ref}
        scope="col"
        className={`${className}${active ? " sorted-col" : ""}`}
        aria-sort={
          active
            ? direction === "desc"
              ? "descending"
              : "ascending"
            : "none"
        }
      >
        <button type="button" onClick={() => onSort(field)}>
          {label}
          {active && (
            <span aria-hidden="true">
              {" "}
              {direction === "desc" ? "↓" : "↑"}
            </span>
          )}
        </button>
      </th>
    );
  };

  return (
    <div
      className="table-wrap"
      ref={scrollRef}
      aria-busy={loading || refreshing}
    >
      <table
        id="project-data"
        key={loading ? "loading" : listPath}
        className={!loading && items.length > 0 ? "list-arrival" : undefined}
      >
        <colgroup>
          <col className="rank-column" />
          <col className="name-column" />
          {WINDOWS.map((hours) => (
            <col className="number-column" key={hours} />
          ))}
          <col className="number-column stars-column" />
          <col className="number-column heat-column" />
        </colgroup>
        <caption className="visually-hidden">
          追踪项目、热度及其 Star 净增量
        </caption>
        <thead>
          <tr>
            <th scope="col" className="rank-head">
              排名
            </th>
            {sortHeading("name", "项目名称", "name-col", nameColRef)}
            {WINDOWS.map((hours: WindowHours) => (
              <React.Fragment key={hours}>
                {sortHeading(String(hours) as SortField, WINDOW_LABELS[hours])}
              </React.Fragment>
            ))}
            {sortHeading("stars", "总数", "numeric stars-col")}
            {sortHeading("heat", "热度", "numeric heat-col")}
          </tr>
        </thead>
        <tbody>
          {items.length ? (
            items.map(({ project, heat, rank }) => (
              <ProjectTableRow
                key={project.id}
                project={project}
                heat={heat}
                rank={rank}
                selected={selectedId === project.id}
                sort={sort}
                admissionLabel={formatNewAdmission(project.enteredAt, now)}
                cellContentWidth={cellContentWidth}
                fontVersion={fontVersion}
                onSelect={onSelect}
                onSearchLanguage={onSearchLanguage}
                onSearchTag={onSearchTag}
              />
            ))
          ) : (
            <tr>
              <td colSpan={8} className="empty-cell">
                {emptyContent}
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}
