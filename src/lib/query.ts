import { WINDOWS, type Project, type WindowHours } from "../../shared/contracts.ts";
import { projectHeat } from "../heat.ts";
import { enCollator } from "./format.ts";

export const ADMISSION_FILTERS = [
  { value: "24", label: "<1天", days: 1, atLeast: false },
  { value: "gte24", label: ">1天", days: 1, atLeast: true },
  { value: "72", label: "<3天", days: 3, atLeast: false },
  { value: "gte72", label: ">3天", days: 3, atLeast: true },
  { value: "168", label: "<7天", days: 7, atLeast: false },
  { value: "gte168", label: ">7天", days: 7, atLeast: true },
] as const;

export type AdmissionFilter = (typeof ADMISSION_FILTERS)[number];
export type AdmissionFilterValue = AdmissionFilter["value"];
export const ADMISSION_OPTIONS: string[] = ADMISSION_FILTERS.map(({ value }) => value);

export function admissionFilterFor(value: string): AdmissionFilter | undefined {
  return ADMISSION_FILTERS.find((filter) => filter.value === value);
}

export const SORT_FIELDS = ["name", "heat", "stars", "24", "72", "168", "720"] as const;
export type SortField = (typeof SORT_FIELDS)[number];
export const DEFAULT_SORT: SortField = "24";

export const SORT_LABELS: Record<SortField, string> = {
  "24": "1 天净增量",
  "72": "3 天净增量",
  "168": "7 天净增量",
  "720": "30 天净增量",
  stars: "Star 总数",
  heat: "热度",
  name: "项目名称",
};

export interface QueryState {
  q: string;
  admitted: "" | AdmissionFilterValue;
  tab: "github" | "linuxdo";
  sort: SortField;
  direction: "asc" | "desc";
  project: string;
  window: WindowHours;
}

export interface ParsedSearch {
  language: string | null;
  keywords: string[];
}

const LANG_MATCH_RE = /(?:^|\s)language:(?:"([^"]+)"|(\S+))/gi;
const LANG_STRIP_RE = /(?:^|\s)language:(?:"[^"]*"|\S*)/gi;

export function formatLanguageToken(language: string): string {
  const trimmed = language.trim();
  return /\s/.test(trimmed) ? `language:"${trimmed}"` : `language:${trimmed}`;
}

export function parseSearchQuery(raw: string): ParsedSearch {
  let language: string | null = null;
  const remainder = raw.replace(LANG_MATCH_RE, (_full, quoted: string | undefined, single: string | undefined) => {
    const candidate = (quoted ?? single ?? "").trim();
    if (candidate) language = candidate;
    return " ";
  });
  const keywords = remainder
    .trim()
    .toLowerCase()
    .split(/\s+/)
    .filter(Boolean);
  return { language, keywords };
}

export function removeLanguageFromQuery(raw: string): string {
  return raw.replace(LANG_STRIP_RE, " ").trim().replace(/\s+/g, " ");
}

export function removeKeywordsFromQuery(raw: string): string {
  const { language } = parseSearchQuery(raw);
  return language ? formatLanguageToken(language) : "";
}

export function applyLanguageFilter(raw: string, language: string): string {
  const rest = removeLanguageFromQuery(raw);
  const trimmed = language.trim();
  if (!trimmed) return rest;
  const token = formatLanguageToken(trimmed);
  return rest ? `${token} ${rest}` : token;
}

export function applyTagFilter(raw: string, tag: string): string {
  const { language } = parseSearchQuery(raw);
  const trimmedTag = tag.trim();
  if (!language) return trimmedTag;
  const token = formatLanguageToken(language);
  return trimmedTag ? `${token} ${trimmedTag}` : token;
}

