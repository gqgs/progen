import { chromium } from "playwright";
import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";
import os from "node:os";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";

const workspace = await fs.mkdtemp(path.join(os.tmpdir(), "progen-material-ui-"));
const evidenceDir = process.env.PROGEN_EVIDENCE_DIR || workspace;
await fs.mkdir(evidenceDir, { recursive: true });
const port = process.env.PROGEN_MATERIAL_TEST_PORT || "18119";
const base = `http://127.0.0.1:${port}`;
const server = spawn(process.env.PROGEN_BIN || "/tmp/progen-mvp", ["serve", "--workspace", workspace, "--addr", `127.0.0.1:${port}`]);
let logs = "", browser, serverError;
server.stdout.on("data", b => logs += b);
server.stderr.on("data", b => logs += b);
server.on("error", error => serverError = error);
const digest = b => createHash("sha256").update(b).digest("hex");
const errors = [], writes = [];
const report = { checks: [], screenshots: {}, errors };
async function bytes(route) {
  const response = await fetch(base + route);
  assert.ok(response.ok, route);
  return Buffer.from(await response.arrayBuffer());
}
try {
  let ready = false;
  for (let i = 0; i < 100; i++) {
    if (serverError) throw serverError;
    if (server.exitCode !== null) throw Error(logs);
    try { if ((await fetch(base + "/api/recipes")).ok) { ready = true; break; } } catch {}
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  assert.ok(ready, logs);
  browser = await chromium.launch({ executablePath: process.env.CHROMIUM_PATH, headless: true, args: ["--no-sandbox", "--use-angle=swiftshader", "--enable-unsafe-swiftshader"] });
  const page = await browser.newPage({ viewport: { width: 1600, height: 1100 }, deviceScaleFactor: 1 });
  page.on("pageerror", e => errors.push(e.message));
  page.on("console", m => { if (m.type() === "error") errors.push(m.text()); });
  page.on("request", r => { if (!["GET", "HEAD"].includes(r.method())) writes.push(r.url()); });
  const rendered = () => page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  const meshReady = async () => {
    await page.waitForFunction(() => document.querySelector("#viewerHint").classList.contains("hidden"));
    await rendered();
  };
  async function capture(name) {
    await rendered();
    const screenshot = await page.locator("#viewer").screenshot({ path: path.join(evidenceDir, name + ".png") });
    report.screenshots[name] = digest(screenshot);
    return digest(screenshot);
  }
  async function generate(file) {
    await page.locator("#recipeFile").setInputFiles(new URL(file, import.meta.url).pathname);
    await page.waitForFunction(() => !document.querySelector("#generate").disabled && document.querySelector("#preset").value === "custom");
    await page.locator("#resolution").selectOption("32");
    await page.locator("#seed").fill("42");
    await page.locator("#count").fill("1");
    await page.locator("#generate").click();
    await page.waitForFunction(() => document.querySelector("#jobState").textContent === "COMPLETED", {}, { timeout: 120000 });
    await meshReady();
    await page.waitForFunction(() => document.querySelector(".card.selected"));
    return page.locator(".card.selected").getAttribute("data-id");
  }
  await page.goto(base);
  await page.waitForFunction(() => document.querySelectorAll("#preset option").length >= 6 && !document.querySelector("#generate").disabled);
  const bridge = await generate("../examples/bridge.json");
  await page.waitForResponse(r => r.url().endsWith(`/candidates/${bridge}/artifacts/reference`), { timeout: 3000 }).catch(() => {});
  const paths = [`/api/candidates/${bridge}`, `/api/candidates/${bridge}/asset.glb`, `/api/candidates/${bridge}/export.zip`];
  const before = await Promise.all(paths.map(bytes));
  const provenance = await page.locator("#provenance").textContent();
  const constraints = await page.locator("#constraintStatus").textContent();
  const writesBefore = writes.length;
  const hashes = [];
  for (const preset of ["clay", "stone", "wood", "metal", "glass"]) {
    await page.locator("#materialPreset").selectOption(preset);
    assert.equal(await page.locator("#glassHelp").isVisible(), preset === "glass");
    hashes.push(await capture("bridge-" + preset));
    for (const mode of ["reference", "overlay", "result"]) {
      await page.locator("#comparison").selectOption(mode);
      await rendered();
      if (preset === "wood" && mode !== "result") await capture("wood-" + mode);
    }
  }
  assert.equal(new Set(hashes).size, 5, "presets did not visibly differ");
  await page.locator("#materialPreset").selectOption("wood");
  const woodFirst = await capture("wood-repeat");
  assert.equal(woodFirst, hashes[2], "preset round trip changed deterministic rendering/camera");
  await page.locator("#previewBackground").selectOption("#d6d9dc");
  assert.notEqual(await capture("wood-light"), woodFirst);
  await page.locator("#materialPreset").selectOption("glass");
  await capture("glass-light");
  await page.locator("#previewBackground").selectOption("#101418");
  await capture("glass-dark");
  assert.equal(writes.length, writesBefore, "preview controls wrote to API");
  assert.equal(await page.locator("#provenance").textContent(), provenance);
  assert.equal(await page.locator("#constraintStatus").textContent(), constraints);
  const after = await Promise.all(paths.map(bytes));
  before.forEach((b, i) => assert.equal(digest(b), digest(after[i]), paths[i] + " changed"));
  report.checks.push("five visibly distinct presets; reference/result/overlay under every material; deterministic round trip; no API writes; byte-identical candidate JSON, GLB and export ZIP; unchanged validation/provenance");
  // Camera remains interactive and is not reset by material changes.
  await page.locator("#materialPreset").selectOption("wood");
  const rect = await page.locator("#viewer canvas").boundingBox();
  const originalView = await capture("before-orbit");
  await page.mouse.move(rect.x+rect.width/2, rect.y+rect.height/2);
  await page.mouse.down(); await page.mouse.move(rect.x+rect.width/2+70,rect.y+rect.height/2+25,{steps:12}); await page.mouse.up();
  await page.waitForTimeout(700);
  assert.notEqual(await capture("after-orbit"), originalView);
  await page.mouse.wheel(0, -100); await page.waitForTimeout(700);
  await page.screenshot({ path: path.join(evidenceDir, "studio.png"), fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth+1), "mobile overflow");
  await page.locator("#materialPreset").selectOption("metal");
  await page.setViewportSize({ width: 1600, height: 1100 });
  await page.reload(); await meshReady();
  assert.equal(await page.locator("#materialPreset").inputValue(), "clay", "reload must restore documented default");
  const organic = await generate("../examples/organic.json");
  assert.notEqual(organic, bridge);
  assert.ok(await page.locator("#comparison").isDisabled());
  for (const preset of ["clay", "stone", "wood", "metal", "glass"]) {
    await page.locator("#materialPreset").selectOption(preset); await rendered();
  }
  await page.locator("#materialPreset").selectOption("wood");
  await capture("organic-wood");
  report.checks.push("orbit/zoom, mobile layout, reload default, all materials on legacy branching geometry");
  assert.deepEqual(errors, [], "browser/GLSL errors");
  console.log(JSON.stringify({ workspace, evidenceDir, ...report }, null, 2));
} finally {
  await fs.writeFile(path.join(evidenceDir, "results.json"), JSON.stringify(report, null, 2));
  await browser?.close(); server.kill();
}
