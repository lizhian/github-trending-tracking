import { readFileSync, mkdirSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
const databaseId = process.env.CLOUDFLARE_D1_DATABASE_ID;
const origin = process.env.ALLOWED_ORIGIN;
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
const config = JSON.parse(readFileSync("wrangler.jsonc", "utf8"));
config.main = resolve("worker/index.ts");
config.d1_databases[0].database_id = databaseId;
config.d1_databases[0].migrations_dir = resolve("migrations");
config.vars.ALLOWED_ORIGIN = origin;
mkdirSync(".wrangler", { recursive: true });
writeFileSync(".wrangler/deploy.json", JSON.stringify(config, null, 2) + "\n");
console.log("Wrote .wrangler/deploy.json (contains no secrets)");
