import { chromium } from "playwright";
import { build } from "esbuild";
import { createServer } from "node:http";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";

const workspace = await fs.mkdtemp(path.join(os.tmpdir(), "progen-material-detail-"));
const evidenceDir = process.env.PROGEN_EVIDENCE_DIR || workspace;
await fs.mkdir(evidenceDir, { recursive: true });
const baselinePath = process.env.PROGEN_MATERIAL_BASELINE;
const binary = process.env.PROGEN_BIN || "/tmp/progen-mvp";
const { stdout } = await promisify(execFile)(binary, ["generate", "--recipe", "examples/bridge.json", "--count", "1", "--workspace", workspace], { maxBuffer: 8 << 20 });
const [candidate] = JSON.parse(stdout);
assert.ok(candidate.valid, JSON.stringify(candidate.problems));
const glb = await fs.readFile(path.join(workspace, "candidates", candidate.id, "asset.glb"));
const source = `
import * as THREE from "three";
import { GLTFLoader } from "three/addons/loaders/GLTFLoader.js";
import { previewMaterial, studioEnvironment } from "./web/material-preview.js";
${baselinePath ? 'import { previewMaterial as baselineMaterial } from "material-baseline";' : ''}
const factories = { after: previewMaterial ${baselinePath ? ', before: baselineMaterial' : ''} };
const renderer = new THREE.WebGLRenderer({antialias:true,preserveDrawingBuffer:true});
renderer.setSize(1000,640);renderer.setPixelRatio(1);
renderer.toneMapping=THREE.ACESFilmicToneMapping;renderer.outputColorSpace=THREE.SRGBColorSpace;
document.body.style.margin="0";document.body.append(renderer.domElement);
const scene=new THREE.Scene();scene.background=new THREE.Color(0x303840);
const environment=studioEnvironment(renderer);scene.environment=environment.texture;scene.environmentIntensity=.8;
scene.add(new THREE.HemisphereLight(0xeaf4ec,0x324834,.6));
const sun=new THREE.DirectionalLight(0xfff5df,2);scene.add(sun);
const model=(await new GLTFLoader().loadAsync('/bridge.glb')).scene;scene.add(model);
const probe=new THREE.Mesh(new THREE.PlaneGeometry(128,128),new THREE.MeshStandardMaterial());
probe.rotation.x=-Math.PI/2;scene.add(probe);
const perspective=new THREE.PerspectiveCamera(36,1000/640,.01,200);
const orthographic=new THREE.OrthographicCamera(-2,2,1.28,-1.28,.01,200);
orthographic.position.set(0,10,.001);orthographic.up.set(0,0,-1);orthographic.lookAt(0,0,0);
function material(preset,version,mode) {
 const m=preset==='neutral-mask' ? new THREE.MeshBasicMaterial({color:0xffffff,side:THREE.DoubleSide}) : factories[version](preset,9);
 if(mode!=='surface') {
  const original=m.onBeforeCompile, key=m.customProgramCacheKey.bind(m);
  m.onBeforeCompile=s=>{original(s);s.fragmentShader=s.fragmentShader.replace('#include <opaque_fragment>',
   '#include <opaque_fragment>\\n'+(mode==='normal' ? 'gl_FragColor=vec4(normal*.5+.5,1.0);' :
    mode==='roughness' ? 'gl_FragColor=vec4(vec3(.5+4.0*(roughnessFactor-roughness)),1.0);' : 'gl_FragColor=vec4(1.0);'));};
  m.customProgramCacheKey=()=>key()+'-detail-test-'+mode;
 }
 return m;
}
window.renderDetail=({preset,version='after',mode='surface',view='deck',span=4,probeOffsetX=0})=>{
 const probing=mode==='normal'||mode==='roughness';probe.visible=probing;model.visible=!probing;probe.position.x=probeOffsetX;
 const object=probing?probe:model;
 object.traverse(o=>{if(o.isMesh){o.material.dispose();o.material=material(preset,version,mode);}});
 let camera=perspective;
 sun.position.set(4,view==='grazing'?.45:5,-2);
 if(probing){orthographic.left=-span/2;orthographic.right=span/2;orthographic.top=span*.32;orthographic.bottom=-span*.32;orthographic.updateProjectionMatrix();camera=orthographic;}
 else {perspective.position.set(view==='orbit'?-2.6:1.8,4.3,view==='orbit'?1.5:2.3);perspective.lookAt(0,0,0);}
 renderer.render(scene,camera);
 const pixels=new Uint8Array(1000*640*4);renderer.getContext().readPixels(0,0,1000,640,renderer.getContext().RGBA,renderer.getContext().UNSIGNED_BYTE,pixels);
 const channels=[[],[],[]];
 for(let y=160;y<480;y++)for(let x=250;x<750;x++)for(let c=0;c<3;c++)channels[c].push(pixels[4*(x+1000*y)+c]);
 const std=channels.map(values=>{const mean=values.reduce((a,b)=>a+b,0)/values.length;return Math.sqrt(values.reduce((a,b)=>a+(b-mean)**2,0)/values.length);});
 const sample=[];for(let y=180;y<460;y+=10)for(let x=280;x<720;x+=10)for(let c=0;c<3;c++)sample.push(pixels[4*(x+1000*y)+c]);
 return {std,sample,png:renderer.domElement.toDataURL('image/png')};
};
window.ready=true;
`;
const bundled = await build({ stdin: { contents: source, resolveDir: process.cwd(), sourcefile: "material-detail-harness.js" }, bundle: true, format: "esm", write: false,
  plugins: baselinePath ? [{ name: "baseline", setup(plugin) {
    plugin.onResolve({filter:/^material-baseline$/},()=>({path:"baseline",namespace:"baseline"}));
    plugin.onLoad({filter:/.*/,namespace:"baseline"},async()=>({contents:await fs.readFile(baselinePath,"utf8"),resolveDir:process.cwd(),loader:"js"}));
  }}] : [] });
