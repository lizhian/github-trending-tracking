export type IconName =
  | "mark"
  | "search"
  | "reset"
  | "github"
  | "external"
  | "previous"
  | "next"
  | "bell"
  | "sun"
  | "moon"
  | "monitor"
  | "close"
  | "barChart"
  | "lineChart";

export function Icon({ name, size = 16 }: { name: IconName; size?: number }) {
  const paths = {
    mark: (
      <>
        <path d="M3 21V3h6v18M15 21V3h6v18M3 12h18" />
        <path className="mark-signal" d="m6 18 6-6 6-6" />
      </>
    ),
    search: (
      <>
        <circle cx="10.5" cy="10.5" r="6.5" />
        <path d="m16 16 5 5" />
      </>
    ),
    reset: <path d="M4 10a8 8 0 1 1 1 8M4 4v6h6" />,
    github: (
      <path d="M9 21v-4c-4 1-4-2-6-2m12 6v-4a3.5 3.5 0 0 0-1-3c3-.4 6-1.5 6-6a4.7 4.7 0 0 0-1-3c.1-1 .1-2-1-3-2 0-3 1-3 1a12 12 0 0 0-6 0s-1-1-3-1c-1 1-1 2-1 3a4.7 4.7 0 0 0-1 3c0 4.5 3 5.6 6 6a3.5 3.5 0 0 0-1 3" />
    ),
    external: <path d="M14 3h7v7M21 3 10 14M10 3H3v18h18v-7" />,
    previous: <path d="m15 5-7 7 7 7" />,
    next: <path d="m9 5 7 7-7 7" />,
    bell: <path d="M18 8a6 6 0 0 0-12 0c0 7-3 7-3 9h18c0-2-3-2-3-9M10 21h4" />,
    sun: (
      <>
        <circle cx="12" cy="12" r="4" />
        <path d="M12 2v2m0 16v2M4.93 4.93l1.41 1.41m11.32 11.32 1.41 1.41M2 12h2m16 0h2M4.93 19.07l1.41-1.41m11.32-11.32 1.41-1.41" />
      </>
    ),
    moon: <path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79Z" />,
    monitor: (
      <>
        <rect x="2" y="3" width="20" height="14" rx="2" />
        <path d="M8 21h8m-4-4v4" />
      </>
    ),
    close: <path d="M18 6 6 18M6 6l12 12" />,
    barChart: <path d="M5 20v-7m5 7V7m5 13v-4m5 4V4" />,
    lineChart: (
      <>
        <path d="M3 3v18h18" />
        <path d="m7 15 4-4 4 3 5-7" />
      </>
    ),
  };

  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {paths[name]}
    </svg>
  );
}
