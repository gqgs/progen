import { chromium } from "playwright";
import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import assert from "node:assert/strict";

const workspace = await fs.mkdtemp(path.join(os.tmpdir(), "progen-implicit-ui-"));
const port = "18117";
const base = "http://127.0.0.1:" + port;
const executable = process.env.PROGEN_BIN || "/tmp/progen-mvp";
const server = spawn(executable, ["serve","--workspace",workspace,"--addr","127.0.0.1:"+port],{stdio:["ignore","pipe","pipe"]});
let logs = "";
server.stdout.on("data",b=>logs+=b.toString());
server.stderr.on("data",b=>logs+=b.toString());
let browser;
try {
 let started = false;
 for(let i=0;i<100;i++){
  if(server.exitCode!==null)throw new Error(logs);
  try {const r=await fetch(base+"/api/recipes");if(r.ok){started=true;break;}}catch{}
  await new Promise(r=>setTimeout(r,100));
 }
 if(!started)throw new Error("server did not start: "+logs);
 browser = await chromium.launch({headless:true,args:["--no-sandbox","--use-angle=swiftshader","--enable-unsafe-swiftshader"]});
 const page = await browser.newPage({viewport:{width:1440,height:1000}});
 const errors=[];
 page.on("pageerror",e=>errors.push(e.message));
 page.on("console",m=>{if(m.type()==="error"&&!m.text().includes("favicon"))errors.push(m.text())});
 await page.goto(base);
 await page.waitForFunction(()=>document.querySelectorAll("#preset option").length>=5);
 for (const [family, node] of [["tpms","implicit"],["voronoi","implicit"],["hybrid","combine"]]) {
  await page.locator("#preset").selectOption(family);
  await page.waitForFunction(n=>document.querySelector('#operatorParams input[data-node="'+n+'"]')!==null,node);
  await page.locator("#resolution").selectOption("32");
  await page.locator("#count").fill("1");
  await page.locator("#generate").click();
  await page.waitForFunction(()=>document.querySelector("#jobState").textContent==="COMPLETED",{}, {timeout:120000});
  await page.waitForFunction(()=>document.querySelectorAll(".card").length===1 && document.querySelector("#viewerHint").classList.contains("hidden") && document.querySelector("#graphToggle").disabled,{},{timeout:30000});
  await page.waitForFunction(()=>document.querySelector("#archiveX").textContent.includes("occupied-volume"),{}, {timeout:30000});
  const metric=await page.locator("#metrics").innerText();
  assert.match(metric,/SOLID VOLUME/);
  assert.match(metric,/EXPOSED FACES/);
  const candidate=await (await fetch(base+"/api/candidates")).json();
  assert.equal(candidate.length,1,family+" must have one generation");
  assert.ok(candidate[0].valid,family+" invalid: "+JSON.stringify(candidate[0].problems));
  assert.ok(!candidate[0].artifacts.graph,"implicit generator emitted graph");
  assert.ok(candidate[0].metrics.solid_fraction>0,"missing occupancy metric");
  assert.ok(candidate[0].metrics.surface_fraction>0,"missing face metric");
  const glb=await fetch(base+"/api/candidates/"+candidate[0].id+"/asset.glb");
  assert.equal(glb.status,200);
  assert.ok((await glb.arrayBuffer()).byteLength>100);
  assert.ok(await page.locator("#error").evaluate(e=>e.classList.contains("hidden")),family+" showed an error");
 }
 assert.deepEqual(errors,[],"browser console errors");
 console.log("Implicit geometry browser smoke tests passed for TPMS, Voronoi and hybrid.");
} catch(error) {
 console.error(logs);
 throw error;
} finally {
 await browser?.close();
 if(server.exitCode===null){
  const done=new Promise(resolve=>server.once("exit",resolve));
  server.kill("SIGTERM");
  await done;
 }
}