const server = createServer((request,response)=>{
 if(request.url==='/bridge.glb'){response.setHeader('Content-Type','model/gltf-binary');response.end(glb);}
 else if(request.url==='/main.js'){response.setHeader('Content-Type','text/javascript');response.end(bundled.outputFiles[0].contents);}
 else {response.setHeader('Content-Type','text/html');response.end('<!doctype html><script type="module" src="/main.js"></script>');}
});
await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
let browser;
const report={candidate:candidate.id,sourceGLBSHA256:createHash('sha256').update(glb).digest('hex'),checks:[],metrics:{},screenshots:{},errors:[]};
try {
 browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH,headless:true,args:['--no-sandbox','--use-angle=swiftshader','--enable-unsafe-swiftshader']});
 const page=await browser.newPage({viewport:{width:1000,height:640}});
 page.on('pageerror',e=>report.errors.push(e.message));page.on('console',m=>{if(m.type()==='error')report.errors.push(m.text());});
 await page.goto(`http://127.0.0.1:${server.address().port}`);await page.waitForFunction(()=>window.ready);
 async function render(options,name){
  const result=await page.evaluate(options=>window.renderDetail(options),options);
  const png=Buffer.from(result.png.split(',')[1],'base64');const hash=createHash('sha256').update(png).digest('hex');
  if(name){await fs.writeFile(path.join(evidenceDir,name+'.png'),png);report.screenshots[name]=hash;}
  return {...result,hash};
 }
 for(const preset of ['clay','stone','wood','metal','glass']) {
  const normal=await render({preset,mode:'normal'});const roughness=await render({preset,mode:'roughness'});
  report.metrics[preset]={normalStd:normal.std,roughnessStd:roughness.std};
  const normalVariation=Math.max(...normal.std),roughnessVariation=Math.max(...roughness.std);
  if(['clay','stone','wood'].includes(preset)){assert.ok(normalVariation>.4,preset+' lacks lighting-normal detail');assert.ok(roughnessVariation>.4,preset+' lacks spatial roughness');}
  else {assert.ok(normalVariation<.05,preset+' unexpectedly acquired bump');assert.ok(roughnessVariation<.05,preset+' unexpectedly acquired roughness noise');}
  if(['clay','stone','wood'].includes(preset)){
   const neutral=await render({preset:'neutral-mask',mode:'mask'});const detailed=await render({preset,mode:'mask'});
   assert.equal(detailed.hash,neutral.hash,preset+' changed canonical silhouette coverage');
  }
  const first=await render({preset},'after-'+preset+'-deck');
  await render({preset:'metal'});const repeat=await render({preset});assert.equal(first.hash,repeat.hash,preset+' is not deterministic');
  if(baselinePath){const before=await render({preset,version:'before'},'before-'+preset+'-deck');
   if(['clay','stone','wood'].includes(preset))assert.notEqual(first.hash,before.hash,preset+' appearance unchanged');
   else assert.equal(first.hash,before.hash,preset+' character changed');
   const beforeMask=await render({preset,version:'before',mode:'mask'});const afterMask=await render({preset,mode:'mask'});assert.equal(beforeMask.hash,afterMask.hash,'surface detail changed silhouette');
  }
 }
 for(const preset of ['stone','wood']){
  const near=await render({preset,mode:'normal',span:4});const far=await render({preset,mode:'normal',span:240});
  assert.ok(Math.max(...far.std)<Math.max(...near.std)*.3,preset+' undersampled detail does not fade');
  const translated=await render({preset,mode:'normal',probeOffsetX:.5});
  const alignmentError=near.sample.reduce((sum,value,i)=>sum+Math.abs(value-translated.sample[i]),0)/near.sample.length;
  assert.ok(alignmentError<.1,preset+' detail moved with mesh-local coordinates');
  report.metrics[preset].minifiedNormalStd=far.std;report.metrics[preset].canonicalAlignmentError=alignmentError;
  await render({preset,view:'grazing'},'after-'+preset+'-grazing');await render({preset,view:'orbit'},'after-'+preset+'-orbit');
  if(baselinePath)await render({preset,view:'grazing',version:'before'},'before-'+preset+'-grazing');
 }
 report.checks.push('real GPU normal and roughness variation on flat canonical deck probe; clay/stone/wood detail, metal/glass unchanged','deterministic material-switch round trips; identical silhouette coverage; fine-normal attenuation under minification','generated bridge same-camera deck and grazing-light previews; orbit view; no browser/GLSL errors');
 assert.deepEqual(report.errors,[]);
 console.log(JSON.stringify({workspace,evidenceDir,...report},null,2));
} finally {await fs.writeFile(path.join(evidenceDir,'detail-results.json'),JSON.stringify(report,null,2));await browser?.close();await new Promise(resolve=>server.close(resolve));}
