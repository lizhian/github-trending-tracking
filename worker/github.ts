import { DeferredRequest, type Upstream } from "./upstream";

export interface Repository {
  id: string;
  nameWithOwner: string;
  description: string | null;
  primaryLanguage: { name: string } | null;
  repositoryTopics: { nodes: { topic: { name: string } }[] };
  stargazerCount: number;
}
const FIELDS =
  "id nameWithOwner description primaryLanguage { name } repositoryTopics(first:100) { nodes { topic { name } } } stargazerCount";

export async function repositories(
  token: string,
  targets: string[],
  byId: boolean,
  upstream: Upstream,
): Promise<(Repository | null)[]> {
  if (!targets.length) return [];
  const query = byId
    ? `query { nodes(ids:${JSON.stringify(targets)}) { ... on Repository { ${FIELDS} } } }`
    : `query { ${targets
        .map((name, i) => {
          const [owner, repo] = name.split("/");
          return `r${i}: repository(owner:${JSON.stringify(owner)},name:${JSON.stringify(repo)}) { ${FIELDS} }`;
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
    return node &&
      typeof node.id === "string" &&
      /^[\w.-]+\/[\w.-]+$/.test(node.nameWithOwner) &&
      Number.isSafeInteger(node.stargazerCount) &&
      node.stargazerCount >= 0 &&
      node.repositoryTopics?.nodes
      ? node
      : null;
  });
}
