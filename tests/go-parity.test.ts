import { expect, test, vi } from "vitest";
import { Miniflare, convertV4MiniflareOptions } from "miniflare";
import { readFileSync, readdirSync, mkdtempSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { execFileSync } from "node:child_process";
import { query } from "../worker/api";
import { saveObservations, setState, type ProjectRow } from "../worker/store";
import { HOUR, DAY } from "../worker/schedule";
import type { Env } from "../worker/index";

test("Go HTTP matches the Worker on the same complete frozen export and time", async () => {
 const directory = mkdtempSync(join(tmpdir(), "gtt-parity-"));
 const mf = new Miniflare(convertV4MiniflareOptions({name:"parity",modules:true,script:"export default {fetch(){return new Response()}}",d1Databases:["DB"],compatibilityDate:"2026-10-01"}));
 const now = Date.parse("2026-10-02T05:04:00Z");
 let success = false;
 try {
  const db = await mf.getD1Database("DB") as unknown as D1Database;
  let dump = "";
  const migrations = readdirSync("migrations").filter(file=>file.endsWith(".sql")).sort();
  for (const name of migrations){const sql=readFileSync(`migrations/${name}`,"utf8");await db.exec(sql.replace(/--[^\n]*/g, "").replace(/\n/g," "));dump += sql+"\n";}
  const names=["acme/alpha","acme/Alpha","acme/ápple","acme/_tool","acme/a-tool","acme/a.tool","acme/a2","acme/a10",...Array.from({length:97},(_,i)=>`acme/item${i}`)];
  const repo=(i:number,stars:number)=>({id:`R_${i}`,nameWithOwner:names[i],description:i%2?null:"Useful tools",primaryLanguage:i%3?{name:"TypeScript"}:null,repositoryTopics:{nodes:i%4?[{topic:{name:"tools"}}]:[]},stargazerCount:stars});
  for(const [time,change] of [[now-3*DAY,0],[now-25*HOUR,20],[now,10]] as const){
   const rows=(await db.prepare("SELECT * FROM projects").all<ProjectRow>()).results;
   await saveObservations(db,names.map((_,i)=>({repo:repo(i,100+i+change),previous:rows.find(row=>row.repo_id===`R_${i}`),listedAt:time})),time);
  }
  await saveObservations(db,[{repo:repo(0,110),type:"linux.do",listedAt:now}],now);
  const policy={version:2 as const,effectiveAt:now-2*DAY};
  await setState(db,"sampling_policy",policy).run();
  await setState(db,"pending",[{name:"later/repo",listedAt:now,nextAt:now+HOUR}]).run();
  await setState(db,"upstream",{"api.github.com":{blockedUntil:now+3*HOUR}}).run();
  await setState(db,"collection",{attemptedAt:now,succeededAt:now,succeeded:105,failed:0,error:null}).run();
  await setState(db,"discovery:linux.do",{attemptedAt:now,succeededAt:null,succeeded:0,failed:1,error:"发现暂时失败"}).run();
  // A source's stored history older than the visible horizon must survive import.
  const cycle=(await db.prepare("SELECT cycle FROM projects WHERE id='github:R_0'").first<{cycle:string}>())!.cycle;
  const old=now-40*DAY;
  await db.prepare("INSERT INTO snapshot_days VALUES(?,?,?)").bind(cycle,Math.floor(old/DAY),JSON.stringify({[Math.floor(old/HOUR)]:[old,80]})).run();
  const blankRepo={...repo(104,204),id:"R_new",nameWithOwner:"acme/new"};
  await saveObservations(db,[{repo:blankRepo,listedAt:now}],now);
  dump += "CREATE TABLE d1_migrations(id INTEGER PRIMARY KEY,name TEXT,applied_at TEXT);\n";
  migrations.forEach((name,i)=>dump+=`INSERT INTO d1_migrations VALUES(${i+1},'${name}','2026-10-01');\n`);
  const sqlValue=(value:unknown)=>value===null?"NULL":typeof value==="number"?String(value):`'${String(value).replaceAll("'","''")}'`;
  for(const table of ["projects","snapshot_days","system_state"]){
   for(const row of (await db.prepare(`SELECT * FROM ${table}`).all<Record<string,unknown>>()).results){dump+=`INSERT INTO ${table}(${Object.keys(row).join(",")}) VALUES(${Object.values(row).map(sqlValue).join(",")});\n`;}
  }
  writeFileSync(join(directory,"source.sql"),dump,{mode:0o600});
  vi.spyOn(Date,"now").mockReturnValue(now);
  const env={DB:db,ALLOWED_ORIGIN:"https://site.test"} as Env;
  const paths=["/api/projects","/api/projects?type=linux.do","/api/projects?sort=name&direction=asc","/api/projects?sort=name&direction=desc","/api/projects?sort=stars&direction=asc&page=2","/api/projects?sort=24&direction=asc","/api/projects?page=999999","/api/projects?q=useful&language=TypeScript&topic=tools","/api/projects?q=no-match","/api/projects?q=a&q=b","/api/projects?type=bad","/api/projects?unknown=1","/api/projects?q=a;b","/api/projects?q=%",...[24,72,168,720].map(window=>`/api/projects/github%3AR_0/history?window=${window}`),"/api/projects/linux.do%3AR_0/history","/api/projects/github%3AR_new/history","/api/projects/missing/history","/api/projects/%FF/history","/api/projects/missing/history?window=12","/api/projects/github%3AR_0/history?window=0x18"];
  const cases=[];
  for(const path of paths){const response=await query(new Request(`https://site.test${path}`),env);cases.push({path,method:"GET",origin:"",status:response.status,body:await response.json()});}
  for(const [method,origin] of [["POST",""],["OPTIONS","https://site.test"],["GET","https://evil.test"]]){const response=await query(new Request("https://site.test/api/projects",{method,headers:origin?{Origin:origin}:{}}),env);cases.push({path:"/api/projects",method,origin,status:response.status,body:response.status===204?null:await response.json()});}
  writeFileSync(join(directory,"cases.json"),JSON.stringify({now,cases}),{mode:0o600});
  const result=execFileSync("go",["test","./internal/tracking","-run","^TestWorkerParity$","-count=1"],{env:{...process.env,GTT_PARITY_DIR:directory},encoding:"utf8",timeout:120000});
  expect(result).toContain("ok");success=true;
 } finally {vi.restoreAllMocks();await mf.dispose();if(success && !process.env.GTT_KEEP_PARITY)rmSync(directory,{recursive:true,force:true});else console.info(`Parity fixture retained at ${directory}`);}
},180000);
