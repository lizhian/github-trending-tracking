import { DeferredRequest, type Upstream } from "./upstream";

export interface RepositoryStars {
  id: string;
  stargazerCount: number;
}
export interface Repository extends RepositoryStars {
  nameWithOwner: string;
  description: string | null;
  primaryLanguage: { name: string } | null;
  repositoryTopics: { nodes: { topic: { name: string } }[] };
}
const FIELDS =
  "id nameWithOwner description primaryLanguage { name } repositoryTopics(first:100) { nodes { topic { name } } } stargazerCount";

export function repositories(token: string, targets: string[], byId: boolean, upstream: Upstream, metadata?: true): Promise<(Repository | null)[]>;
export function repositories(token: string, targets: string[], byId: boolean, upstream: Upstream, metadata: false): Promise<(RepositoryStars | null)[]>;
export async function repositories(
  token: string,
  targets: string[],
  byId: boolean,
  upstream: Upstream,
  metadata = true,
): Promise<(Repository | RepositoryStars | null)[]> {
  if (!targets.length) return [];
  const fields = metadata ? FIELDS : "id stargazerCount";
  const query = byId
    ? `query { nodes(ids:${JSON.stringify(targets)}) { ... on Repository { ${fields} } } }`
    : `query { ${targets
        .map((name, i) => {
          const [owner, repo] = name.split("/");
          return `r${i}: repository(owner:${JSON.stringify(owner)},name:${JSON.stringify(repo)}) { ${fields} }`;
        })
        .join(" ")} }`;
  const url = "https://api.github.com/graphql";
  let result: {
    data?: { nodes?: unknown[]; [key: string]: unknown };
    errors?: { type?: string; message?: string; extensions?: { code?: string } }[];
  };
  try {
    const body = await upstream.text(url, "GitHub", {
      method: "POST",
      headers: {
        Authorization: `Bearer ${token}`,
        "Content-Type": "application/json",
        "User-Agent": "github-trending-tracking",
      },
      body: JSON.stringify({ query }),
    });
    result = JSON.parse(body);
    if (result.errors?.some((error) =>
      error.type === "RATE_LIMITED" || error.extensions?.code === "RATE_LIMITED" ||
      /rate limit/i.test(error.message ?? "")
    )) upstream.cooldown(url);
    if (!result.data) throw new Error("GitHub GraphQL unavailable");
  } catch (error) {
    if (!(error instanceof DeferredRequest))
      upstream.cooldown(url, undefined, 30 * 60_000);
    throw error;
  }
  // Partial GraphQL failures preserve successful repositories; absent nodes retry later.
  const nodes = byId
    ? (result.data.nodes ?? [])
    : targets.map((_, i) => result.data?.[`r${i}`]);
  return targets.map((_, i) => {
    const node = nodes[i] as Repository | undefined;
    if (!(node &&
      typeof node.id === "string" &&
      Number.isSafeInteger(node.stargazerCount) &&
      node.stargazerCount >= 0)) return null;
    if (!metadata) return { id: node.id, stargazerCount: node.stargazerCount };
    return /^[\w.-]+\/[\w.-]+$/.test(node.nameWithOwner) && node.repositoryTopics?.nodes ? node : null;
  });
}
