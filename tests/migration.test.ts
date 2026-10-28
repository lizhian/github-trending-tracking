import { expect, test } from "vitest";
import { Miniflare, convertV4MiniflareOptions } from "miniflare";
import { readFileSync, readdirSync } from "node:fs";

test("adding tracking types preserves existing cycles and history and isolates cascading deletion", async () => {
  const mf = new Miniflare(convertV4MiniflareOptions({
    name: "migration", modules: true, script: "export default {fetch(){return new Response()}}",
    d1Databases: ["DB"], compatibilityDate: "2026-10-01",
  }));
  try {
    const db = await mf.getD1Database("DB") as unknown as D1Database;
    const files = readdirSync("migrations").filter((file) => file.endsWith(".sql")).sort();
    const apply = async (file: string) => db.exec(readFileSync(`migrations/${file}`, "utf8")
      .replace(/--[^\n]*/g, "").replace(/\n/g, " "));
    for (const file of files.slice(0, 2)) await apply(file);
    await db.prepare(`INSERT INTO projects(id,cycle,name,topics,entered_at,listed_at,observed_at,attempted_at,stars,trends)
      VALUES('R_1','original','acme/one','[]',1,2,3,3,100,'{}')`).run();
    await db.prepare(`INSERT INTO snapshot_days VALUES('original',0,'{"0":[3,100]}')`).run();
    for (const file of files.slice(2)) await apply(file);
    expect(await db.prepare("SELECT id,type,repo_id,cycle,entered_at,listed_at,observed_at FROM projects").first())
      .toEqual({ id: "github:R_1", type: "github", repo_id: "R_1", cycle: "original", entered_at: 1, listed_at: 2, observed_at: 3 });
    expect(await db.prepare("SELECT hours FROM snapshot_days WHERE cycle='original'").first("hours"))
      .toBe('{"0":[3,100]}');
    await db.prepare(`INSERT INTO projects(id,type,repo_id,cycle,name,topics,entered_at,listed_at,observed_at,attempted_at,stars,trends)
      VALUES('linux.do:R_1','linux.do','R_1','linux','acme/one','[]',4,4,3,3,100,'{}')`).run();
    await db.prepare(`INSERT INTO snapshot_days VALUES('linux',0,'{"0":[3,100]}')`).run();
    await expect(db.prepare(`INSERT INTO projects(id,type,repo_id,cycle,name,topics,entered_at,listed_at,observed_at,attempted_at,stars,trends)
      VALUES('duplicate','linux.do','R_1','duplicate','acme/one','[]',4,4,3,3,100,'{}')`).run()).rejects.toThrow();
    await db.prepare("DELETE FROM projects WHERE id='github:R_1'").run();
    expect(await db.prepare("SELECT cycle FROM snapshot_days").all()).toMatchObject({ results: [{ cycle: "linux" }] });
  } finally {
    await mf.dispose();
  }
});
