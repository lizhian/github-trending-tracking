import type { TrendSummary, WindowHours } from "../../shared/contracts.ts";

export const WINDOW_LABELS: Record<WindowHours, string> = {
  24: "1 天",
  72: "3 天",
  168: "7 天",
  720: "30 天",
};

const numberFmt = new Intl.NumberFormat("zh-CN");
const heatFmt = new Intl.NumberFormat("zh-CN", {
  minimumFractionDigits: 1,
  maximumFractionDigits: 1,
});
const dateTimeFmt = new Intl.DateTimeFormat("zh-CN", {
  year: "numeric",
  month: "2-digit",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hourCycle: "h23",
});
const chartDateFmt = new Intl.DateTimeFormat("zh-CN", {
  timeZone: "Asia/Shanghai",
  year: "numeric",
  month: "2-digit",
  day: "2-digit",
  hour: "2-digit",
  hourCycle: "h23",
});
const chartAxisDateFmt = new Intl.DateTimeFormat("zh-CN", {
  timeZone: "Asia/Shanghai",
  month: "numeric",
  day: "numeric",
});

export const enCollator = new Intl.Collator("en");

export function formatNumber(value: number): string {
  return numberFmt.format(value);
}

export function formatHeat(value: number): string {
  return value > 1000 ? `${(value / 1000).toFixed(1)}k` : heatFmt.format(value);
}

export function formatSigned(value: number | null): string {
  return value === null
    ? "积累中"
    : `${value < 0 ? "−" : "+"}${numberFmt.format(Math.abs(value))}`;
}

export function formatCompact(value: number): string {
  return Math.abs(value) > 1000
    ? `${(value / 1000).toFixed(1)}k`
    : numberFmt.format(value);
}

export function formatCompactSigned(value: number | null): string {
  return value === null
    ? "积累中"
    : `${value < 0 ? "−" : "+"}${formatCompact(Math.abs(value))}`;
}

export function formatTime(value: number | null | undefined): string {
  return value ? dateTimeFmt.format(value) : "尚无成功记录";
}

export function formatChartAxisDate(timestamp: number): string {
  return chartAxisDateFmt.format(timestamp);
}

function chartDateParts(timestamp: number): Record<string, string> {
  return Object.fromEntries(
    chartDateFmt.formatToParts(timestamp).map(({ type, value }) => [type, value]),
  );
}

export function formatChartPointTime(timestamp: number): string {
  const parts = chartDateParts(timestamp);
  const date = `${parts.year}-${parts.month}-${parts.day}`;
  return parts.hour === "00" ? date : `${date} ${parts.hour}时`;
}

export function formatChartIntervalTime(
  from: number,
  to: number,
): { date: string; duration: string } {
  const parts = chartDateParts(to);
  const date = `${parts.year}-${parts.month}-${parts.day}`;
  const hour = 3_600_000;
  const hours = Math.floor(to / hour) - Math.floor(from / hour);
  if (hours > 0 && hours % 24 === 0 && parts.hour === "00") {
    return { date, duration: `${hours / 24}d` };
  }
  const minutes = Math.floor((to - from) / 60_000);
  const duration = hours > 0 ? `${hours}h` : minutes === 0 ? "<1m" : `${minutes}m`;
  return { date: `${date} ${parts.hour}时`, duration };
}

export function formatNewAdmission(enteredAt: number, now: number): string | null {
  const elapsed = now - enteredAt;
  if (!Number.isFinite(enteredAt) || elapsed < 0 || elapsed >= 24 * 3_600_000) {
    return null;
  }
  const hours = Math.floor(elapsed / 3_600_000);
  return hours === 0 ? "新 · 刚刚" : `新 · ${hours}小时前`;
}

export function trendStateLabel(trend: TrendSummary): string {
  if (trend.basis === "unknown") return "暂不可知";
  if (trend.basis === "carried") return "含持平计算";
  if (trend.state === "accumulating") return "积累中";
  if (trend.state === "partial") return "部分观测";
  return "完整观测";
}

export function parseHomepageURL(value: string | null): URL | null {
  if (!value?.trim()) return null;
  const address = value.trim();
  try {
    const url = new URL(
      address.startsWith("//")
        ? `https:${address}`
        : /^[a-z][a-z\d+.-]*:/i.test(address)
          ? address
          : `https://${address}`,
    );
    return url.protocol === "https:" || url.protocol === "http:" ? url : null;
  } catch {
    return null;
  }
}
