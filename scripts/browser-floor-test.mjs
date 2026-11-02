import { chromium } from "playwright";
import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import assert from "node:assert/strict";
import validator from "gltf-validator";

const workspace = await fs.mkdtemp(path.join(os.tmpdir(),"progen-floor-ui-"));
const base = "http://127.0.0.1:18119";
const server = spawn(process.env.PROGEN_BIN || "/tmp/progen-mvp",["serve","--workspace",workspace,"--addr","127.0.0.1:18119"],{stdio:"pipe"});
let logs="",browser;
server.stdout.on("data",b=>logs+=b);server.stderr.on("data",b=>logs+=b);
const evidence={workspace,cases:[],errors:[]};
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
try {
 for(let i=0;i<100;i++){try{if((await fetch(base+"/api/recipes")).ok)break;}catch{}await sleep(100);}
 browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH,headless:true,args:["--no-sandbox","--use-angle=swiftshader","--enable-unsafe-swiftshader"]});
 const page=await browser.newPage({viewport:{width:1512,height:1080}});
 page.on("pageerror",e=>evidence.errors.push(e.message));
 await page.goto(base);
 await page.waitForFunction(()=>document.querySelectorAll("#preset option").length>=6&&!document.querySelector("#generate").disabled);
 await page.locator("#preset").selectOption("bridge_relief");
 await page.waitForFunction(()=>!!document.querySelector('input[data-node="floor"][data-param="depth"]')&&!document.querySelector("#generate").disabled);
 const presets=await(await fetch(base+"/api/recipes")).json();
 const r=presets.find(p=>p.id==="bridge_relief").recipe;
 const exact=JSON.parse(await fs.readFile(new URL("../testdata/bridge-quantized-collapse.json",import.meta.url)));
 for(const node of r.nodes){const n=exact.nodes.find(n=>n.id===node.id);if(n)node.params=n.params;}
 const compiled=await(await fetch(base+"/api/validate",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(r)})).json();
 for(const node of compiled.nodes)for(const [param,value] of Object.entries(node.params||{})){
  const input=page.locator(`#operatorParams input[data-node="${node.id}"][data-param="${param}"]`);
  if(await input.count())await input.fill(String(value));
 }
 await page.locator("#seed").fill(exact.seed);await page.locator("#count").fill("1");
 const baselinePayload=new Map();
 for(const resolution of [32,64])for(const {strength,depth} of [{strength:0,depth:.06},{strength:1,depth:0},{strength:1,depth:.06}]){
  await page.locator("#resolution").selectOption(String(resolution));
  await page.locator('input[data-node="floor"][data-param="strength"]').fill(String(strength));
  await page.locator('input[data-node="floor"][data-param="depth"]').fill(String(depth));
  const response=page.waitForResponse(r=>r.url()===base+"/api/jobs"&&r.request().method()==="POST");
  await page.locator("#generate").click();
  const job=await(await response).json();
  let done;
  for(let attempt=0;attempt<240;attempt++){
   done=await(await fetch(base+"/api/jobs/"+job.id)).json();
   assert.ok(!["failed","cancelled"].includes(done.status),JSON.stringify(done));
   if(done.status==="completed")break;
   await sleep(500);
  }
  assert.equal(done.status,"completed",JSON.stringify(done));
  assert.equal(done.candidates.length,1,JSON.stringify(done));
  const c=await(await fetch(base+"/api/candidates/"+done.candidates[0])).json();
  assert.ok(c.valid,JSON.stringify(c.problems));
  assert.equal(c.recipe.nodes.find(n=>n.id==="floor").params.strength,strength);
  await page.locator(`.card[data-id="${c.id}"]`).click();
  await page.waitForFunction(id=>document.querySelector("#selectedId").textContent.includes(id.slice(0,16))&&document.querySelector("#viewerHint").classList.contains("hidden"),c.id);
  await sleep(500);
  const prefix=path.join(workspace,`floor-${resolution}-${depth===0?"depth0":strength}`);
  await page.locator("#viewer").screenshot({path:prefix+".png"});
  const glb=new Uint8Array(await(await fetch(base+`/api/candidates/${c.id}/asset.glb`)).arrayBuffer());
  const jsonLength=new DataView(glb.buffer,glb.byteOffset).getUint32(12,true);
  const payload=Buffer.from(glb.slice(28+jsonLength));
  if(strength===0)baselinePayload.set(resolution,payload);
  if(depth===0)assert.deepEqual(payload,baselinePayload.get(resolution),"depth zero changed exported mesh");
  const result=await validator.validateBytes(glb);assert.equal(result.issues.numErrors,0,JSON.stringify(result.issues.messages));
  await fs.writeFile(prefix+".glb",glb);
  evidence.cases.push({resolution,strength,depth,id:c.id,glb:prefix+".glb",screenshot:prefix+".png",validator:{errors:result.issues.numErrors,warnings:result.issues.numWarnings}});
 }
 // Switch to the explicitly less-protected contract, then back to strict.
 for(const family of ["bridge_structural","bridge_relief"]){
  await page.locator("#preset").selectOption(family);
  const control=family==="bridge_structural"?"deck":"floor";
  await page.waitForFunction(node=>!!document.querySelector('input[data-node="'+node+'"]')&&!document.querySelector("#generate").disabled,control);
  const recipe=structuredClone(presets.find(p=>p.id===family).recipe);
  for(const node of recipe.nodes){const n=exact.nodes.find(n=>n.id===node.id);if(n)node.params=n.params;}
  const ready=await(await fetch(base+"/api/validate",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(recipe)})).json();
  for(const node of ready.nodes)for(const [key,value]of Object.entries(node.params||{})){
   const input=page.locator('#operatorParams input[data-node="'+node.id+'"][data-param="'+key+'"]');if(await input.count())await input.fill(String(value));
  }
  await page.locator("#seed").fill(exact.seed);await page.locator("#count").fill("1");
  for(const resolution of family==="bridge_structural"?[32,64]:[32]){
   await page.locator("#resolution").selectOption(String(resolution));
   const response=page.waitForResponse(r=>r.url()===base+"/api/jobs"&&r.request().method()==="POST");
   await page.locator("#generate").click();const job=await(await response).json();let done;
   for(let i=0;i<240;i++){done=await(await fetch(base+"/api/jobs/"+job.id)).json();assert.ok(!["failed","cancelled"].includes(done.status),JSON.stringify(done));if(done.status==="completed")break;await sleep(500);}
   assert.equal(done.status,"completed");assert.equal(done.candidates.length,1,JSON.stringify(done));
   const c=await(await fetch(base+"/api/candidates/"+done.candidates[0])).json();assert.ok(c.valid,JSON.stringify(c.problems));
   await page.locator('.card[data-id="'+c.id+'"]').click();
   await page.waitForFunction(id=>document.querySelector("#selectedId").textContent.includes(id.slice(0,16))&&document.querySelector("#viewerHint").classList.contains("hidden"),c.id);
   const status=await page.locator("#constraintStatus").innerText();
   if(family==="bridge_structural"){
    assert.equal(c.walking_checks.evaluated,true);assert.equal(c.walking_checks.passed,false);
    assert.match(status,/LESS PROTECTED/);assert.match(status,/not a game-ready/);assert.ok(c.walking_checks.problems.length>0);
   }else{assert.equal(c.walking_checks,undefined);assert.match(status,/Deck, endpoints, clearance/);assert.doesNotMatch(status,/LESS PROTECTED/);}
   const glb=new Uint8Array(await(await fetch(base+'/api/candidates/'+c.id+'/asset.glb')).arrayBuffer());
   const result=await validator.validateBytes(glb);assert.equal(result.issues.numErrors,0,JSON.stringify(result.issues.messages));
   const size=new DataView(glb.buffer,glb.byteOffset).getUint32(12,true);const doc=JSON.parse(new TextDecoder().decode(glb.slice(20,20+size)));
   if(family==="bridge_structural")assert.deepEqual(doc.extras.progen.walking_checks,c.walking_checks);
   const prefix=path.join(workspace,family+"-"+resolution);await fs.writeFile(prefix+".glb",glb);await page.locator("#viewer").screenshot({path:prefix+".png"});
   evidence.cases.push({family,resolution,id:c.id,walking:c.walking_checks,glb:prefix+".glb",screenshot:prefix+".png",validator:{errors:result.issues.numErrors,warnings:result.issues.numWarnings}});
  }
 }
 const cancelRecipe=structuredClone(presets.find(p=>p.id==="bridge_structural").recipe);cancelRecipe.domain.resolution=64;
 const job=await(await fetch(base+"/api/jobs",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({kind:"generate",recipe:cancelRecipe,count:16,workers:1,search_seed:"991"})})).json();
 assert.equal((await fetch(base+"/api/jobs/"+job.id,{method:"DELETE"})).status,202);
 let cancelled;for(let i=0;i<120;i++){cancelled=await(await fetch(base+"/api/jobs/"+job.id)).json();if(cancelled.status==="cancelled")break;await sleep(100);}
 assert.equal(cancelled.status,"cancelled",JSON.stringify(cancelled));evidence.cancelledJob=job.id;
 assert.deepEqual(evidence.errors,[]);
 console.log(JSON.stringify(evidence,null,2));
} catch(e){console.error(logs);throw e;} finally {
 await fs.writeFile(path.join(workspace,"evidence.json"),JSON.stringify(evidence,null,2));
 await browser?.close();server.kill("SIGTERM");
}
