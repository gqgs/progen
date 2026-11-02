import { chromium } from 'playwright';
import http from 'node:http';
import fs from 'node:fs/promises';
import path from 'node:path';
const folder=process.argv[2];if(!folder)throw Error('Usage: node scripts/render-floor-evidence.mjs <browser-floor workspace>');
const root=process.cwd();
const html=`<!doctype html><html><body style="margin:0;background:#222"><script type="importmap">{"imports":{"three":"/node_modules/three/build/three.module.js","three/addons/":"/node_modules/three/examples/jsm/"}}</script><script type="module">
import * as THREE from 'three';import {GLTFLoader} from 'three/addons/loaders/GLTFLoader.js';
const scene=new THREE.Scene();scene.background=new THREE.Color(0x202a29);
scene.add(new THREE.HemisphereLight(0xf0f5f0,0x273230,2));const light=new THREE.DirectionalLight(0xffffff,4);light.position.set(-2,5,-3);scene.add(light);
const camera=new THREE.PerspectiveCamera(48,1200/750,.01,100);camera.position.set(4.9,1.8,.1);camera.lookAt(-.8,0,0);
const renderer=new THREE.WebGLRenderer({antialias:true,preserveDrawingBuffer:true});renderer.setSize(1200,750);renderer.outputColorSpace=THREE.SRGBColorSpace;renderer.toneMapping=THREE.ACESFilmicToneMapping;document.body.append(renderer.domElement);
const asset=new URLSearchParams(location.search).get('asset');new GLTFLoader().load('/asset/'+asset,g=>{scene.add(g.scene);renderer.render(scene,camera);window.ready=true;});
</script></body></html>`;
const server=http.createServer(async(req,res)=>{try{const u=new URL(req.url,'http://localhost');if(u.pathname==='/'){res.setHeader('Content-Type','text/html');return res.end(html)}const p=u.pathname.startsWith('/asset/')?path.join(folder,path.basename(u.pathname)):path.join(root,u.pathname);res.setHeader('Content-Type',p.endsWith('.js')?'application/javascript':'application/octet-stream');res.end(await fs.readFile(p));}catch{res.writeHead(404);res.end()}}).listen(18120,'127.0.0.1');
let browser;try{browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH||'/usr/bin/chromium',headless:true,args:['--no-sandbox','--use-angle=swiftshader','--enable-unsafe-swiftshader']});const page=await browser.newPage({viewport:{width:1200,height:750}});for(const n of [32,64])for(const s of [0,1]){await page.goto('http://127.0.0.1:18120/?asset='+`floor-${n}-${s}.glb`);await page.waitForFunction(()=>window.ready);await page.screenshot({path:path.join(folder,`corridor-${n}-${s}.png`)});}}finally{await browser?.close();server.close()}
