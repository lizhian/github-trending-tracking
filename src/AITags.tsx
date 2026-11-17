import { createPortal } from "react-dom";
import { useLayoutEffect, useRef, useState } from "react";

// Measure complete tags so partially clipped labels never appear.
export function AITags({ tags }: { tags: string[] }) {
  const root = useRef<HTMLDivElement>(null);
  const [tooltip, setTooltip] = useState<{left: number; top: number} | null>(null);
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
    {tags.slice(0, visible).map(tag => <span className="ai-tag" key={tag}>{tag}</span>)}
    {hidden.length > 0 && <span
      onMouseEnter={event => { const rect = event.currentTarget.getBoundingClientRect(); setTooltip({left: Math.max(8, Math.min(rect.left, window.innerWidth - 268)), top: rect.bottom + 4}); }}
      onMouseLeave={() => setTooltip(null)}
      onFocus={event => { const rect = event.currentTarget.getBoundingClientRect(); setTooltip({left: Math.max(8, Math.min(rect.left, window.innerWidth - 268)), top: rect.bottom + 4}); }}
      onBlur={() => setTooltip(null)} className="ai-tag-overflow" tabIndex={0} aria-label={`隐藏标签：${hidden.join("、")}`} onClick={event => event.stopPropagation()}>
      <span className="ai-tag">+{hidden.length}</span>

    </span>}
    {tooltip && hidden.length > 0 && createPortal(<span className="ai-tag-tooltip" role="tooltip" style={{left: tooltip.left, top: tooltip.top}}>{hidden.join(" · ")}</span>, document.body)}
  </div>;
}
