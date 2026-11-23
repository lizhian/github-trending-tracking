import { createPortal } from "react-dom";
import { useEffect, useRef, useState, type CSSProperties } from "react";

export function AITags({
  tags,
  visibleCount,
  onTag,
}: {
  tags: string[];
  visibleCount?: number;
  onTag: (tag: string) => void;
}) {
  const triggerRef = useRef<HTMLButtonElement>(null);
  const tooltipRef = useRef<HTMLDivElement>(null);
  const closeTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const [tooltipStyle, setTooltipStyle] = useState<CSSProperties | null>(null);

  const keepOpen = () => clearTimeout(closeTimer.current);
  const closeNow = () => {
    keepOpen();
    setTooltipStyle(null);
  };
  const closeSoon = () => {
    keepOpen();
    closeTimer.current = setTimeout(() => setTooltipStyle(null), 150);
  };

  const openTooltip = (element: HTMLElement) => {
    keepOpen();
    const rect = element.getBoundingClientRect();
    const left = Math.max(8, Math.min(rect.left, window.innerWidth - 268));
    const spaceBelow = window.innerHeight - rect.bottom;
    const spaceAbove = rect.top;
    const flip = spaceBelow < 140 && spaceAbove > spaceBelow;
    setTooltipStyle(
      flip
        ? { left, bottom: window.innerHeight - rect.top + 4 }
        : { left, top: rect.bottom + 4 },
    );
  };

  useEffect(() => () => clearTimeout(closeTimer.current), []);

  useEffect(() => {
    if (!tooltipStyle) return;
    const dismiss = () => setTooltipStyle(null);
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as Node | null;
      if (
        tooltipRef.current?.contains(target) ||
        triggerRef.current?.contains(target)
      ) {
        return;
      }
      setTooltipStyle(null);
    };
    window.addEventListener("scroll", dismiss, true);
    window.addEventListener("resize", dismiss);
    document.addEventListener("pointerdown", onPointerDown);
    return () => {
      window.removeEventListener("scroll", dismiss, true);
      window.removeEventListener("resize", dismiss);
      document.removeEventListener("pointerdown", onPointerDown);
    };
  }, [tooltipStyle]);

  if (!tags.length) return null;

  const limit =
    visibleCount === undefined
      ? tags.length
      : Math.max(0, Math.min(tags.length, visibleCount));
  const visibleTags = tags.slice(0, limit);
  const hiddenTags = tags.slice(limit);
  const open = tooltipStyle !== null;

  return (
    <div className="ai-tags">
      {visibleTags.map((tag) => (
        <button
          type="button"
          className="ai-tag"
          key={tag}
          aria-label={`搜索 AI 标签 ${tag}`}
          onClick={(event) => {
            event.stopPropagation();
            onTag(tag);
          }}
        >
          {tag}
        </button>
      ))}
      {hiddenTags.length > 0 && (
        <button
          ref={triggerRef}
          type="button"
          className="ai-tag ai-tag-overflow"
          aria-expanded={open}
          aria-label={`更多 AI 标签：${hiddenTags.join("、")}`}
          onMouseEnter={(event) => openTooltip(event.currentTarget)}
          onMouseLeave={closeSoon}
          onFocus={(event) => openTooltip(event.currentTarget)}
          onBlur={closeSoon}
          onClick={(event) => {
            event.stopPropagation();
            if (open) closeNow();
            else openTooltip(event.currentTarget);
          }}
        >
          +{hiddenTags.length}
        </button>
      )}
      {tooltipStyle &&
        hiddenTags.length > 0 &&
        createPortal(
          <div
            ref={tooltipRef}
            className="ai-tag-tooltip"
            role="group"
            aria-label="更多 AI 标签"
            style={tooltipStyle}
            onMouseEnter={keepOpen}
            onMouseLeave={closeSoon}
            onFocus={keepOpen}
            onBlur={closeSoon}
            onKeyDown={(event) => {
              if (event.key === "Escape") {
                event.stopPropagation();
                closeNow();
                triggerRef.current?.focus();
              }
            }}
          >
            {hiddenTags.map((tag) => (
              <button
                type="button"
                className="ai-tag"
                key={tag}
                aria-label={`搜索 AI 标签 ${tag}`}
                onClick={(event) => {
                  event.stopPropagation();
                  closeNow();
                  onTag(tag);
                }}
              >
                {tag}
              </button>
            ))}
          </div>,
          document.body,
        )}
    </div>
  );
}
