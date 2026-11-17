import { createPortal } from "react-dom";
import { useEffect, useLayoutEffect, useRef, useState } from "react";

// Measure complete tags so partially clipped labels never appear.
export function AITags({ tags, onTag }: { tags: string[]; onTag: (tag: string) => void }) {
  const root = useRef<HTMLDivElement>(null);
  const [tooltip, setTooltip] = useState<{left: number; top: number} | null>(null);
  const closeTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const keepOpen = () => clearTimeout(closeTimer.current);
  const closeSoon = () => {
    keepOpen();
    closeTimer.current = setTimeout(() => setTooltip(null), 150);
  };
  useEffect(() => () => clearTimeout(closeTimer.current), []);
  const [visible, setVisible] = useState(tags.length);
  useLayoutEffect(() => {
    const element = root.current;
    if (!element) return;
    const measure = () => {
      if (!element.isConnected) return;
      const labels = [...element.querySelectorAll<HTMLElement>(".ai-tag-measure .ai-tag:not(.ai-count-measure)")];
      const counter = element.querySelector<HTMLElement>(".ai-count-measure");
      const widths = labels.map(label => label.getBoundingClientRect().width);
      const available = element.getBoundingClientRect().width;
      let count = widths.length;
      const width = (n: number) => widths.slice(0, n).reduce((sum, w) => sum + w, 0) + Math.max(0, n - 1) * 4;
      while (count > 0 && width(count) + (count < widths.length ? (counter?.getBoundingClientRect().width ?? 30) + 4 : 0) > available) count--;
      setVisible(count);
    };
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    measure();
    document.fonts.ready.then(measure);
    return () => observer.disconnect();
  }, [tags]);
  if (!tags.length) return null;
  const hidden = tags.slice(visible);
  return <div className="ai-tags" ref={root}>
    <span className="ai-tag-measure" aria-hidden="true">{tags.map(tag => <span className="ai-tag" key={tag}>{tag}</span>)}<span className="ai-tag ai-count-measure">+{tags.length}</span></span>
    {tags.slice(0, visible).map(tag => <button type="button" className="ai-tag" key={tag} aria-label={`搜索 AI 标签 ${tag}`} onClick={event => { event.stopPropagation(); onTag(tag); }}>{tag}</button>)}
    {hidden.length > 0 && <button
      type="button"
      onMouseEnter={event => { keepOpen(); const rect = event.currentTarget.getBoundingClientRect(); setTooltip({left: Math.max(8, Math.min(rect.left, window.innerWidth - 268)), top: rect.bottom + 4}); }}
      onMouseLeave={closeSoon}
      onFocus={event => { keepOpen(); const rect = event.currentTarget.getBoundingClientRect(); setTooltip({left: Math.max(8, Math.min(rect.left, window.innerWidth - 268)), top: rect.bottom + 4}); }}
      onBlur={closeSoon}
      className="ai-tag ai-tag-overflow" aria-expanded={!!tooltip} aria-label={`更多 AI 标签：${hidden.join("、")}`} onClick={event => { event.stopPropagation(); keepOpen(); const rect = event.currentTarget.getBoundingClientRect(); setTooltip({left: Math.max(8, Math.min(rect.left, window.innerWidth - 268)), top: rect.bottom + 4}); }}>
      +{hidden.length}
    </button>}
    {tooltip && hidden.length > 0 && createPortal(<div
      className="ai-tag-tooltip" role="group" aria-label="更多 AI 标签"
      style={{left: tooltip.left, top: tooltip.top}}
      onMouseEnter={keepOpen} onMouseLeave={closeSoon} onFocus={keepOpen} onBlur={closeSoon}
      onKeyDown={event => { if (event.key === "Escape") { event.stopPropagation(); setTooltip(null); } }}
    >{hidden.map(tag => <button type="button" className="ai-tag" key={tag} aria-label={`搜索 AI 标签 ${tag}`} onClick={event => { event.stopPropagation(); setTooltip(null); onTag(tag); }}>{tag}</button>)}</div>, document.body)}
  </div>;
}
