import { useCallback, useEffect, useState } from "react";

export type ThemeMode = "system" | "light" | "dark";

const STORAGE_KEY = "theme-mode";
const THEME_COLORS = {
  light: "#f5f5f7",
  dark: "#111113",
} as const;

function readSavedTheme(): ThemeMode {
  try {
    const saved = localStorage.getItem(STORAGE_KEY);
    return saved === "light" || saved === "dark" ? saved : "system";
  } catch {
    return "system";
  }
}

function readSystemDark(): boolean {
  return window.matchMedia("(prefers-color-scheme: dark)").matches;
}

function applyThemeToDocument(mode: ThemeMode, systemDark: boolean): "light" | "dark" {
  const root = document.documentElement;
  if (mode === "system") {
    delete root.dataset.theme;
  } else {
    root.dataset.theme = mode;
  }
  const resolved = mode === "system" ? (systemDark ? "dark" : "light") : mode;
  const meta = document.querySelector<HTMLMetaElement>('meta[name="theme-color"]');
  if (meta) {
    meta.content = THEME_COLORS[resolved];
  }
  return resolved;
}

export function useTheme() {
  const [mode, setMode] = useState<ThemeMode>(readSavedTheme);
  const [systemDark, setSystemDark] = useState<boolean>(readSystemDark);

  useEffect(() => {
    const media = window.matchMedia("(prefers-color-scheme: dark)");
    const update = () => setSystemDark(media.matches);
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);

  const resolved = mode === "system" ? (systemDark ? "dark" : "light") : mode;

  useEffect(() => {
    applyThemeToDocument(mode, systemDark);
  }, [mode, systemDark]);

  const cycleTheme = useCallback(() => {
    setMode((previous) => {
      const next: ThemeMode =
        previous === "system" ? "light" : previous === "light" ? "dark" : "system";
      try {
        if (next === "system") localStorage.removeItem(STORAGE_KEY);
        else localStorage.setItem(STORAGE_KEY, next);
      } catch {
        /* Storage is optional. */
      }
      return next;
    });
  }, []);

  return { mode, resolved, cycleTheme };
}
