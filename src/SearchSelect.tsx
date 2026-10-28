import {
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type CSSProperties,
} from "react";
import { createPortal } from "react-dom";

/** A searchable, single-value filter. Typing never commits a selection. */
export function SearchSelect({
  label,
  all,
  name,
  value,
  options,
  onChange,
}: {
  label: string;
  all: string;
  name: string;
  value: string;
  options: string[];
  onChange: (value: string) => void;
}) {
  const id = useId();
  const trigger = useRef<HTMLButtonElement>(null);
  const panel = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const [active, setActive] = useState(0);
  const [position, setPosition] = useState<CSSProperties>({
    top: 0,
    left: 0,
    width: 240,
    maxHeight: 360,
  });
  const choices = ["", ...new Set([...options, ...(value ? [value] : [])])];
  const filtered = choices.filter((option) =>
    (option || all)
      .toLocaleLowerCase()
      .includes(search.trim().toLocaleLowerCase()),
  );
  const close = (restore = false) => {
    setOpen(false);
    if (restore) trigger.current?.focus();
  };
  const choose = (option: string) => {
    onChange(option);
    close(true);
  };
  useLayoutEffect(() => {
    if (!open) return;
    const place = () => {
      const rect = trigger.current?.getBoundingClientRect();
      if (!rect) return;
      const viewport = window.visualViewport;
      const bottom = viewport
        ? viewport.offsetTop + viewport.height
        : innerHeight;
      const top = viewport?.offsetTop ?? 0;
      const below = bottom - rect.bottom - 16;
      const above = rect.top - top - 16;
      const flip = below < 220 && above > below;
      const maxHeight = Math.max(100, Math.min(360, flip ? above : below));
      const width = Math.min(Math.max(rect.width, 240), innerWidth - 24);
      setPosition({
        top: flip ? undefined : rect.bottom + 8,
        bottom: flip ? innerHeight - rect.top + 8 : undefined,
        left: Math.max(12, Math.min(rect.left, innerWidth - width - 12)),
        width,
        maxHeight,
      });
    };
    place();
    input.current?.focus({ preventScroll: true });
    addEventListener("resize", place);
    addEventListener("scroll", place, true);
    visualViewport?.addEventListener("resize", place);
    visualViewport?.addEventListener("scroll", place);
    return () => {
      removeEventListener("resize", place);
      removeEventListener("scroll", place, true);
      visualViewport?.removeEventListener("resize", place);
      visualViewport?.removeEventListener("scroll", place);
    };
  }, [open]);
  useEffect(() => {
    if (!open) return;
    const outside = (event: Event) => {
      const node = event.target as Node;
      if (!panel.current?.contains(node) && !trigger.current?.contains(node))
        setOpen(false);
    };
    document.addEventListener("pointerdown", outside);
    document.addEventListener("focusin", outside);
    return () => {
      document.removeEventListener("pointerdown", outside);
      document.removeEventListener("focusin", outside);
    };
  }, [open]);
  useEffect(() => {
    if (open)
      document
        .getElementById(`${id}-option-${active}`)
        ?.scrollIntoView({ block: "nearest" });
  }, [open, active, search, id]);
  return (
    <div className="select-field filter-select">
      <button
        ref={trigger}
        type="button"
        className="filter-trigger"
        name={name}
        aria-label={`${label}：${value || all}`}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-controls={open ? `${id}-panel` : undefined}
        onClick={() => {
          if (open) close();
          else {
            setSearch("");
            setActive(Math.max(0, choices.indexOf(value)));
            setOpen(true);
          }
        }}
        onKeyDown={(event) => {
          if (event.key === "ArrowDown" || event.key === "ArrowUp") {
            event.preventDefault();
            setSearch("");
            setActive(Math.max(0, choices.indexOf(value)));
            setOpen(true);
          }
        }}
      >
        <span>{value || all}</span>
        <svg
          width="14"
          height="14"
          viewBox="0 0 16 16"
          fill="none"
          aria-hidden="true"
        >
          <path d="m4 6 4 4 4-4" stroke="currentColor" strokeWidth="1.5" />
        </svg>
      </button>
      {open &&
        createPortal(
          <div
            ref={panel}
            id={`${id}-panel`}
            className="filter-popover"
            role="dialog"
            aria-label={`${label}筛选`}
            style={position}
            onKeyDown={(event) => {
              if (event.key === "Escape") {
                event.preventDefault();
                close(true);
              } else if (event.key === "Tab") {
                event.preventDefault();
                const stops = [
                  ...document.querySelectorAll<HTMLElement>(
                    "a[href],button,input,select,textarea,summary,[tabindex='0']",
                  ),
                ].filter(
                  (element) =>
                    !panel.current?.contains(element) &&
                    !element.matches(":disabled") &&
                    element.getClientRects().length > 0,
                );
                const index = stops.indexOf(trigger.current!);
                const next = stops[index + (event.shiftKey ? -1 : 1)];
                close();
                (next || trigger.current)?.focus();
              }
            }}
          >
            <div className="filter-search">
              <label className="visually-hidden" htmlFor={`${id}-search`}>
                搜索{label}
              </label>
              <input
                ref={input}
                id={`${id}-search`}
                role="combobox"
                type="search"
                autoComplete="off"
                placeholder={`搜索${label}…`}
                value={search}
                aria-expanded="true"
                aria-controls={`${id}-options`}
                aria-autocomplete="list"
                aria-activedescendant={
                  filtered.length
                    ? `${id}-option-${Math.min(active, filtered.length - 1)}`
                    : undefined
                }
                onChange={(event) => {
                  setSearch(event.target.value);
                  setActive(0);
                }}
                onKeyDown={(event) => {
                  if (event.nativeEvent.isComposing) return;
                  if (event.key === "ArrowDown" || event.key === "ArrowUp") {
                    event.preventDefault();
                    setActive((previous) =>
                      filtered.length
                        ? (previous +
                            (event.key === "ArrowDown" ? 1 : -1) +
                            filtered.length) %
                          filtered.length
                        : 0,
                    );
                  } else if (event.key === "Enter") {
                    event.preventDefault();
                    const option =
                      filtered[Math.min(active, filtered.length - 1)];
                    if (option !== undefined) choose(option);
                  }
                }}
              />
            </div>
            <div
              id={`${id}-options`}
              className="filter-options"
              role="listbox"
              aria-label={label}
            >
              {filtered.map((option, index) => (
                <div
                  key={option}
                  id={`${id}-option-${index}`}
                  role="option"
                  aria-selected={value === option}
                  className={
                    active === index
                      ? "filter-option active-option"
                      : "filter-option"
                  }
                  onPointerMove={() => setActive(index)}
                  onMouseDown={(event) => event.preventDefault()}
                  onClick={() => choose(option)}
                >
                  <span>{option || all}</span>
                  <span className="option-check" aria-hidden="true">
                    {value === option ? "✓" : ""}
                  </span>
                </div>
              ))}
            </div>
            {!filtered.length && (
              <p className="filter-no-results" role="status">
                无匹配选项
              </p>
            )}
          </div>,
          document.body,
        )}
    </div>
  );
}