export function parseLocation(search: string): QueryState {
  const params = new URLSearchParams(search);
  const text = (key: string) => (params.get(key) ?? "").slice(0, 200);
  const qRaw = text("q");
  const langParam = text("language").trim();
  const topicParam = text("topic").trim();
  let q = [qRaw, topicParam].filter(Boolean).join(" ").trim();
  if (langParam) {
    q = applyLanguageFilter(q, langParam);
  }
  const sortRaw = text("sort") as SortField;
  const hours = Number(text("window")) as WindowHours;
  return {
    q: q.slice(0, 200),
    admitted: admissionFilterFor(text("admitted"))?.value ?? "",
    tab: text("tab") === "linuxdo" ? "linuxdo" : "github",
    sort: SORT_FIELDS.includes(sortRaw) ? sortRaw : DEFAULT_SORT,
    direction: text("direction") === "asc" ? "asc" : "desc",
    project: text("project"),
    window: WINDOWS.includes(hours) ? hours : 720,
  };
}

export function serializeLocation(
  query: QueryState,
  pathname: string,
  hash: string,
): string {
  const params = new URLSearchParams();
  if (query.q) params.set("q", query.q);
  if (query.admitted) params.set("admitted", query.admitted);
  if (query.sort !== DEFAULT_SORT) params.set("sort", query.sort);
  if (query.direction !== "desc") params.set("direction", query.direction);
  if (query.tab === "linuxdo") params.set("tab", "linuxdo");
  if (query.project) params.set("project", query.project);
  if (query.window !== 720) params.set("window", String(query.window));
  return `${pathname}${params.size ? `?${params}` : ""}${hash}`;
}

export interface IndexedProject {
  project: Project;
  heat: number;
  searchText: string;
  languageLower: string;
}

export interface RankedProject extends IndexedProject {
  rank: number;
}

export function indexProjects(items: Project[]): IndexedProject[] {
  return items.map((project) => ({
    project,
    heat: projectHeat(project),
    languageLower: project.language?.trim().toLowerCase() ?? "",
    searchText: [
      project.name,
      project.description ?? "",
      project.language ?? "",
      project.topics.join(" "),
      project.aiSummary ?? "",
      (project.aiTags ?? []).join(" "),
    ]
      .join(" ")
      .toLowerCase(),
  }));
}

export function rankProjects(
  indexed: IndexedProject[],
  sort: SortField,
  direction: "asc" | "desc",
): RankedProject[] {
  const sorted = [...indexed].sort((a, b) => {
    let order: number;
    if (sort === "name") {
      order = enCollator.compare(
        a.project.name.toLowerCase(),
        b.project.name.toLowerCase(),
      );
    } else {
      const av =
        sort === "heat"
          ? a.heat
          : sort === "stars"
            ? a.project.stars
            : a.project.trends[Number(sort) as WindowHours].delta;
      const bv =
        sort === "heat"
          ? b.heat
          : sort === "stars"
            ? b.project.stars
            : b.project.trends[Number(sort) as WindowHours].delta;
      if (av === null || bv === null) {
        if (av !== bv) return av === null ? 1 : -1;
        return enCollator.compare(a.project.name, b.project.name);
      }
      order = av - bv;
    }
    return (
      (direction === "desc" ? -order : order) ||
      enCollator.compare(a.project.name, b.project.name)
    );
  });
  return sorted.map((item, index) => ({ ...item, rank: index + 1 }));
}

export function matchesAdmissionFilter(
  enteredAt: number,
  now: number,
  filter?: AdmissionFilter,
): boolean {
  if (!filter) return true;
  const elapsed = now - enteredAt;
  const threshold = filter.days * 86_400_000;
  return elapsed >= 0 && (filter.atLeast ? elapsed >= threshold : elapsed < threshold);
}

export function filterProjects(
  ranked: RankedProject[],
  q: string,
  admitted: "" | AdmissionFilterValue,
  now: number,
): RankedProject[] {
  const { language, keywords } = parseSearchQuery(q);
  const languageTarget = language ? language.toLowerCase() : null;
  const admissionFilter = admissionFilterFor(admitted);
  if (languageTarget === null && keywords.length === 0 && !admissionFilter) {
    return ranked;
  }
  return ranked.filter((item) => {
    if (languageTarget !== null && item.languageLower !== languageTarget) {
      return false;
    }
    if (
      keywords.length > 0 &&
      !keywords.every((keyword) => item.searchText.includes(keyword))
    ) {
      return false;
    }
    return matchesAdmissionFilter(item.project.enteredAt, now, admissionFilter);
  });
}
