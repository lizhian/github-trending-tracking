const DEFAULT_FONT_FAMILY =
  '-apple-system, BlinkMacSystemFont, "Helvetica Neue", "PingFang SC", "Microsoft YaHei", system-ui, sans-serif';

const textWidthCache = new Map<string, number>();
let cachedCtx: CanvasRenderingContext2D | null | undefined;
let cachedFontFamily: string | null = null;

function getMeasureContext(): CanvasRenderingContext2D | null {
  if (cachedCtx !== undefined) return cachedCtx;
  if (typeof document === "undefined") {
    cachedCtx = null;
    return null;
  }
  const canvas = document.createElement("canvas");
  cachedCtx = canvas.getContext("2d");
  return cachedCtx;
}

export function clearTextWidthCache(): void {
  textWidthCache.clear();
  cachedFontFamily = null;
}

export function getBodyFontFamily(): string {
  if (cachedFontFamily !== null) return cachedFontFamily;
  if (typeof document === "undefined" || typeof getComputedStyle === "undefined") {
    return DEFAULT_FONT_FAMILY;
  }
  const family = getComputedStyle(document.body).fontFamily.trim();
  cachedFontFamily = family || DEFAULT_FONT_FAMILY;
  return cachedFontFamily;
}

export function measureTextWidth(text: string, font: string): number {
  if (!text) return 0;
  const key = `${font}\0${text}`;
  const cached = textWidthCache.get(key);
  if (cached !== undefined) return cached;

  const ctx = getMeasureContext();
  let width: number;
  if (ctx) {
    ctx.font = font;
    width = ctx.measureText(text).width;
  } else {
    // Fallback for non-browser test environments.
    width = text.length * 10;
  }
  if (textWidthCache.size > 4000) {
    textWidthCache.clear();
  }
  textWidthCache.set(key, width);
  return width;
}

/**
 * Computes the maximum number of complete tags that fit within `availableWidth`.
 * When all tags fit, returns `tagWidths.length` (no `+N` badge needed).
 * Otherwise reserves space for the `+N` overflow badge and returns `0..tagWidths.length - 1`.
 */
export function fitTagCount(
  tagWidths: readonly number[],
  overflowWidth: (hiddenCount: number) => number,
  availableWidth: number,
  gap = 4,
): number {
  const total = tagWidths.length;
  if (total === 0) return 0;
  if (availableWidth <= 0) return 0;

  let allWidth = (total - 1) * gap;
  for (let i = 0; i < total; i++) {
    allWidth += tagWidths[i];
  }
  if (allWidth <= availableWidth) {
    return total;
  }

  // Prefix sums for O(N) scan from most tags to fewest.
  const prefix = new Array<number>(total + 1);
  prefix[0] = 0;
  for (let i = 0; i < total; i++) {
    prefix[i + 1] = prefix[i] + tagWidths[i];
  }

  for (let count = total - 1; count >= 1; count--) {
    const hidden = total - count;
    const needed = prefix[count] + count * gap + overflowWidth(hidden);
    if (needed <= availableWidth) {
      return count;
    }
  }

  return 0;
}

const TAG_PILL_CHROME = 16; // 6px*2 padding + 1px*2 border + 2px subpixel cushion
const LANG_ICON_WIDTH = 21; // 15px SVG + 3px*2 padding
const TITLE_BASE_GAPS = 12; // 2 flex gaps (6px each) between repo-button, lang, ai-tags
const PROBLEM_BTN_SPACE = 34; // 28px indicator + 6px flex gap
const ROW_SAFETY_CUSHION = 8;

export function computeVisibleTagCount({
  tags,
  repoName,
  admissionLabel,
  hasProblems,
  cellContentWidth,
}: {
  tags: readonly string[];
  repoName: string;
  admissionLabel: string | null;
  hasProblems: boolean;
  cellContentWidth: number;
}): number {
  if (!tags.length) return 0;
  if (cellContentWidth <= 0) return Math.min(2, tags.length);

  const family = getBodyFontFamily();
  const slashIdx = repoName.indexOf("/");
  const owner = slashIdx === -1 ? repoName : repoName.slice(0, slashIdx);
  const repo = slashIdx === -1 ? "" : repoName.slice(slashIdx + 1);

  let rawButtonWidth =
    measureTextWidth(owner, `400 12px ${family}`) +
    measureTextWidth("/", `400 15px ${family}`) +
    8 + // .slash margin-inline: 4px * 2
    measureTextWidth(repo, `600 15px ${family}`);

  if (admissionLabel) {
    rawButtonWidth +=
      8 + measureTextWidth(admissionLabel, `500 11px ${family}`);
  }

  const buttonWidth = Math.min(
    Math.ceil(rawButtonWidth),
    Math.floor(cellContentWidth * 0.68),
  );

  const availableForTags =
    cellContentWidth -
    buttonWidth -
    LANG_ICON_WIDTH -
    TITLE_BASE_GAPS -
    (hasProblems ? PROBLEM_BTN_SPACE : 0) -
    ROW_SAFETY_CUSHION;

  const tagFont = `400 10px ${family}`;
  const tagWidths = tags.map(
    (tag) => Math.ceil(measureTextWidth(tag, tagFont)) + TAG_PILL_CHROME,
  );
  const overflowWidth = (hidden: number) =>
    Math.ceil(measureTextWidth(`+${hidden}`, tagFont)) + TAG_PILL_CHROME;

  return fitTagCount(tagWidths, overflowWidth, availableForTags, 4);
}
