import { XMLParser, XMLValidator } from "fast-xml-parser";
export const RSS_URL =
  "https://rsshub-balancer.virworks.moe/github/trending/daily/any";
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

async function boundedResponse(response: Response): Promise<string> {
  if (!response.body) throw new Error("Upstream empty response");
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let bytes = 0;
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    bytes += value.length;
    if (bytes > 2_000_000) {
      await reader.cancel();
      throw new Error("Upstream response too large");
    }
    chunks.push(value);
  }
  const decoder = new TextDecoder();
  return (
    chunks.map((value) => decoder.decode(value, { stream: true })).join("") +
    decoder.decode()
  );
}
export async function discover(): Promise<string[]> {
  const response = await fetch(RSS_URL, {
    signal: AbortSignal.timeout(15_000),
  });
  if (!response.ok) throw new Error(`RSS HTTP ${response.status}`);
  const xml = await boundedResponse(response);
  if (XMLValidator.validate(xml) !== true) throw new Error("RSS invalid XML");
  const parsed = new XMLParser({
    ignoreAttributes: false,
    processEntities: false,
  }).parse(xml);
  if (!parsed.rss?.channel) throw new Error("RSS missing channel");
  const items = parsed.rss.channel.item ?? [];
  const names = new Set<string>();
  for (const item of Array.isArray(items) ? items : [items]) {
    const text = [
      item.link,
      item.guid,
      item.description,
      item["content:encoded"],
    ]
      .filter((v) => typeof v === "string")
      .join(" ");
    for (const match of text.matchAll(
      /https:\/\/github\.com\/([\w.-]+)\/([\w.-]+)(?=[\s<"'?/#&]|$)/g,
    )) {
      if (
        ![
          "trending",
          "topics",
          "collections",
          "sponsors",
          "features",
          "marketplace",
        ].includes(match[1])
      )
        names.add(`${match[1]}/${match[2]}`.replace(/\.git$/, ""));
    }
  }
  return [...names];
}
export async function repositories(
  token: string,
  targets: string[],
  byId: boolean,
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
  const response = await fetch("https://api.github.com/graphql", {
    method: "POST",
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
      "User-Agent": "github-trending-tracking",
    },
    body: JSON.stringify({ query }),
    signal: AbortSignal.timeout(20_000),
  });
  if (!response.ok) throw new Error(`GitHub HTTP ${response.status}`);
  const result = JSON.parse(await boundedResponse(response)) as {
    data?: { nodes?: unknown[]; [key: string]: unknown };
    errors?: { type?: string }[];
  };
  if (!result.data) throw new Error("GitHub GraphQL unavailable");
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
