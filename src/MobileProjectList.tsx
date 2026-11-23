import React from "react";
import type { Project, WindowHours } from "../shared/contracts.ts";
import { Icon } from "./Icons.tsx";
import { LanguageIcon } from "./LanguageIcon.tsx";
import { ProjectProblemIndicator } from "./ProjectProblems.tsx";
import {
  HeatValue,
  ProjectButton,
  StarValue,
  TrendValue,
} from "./ProjectValues.tsx";
import { formatNewAdmission, WINDOW_LABELS } from "./lib/format.ts";
import {
  ADMISSION_FILTERS,
  SORT_FIELDS,
  SORT_LABELS,
  type AdmissionFilterValue,
  type RankedProject,
  type SortField,
} from "./lib/query.ts";

interface MobileCardProps {
  project: Project;
  heat: number | null;
  rank: number;
  selected: boolean;
  sort: SortField;
  admissionLabel: string | null;
  onSelect: (project: Project) => void;
  onSearchLanguage: (language: string) => void;
}

const MobileProjectCard = React.memo(function MobileProjectCard({
  project,
  heat,
  rank,
  selected,
  sort,
  admissionLabel,
  onSelect,
  onSearchLanguage,
}: MobileCardProps) {
  return (
    <li>
      <div className="mobile-project-title">
        <span className="rank">{String(rank).padStart(2, "0")}</span>
        <ProjectButton
          project={project}
          admissionLabel={admissionLabel}
          selected={selected}
          onClick={() => onSelect(project)}
        />
        <LanguageIcon language={project.language} onSearch={onSearchLanguage} />
        <ProjectProblemIndicator project={project} />
      </div>
      {sort !== "name" && (
        <div className="mobile-project-metric">
          <span className="visually-hidden">
            {sort === "stars"
              ? "Star 总数"
              : sort === "heat"
                ? "热度"
                : `${WINDOW_LABELS[Number(sort) as WindowHours]} Star 净增量`}
          </span>
          {sort === "stars" ? (
            <StarValue stars={project.stars} />
          ) : sort === "heat" ? (
            <HeatValue project={project} heat={heat} />
          ) : (
            <TrendValue
              trend={project.trends[Number(sort) as WindowHours]}
              showState
            />
          )}
        </div>
      )}
      <button
        type="button"
        className="mobile-project-summary"
        aria-haspopup="dialog"
        onClick={() => onSelect(project)}
      >
        {project.aiSummary || project.description || "暂无项目介绍"}
      </button>
    </li>
  );
});

export function MobileProjectList({
  items,
  now,
  selectedId,
  sort,
  direction,
  draft,
  language,
  languages,
  admitted,
  loading,
  emptyContent,
  onDraftChange,
  onSortChange,
  onToggleDirection,
  onLanguageChange,
  onAdmittedChange,
  onSelect,
  onSearchLanguage,
}: {
  items: RankedProject[];
  now: number;
  selectedId: string | undefined;
  sort: SortField;
  direction: "asc" | "desc";
  draft: string;
  language: string;
  languages: string[];
  admitted: "" | AdmissionFilterValue;
  loading: boolean;
  emptyContent: React.ReactNode;
  onDraftChange: (value: string) => void;
  onSortChange: (sort: SortField) => void;
  onToggleDirection: () => void;
  onLanguageChange: (language: string) => void;
  onAdmittedChange: (admitted: "" | AdmissionFilterValue) => void;
  onSelect: (project: Project) => void;
  onSearchLanguage: (language: string) => void;
}) {
  return (
    <>
      <div className="mobile-controls" aria-label="移动端筛选与排序">
        <label className="search-field mobile-search">
          <Icon name="search" />
          <span className="visually-hidden">
            搜索项目、描述、语言、Topic、AI 总结或 AI 标签
          </span>
          <input
            type="search"
            name="q-mobile"
            autoComplete="off"
            value={draft}
            onChange={(event) => onDraftChange(event.target.value.slice(0, 200))}
            placeholder="搜索项目、language:Rust、AI 标签…"
          />
        </label>
        <div className="mobile-control-row">
          <div className="mobile-sort-group">
            <select
              className="mobile-select"
              aria-label="排序指标"
              value={sort}
              onChange={(event) => onSortChange(event.target.value as SortField)}
            >
              {SORT_FIELDS.map((field) => (
                <option key={field} value={field}>
                  {SORT_LABELS[field]}
                </option>
              ))}
            </select>
            <button
              type="button"
              className="icon-button mobile-dir-btn"
              aria-label={direction === "desc" ? "当前降序，点击切换升序" : "当前升序，点击切换降序"}
              title={direction === "desc" ? "降序" : "升序"}
              onClick={onToggleDirection}
            >
              {direction === "desc" ? "↓" : "↑"}
            </button>
          </div>
          <select
            className="mobile-select"
            aria-label="按编程语言筛选"
            value={language}
            onChange={(event) => onLanguageChange(event.target.value)}
          >
            <option value="">全部语言</option>
            {languages.map((lang) => (
              <option key={lang} value={lang}>
                {lang}
              </option>
            ))}
          </select>
          <select
            className="mobile-select"
            aria-label="按入库时长筛选"
            value={admitted}
            onChange={(event) =>
              onAdmittedChange(event.target.value as "" | AdmissionFilterValue)
            }
          >
            <option value="">全部时长</option>
            {ADMISSION_FILTERS.map((filter) => (
              <option key={filter.value} value={filter.value}>
                {filter.label}
              </option>
            ))}
          </select>
        </div>
      </div>

      {items.length ? (
        <ol
          aria-label="追踪项目"
          role="list"
          aria-busy={loading}
          className="mobile-projects"
        >
          {items.map(({ project, heat, rank }) => (
            <MobileProjectCard
              key={project.id}
              project={project}
              heat={heat}
              rank={rank}
              selected={selectedId === project.id}
              sort={sort}
              admissionLabel={formatNewAdmission(project.enteredAt, now)}
              onSelect={onSelect}
              onSearchLanguage={onSearchLanguage}
            />
          ))}
        </ol>
      ) : (
        <div className="empty-cell mobile-projects-empty">{emptyContent}</div>
      )}
    </>
  );
}
