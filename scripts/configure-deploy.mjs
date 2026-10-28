import { readFileSync, mkdirSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { parse } from "jsonc-parser";
const databaseId = process.env.CLOUDFLARE_D1_DATABASE_ID;
const accountId = process.env.CLOUDFLARE_ACCOUNT_ID;
const origin = process.env.ALLOWED_ORIGIN;
const zoneName = process.env.CLOUDFLARE_ZONE_NAME;
if (
  !databaseId ||
  !/^[\da-f]{8}(?:-[\da-f]{4}){3}-[\da-f]{12}$/i.test(databaseId)
)
  throw new Error("Set CLOUDFLARE_D1_DATABASE_ID to the D1 database UUID");
if (
  !origin ||
  new URL(origin).origin !== origin ||
  !origin.startsWith("https://")
)
  throw new Error(
    "Set ALLOWED_ORIGIN to the exact HTTPS Pages origin, without a trailing slash",
  );
const config = parse(readFileSync("wrangler.jsonc", "utf8"));
if (accountId) {
  if (!/^[\da-f]{32}$/i.test(accountId))
    throw new Error("Set CLOUDFLARE_ACCOUNT_ID to the account ID");
  config.account_id = accountId;
}
config.main = resolve("worker/index.ts");
config.d1_databases[0].database_id = databaseId;
config.d1_databases[0].migrations_dir = resolve("migrations");
config.vars.ALLOWED_ORIGIN = origin;
if (zoneName) {
  const hostname = new URL(origin).hostname;
  if (hostname !== zoneName && !hostname.endsWith(`.${zoneName}`))
    throw new Error("ALLOWED_ORIGIN must belong to CLOUDFLARE_ZONE_NAME");
  config.routes = [{ pattern: `${hostname}/api/*`, zone_name: zoneName }];
}
mkdirSync(".wrangler", { recursive: true });
writeFileSync(".wrangler/deploy.json", JSON.stringify(config, null, 2) + "\n");
console.log("Wrote .wrangler/deploy.json (contains no secrets)");
