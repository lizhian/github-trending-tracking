import { useEffect, useState } from "react";

export const apiBase = (import.meta.env.VITE_API_URL ?? "").replace(/\/$/, "");

const MAX_CACHE_ENTRIES = 40;
const apiCache = new Map<string, unknown>();

function readCache<T>(path: string): T | undefined {
  if (!apiCache.has(path)) return undefined;
  const value = apiCache.get(path) as T;
  // Refresh LRU recency.
  apiCache.delete(path);
  apiCache.set(path, value);
  return value;
}

function writeCache<T>(path: string, value: T): void {
  if (apiCache.has(path)) {
    apiCache.delete(path);
  } else if (apiCache.size >= MAX_CACHE_ENTRIES) {
    const oldest = apiCache.keys().next().value;
    if (oldest !== undefined) apiCache.delete(oldest);
  }
  apiCache.set(path, value);
}

export function clearApiCache(prefix?: string): void {
  if (!prefix) {
    apiCache.clear();
    return;
  }
  for (const key of apiCache.keys()) {
    if (key.startsWith(prefix)) apiCache.delete(key);
  }
}

export interface Resource<T> {
  path: string;
  value: T | null;
  loading: boolean;
  refreshing: boolean;
  error: string;
}

export function useApi<T>(
  path: string,
  revision: string | number = 0,
  enabled = true,
): Resource<T> {
  const [resource, setResource] = useState<Resource<T>>(() => {
    const cached = enabled ? readCache<T>(path) : undefined;
    return {
      path: enabled ? path : "",
      value: cached ?? null,
      loading: enabled && cached === undefined,
      refreshing: enabled && cached !== undefined,
      error: "",
    };
  });

  useEffect(() => {
    if (!enabled) return;
    const controller = new AbortController();
    const cached = readCache<T>(path);
    setResource((previous) => {
      const value =
        cached !== undefined
          ? cached
          : previous.path === path
            ? previous.value
            : null;
      return {
        path,
        value,
        loading: value === null,
        refreshing: value !== null,
        error: "",
      };
    });

    async function read() {
      try {
        const response = await fetch(`${apiBase}${path}`, {
          signal: controller.signal,
          cache: "no-store",
        });
        if (!response.ok) {
          const body = (await response.json().catch(() => null)) as {
            error?: string;
          } | null;
          throw new Error(body?.error || "请求失败，请稍后重试");
        }
        const value = (await response.json()) as T;
        writeCache(path, value);
        if (!controller.signal.aborted) {
          setResource({
            path,
            value,
            loading: false,
            refreshing: false,
            error: "",
          });
        }
      } catch (error) {
        if (!controller.signal.aborted) {
          setResource((previous) => {
            const fallback =
              previous.path === path ? previous.value : (cached ?? null);
            return {
              path,
              value: fallback,
              loading: false,
              refreshing: false,
              error:
                error instanceof Error
                  ? error.message
                  : "请求失败，请稍后重试",
            };
          });
        }
      }
    }

    void read();
    return () => controller.abort();
  }, [path, revision, enabled]);

  if (!enabled) {
    return {
      path,
      value: null,
      loading: false,
      refreshing: false,
      error: "",
    };
  }

  if (resource.path === path) {
    return resource;
  }

  const cached = readCache<T>(path);
  return {
    path,
    value: cached ?? null,
    loading: cached === undefined,
    refreshing: cached !== undefined,
    error: "",
  };
}
