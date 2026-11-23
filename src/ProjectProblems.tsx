import { useEffect, useId, useRef, useState, type CSSProperties } from "react";
import type { Project } from "../shared/contracts";
import { projectProblems } from "./lib/collection.ts";

export { projectProblems };

export function ProjectProblemDetails({ project }: { project: Project }) {
  return (
    <>
      <ul className="project-problem-reasons">
        {projectProblems(project).map((problem) => <li key={problem}>{problem}</li>)}
      </ul>
      <p className="project-problem-time">
        最后成功观测：{project.observedAt
          ? new Date(project.observedAt).toLocaleString("zh-CN", { hour12: false })
          : "尚无成功记录"}
      </p>
    </>
  );
}

export function ProjectProblemIndicator({ project }: { project: Project }) {
  const id = useId();
  const trigger = useRef<HTMLButtonElement>(null);
  const popup = useRef<HTMLDivElement>(null);
  const pinned = useRef(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState<CSSProperties>({});
  const cancelHide = () => clearTimeout(timer.current);
  const hide = () => {
    cancelHide();
    popup.current?.hidePopover();
  };
  const show = () => {
    cancelHide();
    const rect = trigger.current?.getBoundingClientRect();
    if (!rect || !popup.current) return;
    const width = Math.min(340, window.innerWidth - 24);
    const below = window.innerHeight - rect.bottom >= 160;
    setPosition({
      width,
      left: Math.max(12, Math.min(rect.left, window.innerWidth - width - 12)),
      top: below ? rect.bottom + 8 : "auto",
      bottom: below ? "auto" : window.innerHeight - rect.top + 8,
      maxHeight: Math.min(280, below ? window.innerHeight - rect.bottom - 20 : rect.top - 20),
    });
    popup.current.showPopover();
  };
  const scheduleHide = () => {
    cancelHide();
    if (!pinned.current) timer.current = setTimeout(hide, 180);
  };
  useEffect(() => () => clearTimeout(timer.current), []);
  useEffect(() => {
    if (!open) return;
    const dismiss = (event: Event) => {
      if (event.target instanceof Node && popup.current?.contains(event.target)) return;
      popup.current?.hidePopover();
    };
    window.addEventListener("scroll", dismiss, true);
    window.addEventListener("resize", dismiss);
    return () => {
      clearTimeout(timer.current);
      window.removeEventListener("scroll", dismiss, true);
      window.removeEventListener("resize", dismiss);
    };
  }, [open]);
  if (!projectProblems(project).length) return null;
  return (
    <>
      <button
        type="button"
        ref={trigger}
        className="project-problem-indicator"
        aria-label={`查看 ${project.name} 问题详情`}
        aria-expanded={open}
        aria-controls={id}
        aria-describedby={open ? id : undefined}
        onPointerEnter={(event) => { if (event.pointerType === "mouse") show(); }}
        onPointerLeave={scheduleHide}
        onFocus={show}
        onBlur={scheduleHide}
        onClick={(event) => {
          event.stopPropagation();
          if (pinned.current) hide();
          else { pinned.current = true; show(); }
        }}
      >
        <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" aria-hidden="true">
          <circle cx="12" cy="12" r="9" />
          <path d="M12 7v6m0 3v1" />
        </svg>
      </button>
      <div
        id={id}
        ref={popup}
        popover="auto"
        className="project-problem-popup"
        style={position}
        onPointerEnter={cancelHide}
        onPointerLeave={scheduleHide}
        onClick={(event) => event.stopPropagation()}
        onToggle={() => {
          const visible = popup.current?.matches(":popover-open") ?? false;
          setOpen(visible);
          if (!visible) { pinned.current = false; cancelHide(); }
        }}
      >
        <strong>{project.name}</strong>
        <ProjectProblemDetails project={project} />
      </div>
    </>
  );
}
