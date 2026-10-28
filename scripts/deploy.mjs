import { existsSync, readFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { setTimeout } from "node:timers/promises";

const root = fileURLToPath(new URL("../", import.meta.url));
process.chdir(root);
const [target = "all", ...flags] = process.argv.slice(2);
if (target === "--help" || flags.includes("--help")) {
  console.log(
    "Usage: node scripts/deploy.mjs [pages|worker|all|verify] [--dry-run]",
  );
  console.log(
    "--dry-run: local checks/builds only; no Cloudflare reads or writes.",
  );
  process.exit(0);
}
if (
  !["pages", "worker", "all", "verify"].includes(target) ||
  flags.some((f) => f !== "--dry-run")
)
  throw new Error(
    "Use pages, worker, all or verify, optionally with --dry-run",
  );
const dryRun = flags.includes("--dry-run");
const publishWorker = target === "worker" || target === "all";
const publishPages = target === "pages" || target === "all";
const wrangler = fileURLToPath(
  new URL("../node_modules/wrangler/bin/wrangler.js", import.meta.url),
);
const configPath = ".wrangler/deploy.json";

function run(command, args, capture = false) {
  console.log(
    `> ${command === process.execPath ? "node" : command} ${args.join(" ")}`,
  );
  const result = spawnSync(command, args, {
    cwd: root,
    env: process.env,
    encoding: "utf8",
    stdio: capture ? ["ignore", "pipe", "inherit"] : "inherit",
  });
  if (result.error) throw result.error;
  if (result.status !== 0)
    throw new Error(`Command failed (${result.status}): ${args.join(" ")}`);
  return result.stdout;
}
const cf = (args, capture = false) =>
  run(process.execPath, [wrangler, ...args], capture);

async function verify(origin, expectedAssets = []) {
  let failure;
  for (let attempt = 0; attempt < 3; attempt++) {
    try {
      const get = async (path) => {
        const response = await fetch(new URL(path, origin), {
          cache: "no-store",
          signal: AbortSignal.timeout(10_000),
        });
        if (!response.ok) throw new Error(`${path}: HTTP ${response.status}`);
        return response;
      };
      const html = await (await get("/")).text();
      if (
        !html.includes("<title>GitHub 趋势追踪</title>") ||
        expectedAssets.some((asset) => !html.includes(asset))
      )
        throw new Error(
          "The site is not serving the expected frontend build yet",
        );
      const list = await (await get("/api/projects")).json();
      if (
        !Array.isArray(list.items) ||
        !Number.isInteger(list.total) ||
        !list.summary ||
        !list.status
      )
        throw new Error("List API response does not match the query contract");
      if (list.items.length) {
        const history = await (
          await get(
            `/api/projects/${encodeURIComponent(list.items[0].id)}/history?window=24`,
          )
        ).json();
        if (!Array.isArray(history.points) || !history.trend)
          throw new Error(
            "History API response does not match the query contract",
          );
      }
      console.log(
        `Verified ${origin}: frontend + same-origin API, ${list.total} projects.`,
      );
      return;
    } catch (error) {
      failure = error;
      if (attempt < 2) await setTimeout(3000);
    }
  }
  throw new Error(
    `Verification failed: ${failure?.message}. Retry npm run deploy:verify; inspect the browser if local networking fails. Publishing may already have succeeded.`,
  );
}

async function main() {
  if (existsSync(".env.deploy.local")) process.loadEnvFile(".env.deploy.local");
  const required = [
    "CLOUDFLARE_ACCOUNT_ID",
    "CLOUDFLARE_D1_DATABASE_ID",
    "ALLOWED_ORIGIN",
    "CLOUDFLARE_ZONE_NAME",
    "CLOUDFLARE_PAGES_PROJECT_NAME",
    "CLOUDFLARE_PAGES_BRANCH",
  ];
  for (const name of required)
    if (!process.env[name])
      throw new Error(
        `Missing ${name}. Copy .env.deploy.example to .env.deploy.local and fill the existing production resource IDs.`,
      );
  run(process.execPath, ["scripts/configure-deploy.mjs"]);
  const config = JSON.parse(readFileSync(configPath, "utf8"));
  const origin = config.vars.ALLOWED_ORIGIN;
  const projectName = process.env.CLOUDFLARE_PAGES_PROJECT_NAME;
  console.log(
    `${dryRun ? "Dry run" : target}: ${origin}; Worker=${config.name}; Pages=${projectName}`,
  );
  if (target === "verify") {
    if (!dryRun) await verify(origin);
    return;
  }
  if (!dryRun) {
    let auth;
    try {
      auth = JSON.parse(cf(["whoami", "--json"], true));
    } catch (error) {
      throw new Error(`${error.message}. Run npx wrangler login, then retry.`);
    }
    if (
      !auth.loggedIn ||
      !auth.accounts?.some((account) => account.id === config.account_id)
    )
      throw new Error(
        "Wrangler must be logged in to the configured production account",
      );
    const scopes = [
      "account:read",
      "user:read",
      ...(publishPages ? ["pages:write"] : []),
      ...(publishWorker
        ? [
            "workers:write",
            "workers_scripts:write",
            "workers_routes:write",
            "zone:read",
            "d1:write",
          ]
        : []),
    ];
    if (auth.authType?.toLowerCase().includes("oauth")) {
      const missing = scopes.filter(
        (scope) => !auth.tokenPermissions?.includes(scope),
      );
      if (missing.length) {
        const loginScopes = [
          ...new Set([
            ...auth.tokenPermissions.filter(
              (scope) => scope !== "offline_access",
            ),
            ...scopes,
          ]),
        ];
        throw new Error(
          `Missing OAuth scopes: ${missing.join(", ")}. Run npx wrangler login --scopes ${loginScopes.join(" ")}`,
        );
      }
    }
    if (publishPages) {
      const projects = JSON.parse(
        cf(["pages", "project", "list", "--json"], true),
      );
      // Wrangler JSON uses the same column names as its table output.
      if (!projects.some((project) => project["Project Name"] === projectName))
        throw new Error(
          `Existing Pages project ${projectName} was not found. Follow the initial setup in README before incremental deployment.`,
        );
    }
  }
  run("npm", ["run", "typecheck"]);
  if (publishWorker) run("npm", ["test"]);
  // Finish local builds before the first production mutation.
  if (publishPages) run("npm", ["run", "build"]);
  if (publishWorker)
    cf([
      "deploy",
      "--config",
      configPath,
      "--minify",
      "--dry-run",
      "--outdir",
      ".wrangler/build",
    ]);
  if (dryRun) {
    console.log(
      "Local checks passed. Skipped production migrations, publishing and verification.",
    );
    return;
  }
  if (publishWorker) {
    cf(["d1", "migrations", "apply", "DB", "--remote", "--config", configPath]);
    cf(["deploy", "--config", configPath, "--minify"]);
  }
  let assets = [];
  if (publishPages) {
    const dirty = run("git", ["status", "--porcelain"], true).trim().length > 0;
    cf([
      "pages",
      "deploy",
      "dist",
      "--project-name",
      projectName,
      "--branch",
      process.env.CLOUDFLARE_PAGES_BRANCH,
      `--commit-dirty=${dirty}`,
    ]);
    assets = [
      ...readFileSync("dist/index.html", "utf8").matchAll(
        /(?:src|href)="(\/assets\/[^\"]+)"/g,
      ),
    ].map((match) => match[1]);
  }
  await verify(origin, assets);
}

// The production site uses /api on the Pages origin, regardless of shell or .env overrides.
process.env.VITE_API_URL = "";
main().catch((error) => {
  console.error(error.message);
  process.exitCode = 1;
});
