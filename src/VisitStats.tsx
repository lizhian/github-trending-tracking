import { useEffect, useState } from "react";

interface VisitCounts {
  today: number;
  total: number;
}

const endpoint = `${(import.meta.env.VITE_API_URL ?? "").replace(/\/$/, "")}/api/visits`;
let pageVisit: Promise<VisitCounts> | undefined;
async function fetchVisits(method: "GET" | "POST"): Promise<VisitCounts> {
  const response = await fetch(endpoint, { method, cache: "no-store" });
  if (!response.ok) throw new Error("访问统计暂不可用");
  return response.json();
}

export function VisitStats() {
  const [counts, setCounts] = useState<VisitCounts | null>(null);
  useEffect(() => {
    let active = true;
    const update = (request: Promise<VisitCounts>) => {
      void request.then((value) => {
        if (active) setCounts(value);
      }).catch(() => {
        if (active) setCounts(null);
      });
    };
    // 每个文档仅登记一次，React StrictMode 重放及组件重新挂载共用请求。
    pageVisit ??= fetchVisits("POST");
    update(pageVisit);
    const timer = window.setInterval(() => update(fetchVisits("GET")), 60_000);
    return () => {
      active = false;
      window.clearInterval(timer);
    };
  }, []);
  const format = (value: number | undefined) =>
    value === undefined ? "—" : value.toLocaleString("zh-CN");
  return (
    <span className="visit-stats" aria-label="全站访问统计">
      <span>今日访问数 <strong>{format(counts?.today)}</strong></span>
      <span>总访问数 <strong>{format(counts?.total)}</strong></span>
    </span>
  );
}
