import { chromium } from "playwright";
import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";
import os from "node:os";
import { createHash } from "node:crypto";
import assert from "node:assert/strict";
import validator from "gltf-validator";

const workspace = await fs.mkdtemp(path.join(os.tmpdir(), "progen-bridge-ui-"));
const binary = process.env.PROGEN_BIN || "/tmp/progen-mvp";
const port = process.env.PROGEN_BRIDGE_TEST_PORT || "18118";
const base = `http://127.0.0.1:${port}`;
const fixture = await fs.readFile(new URL("../examples/bridge.glb", import.meta.url));
const sidecar = await fs.readFile(new URL("../examples/bridge.constraints.json", import.meta.url));
const digest = bytes => createHash("sha256").update(bytes).digest("hex");
const screenshots = {};
const evidence = { workspace, screenshots, batches: [], checks: [], browserErrors: [] };
const server = spawn(binary, ["serve", "--workspace", workspace, "--addr", `127.0.0.1:${port}`], { stdio: ["ignore", "pipe", "pipe"] });
let logs = "", serverError, browser;
server.stdout.on("data", b => logs += b);
server.stderr.on("data", b => logs += b);
server.once("error", error => serverError = error);
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));

async function api(route, options) {
  const response = await fetch(base + "/api/" + route, options);
  assert.ok(response.ok, `${route}: ${response.status} ${await response.clone().text()}`);
  return response.json();
}

try {
  let ready = false;
  for (let attempt = 0; attempt < 100; attempt++) {
    if (serverError) throw serverError;
    if (server.exitCode !== null) throw new Error(logs);
    try { if ((await fetch(base + "/api/recipes")).ok) { ready = true; break; } } catch {}
    await sleep(100);
  }
  assert.ok(ready, "server did not start: " + logs);
  browser = await chromium.launch({ executablePath: process.env.CHROMIUM_PATH || "/usr/bin/chromium", headless: true, args: ["--no-sandbox", "--use-angle=swiftshader", "--enable-unsafe-swiftshader"] });
  const page = await browser.newPage({ viewport: { width: 1512, height: 1080 } });
  page.on("pageerror", error => evidence.browserErrors.push(error.message));
  page.on("console", message => {
    // A deliberately invalid import below returns 400 and is asserted in UI.
    if (message.type() === "error" && !message.text().includes("favicon") && !(/400/.test(message.text()) && message.location().url.endsWith("/api/archetypes"))) {
      evidence.browserErrors.push(message.text());
    }
  });
  // Make UI batches reproducible while exercising the normal browser controls.
  await page.route("**/api/jobs", async route => {
    if (route.request().method() !== "POST") return route.continue();
    const request = route.request().postDataJSON();
    request.search_seed = "12";
    await route.continue({ postData: JSON.stringify(request) });
  });
  const controlsReady = () => page.waitForFunction(() => !document.querySelector("#generate").disabled && document.querySelectorAll("#preset option").length >= 6);
  const meshReady = () => page.waitForFunction(() => document.querySelector("#viewerHint").classList.contains("hidden") && document.querySelector("#viewer canvas") !== null, {}, { timeout: 30000 });
  const rendered = () => page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  async function capture(name, fullPage = false) {
    await meshReady(); await rendered();
    const target = path.join(workspace, name + ".png");
    if (fullPage) await page.screenshot({ path: target, fullPage: true });
    else await page.locator("#viewer").screenshot({ path: target });
    screenshots[name] = target;
    return digest(await fs.readFile(target));
  }
  async function select(candidate) {
    const referenceLoaded = page.waitForResponse(response => response.url() === base + `/api/candidates/${candidate.id}/artifacts/reference` && response.ok());
    await page.locator(`.card[data-id="${candidate.id}"]`).click();
    await page.waitForFunction(id => document.querySelector(".card.selected")?.dataset.id === id && document.querySelector("#provenance").textContent.includes(id), candidate.id);
    await referenceLoaded;
    await meshReady();
    // Wait for the reference artifact as well as the generated GLB to render.
    await page.waitForFunction(() => !document.querySelector("#comparison").disabled);
    await rendered();
  }
  async function importFiles(bytes = sidecar, expectedStatus = 200) {
    await page.locator("#archetypeImport").evaluate(element => element.open = true);
    await page.locator("#archetypeFile").setInputFiles({ name: "bridge.glb", mimeType: "model/gltf-binary", buffer: fixture });
    await page.locator("#sidecarFile").setInputFiles({ name: "bridge.constraints.json", mimeType: "application/json", buffer: bytes });
    const pending = page.waitForResponse(response => response.url() === base + "/api/archetypes" && response.request().method() === "POST");
    await page.locator("#importArchetype").click();
    const response = await pending;
    assert.equal(response.status(), expectedStatus, await response.text());
    const posted = response.request().postDataJSON();
    assert.deepEqual(Buffer.from(posted.glb, "base64"), fixture, "GLB upload changed bytes");
    assert.deepEqual(Buffer.from(posted.sidecar, "base64"), bytes, "sidecar upload changed bytes");
    if (expectedStatus !== 200) return response.json();
    const recipe = await response.json();
    assert.equal(recipe.source.glb_sha256, digest(fixture));
    assert.equal(recipe.source.sidecar_sha256, digest(bytes));
    await controlsReady(); await meshReady();
    assert.equal(await page.locator("#preset").inputValue(), "custom");
    assert.equal(await page.locator("#seed").inputValue(), recipe.seed);
    for (const node of recipe.nodes) for (const [parameter, value] of Object.entries(node.params || {})) {
      const input = page.locator(`#operatorParams input[data-node="${node.id}"][data-param="${parameter}"]`);
      if (await input.count()) assert.equal(Number(await input.inputValue()), value, "import randomized " + node.id + "." + parameter);
    }
    assert.ok(await page.locator("#search").isDisabled(), "conditioned MAP-Elites should remain a separate milestone");
    assert.deepEqual(await fs.readFile(path.join(workspace, "sources", recipe.source.sidecar_sha256 + ".json")), bytes);
    return recipe;
  }
  async function generate(resolution, count) {
    await page.locator("#resolution").selectOption(String(resolution));
    await page.locator("#count").fill(String(count));
    const submitted = page.waitForResponse(response => response.url() === base + "/api/jobs" && response.request().method() === "POST");
    await page.locator("#generate").click();
    const response = await submitted;
    assert.equal(response.status(), 202, await response.text());
    const jobID = (await response.json()).id;
    await page.waitForFunction(() => document.querySelector("#jobState").textContent === "COMPLETED", {}, { timeout: 120000 });
    const job = await api("jobs/" + jobID);
    assert.equal(job.status, "completed", JSON.stringify(job));
    const candidates = await Promise.all(job.candidates.map(id => api("candidates/" + id)));
    assert.equal(candidates.length, count, JSON.stringify(job));
    evidence.batches.push({ resolution, job, candidates });
    await page.waitForFunction(ids => ids.every(id => document.querySelector(`.card[data-id="${id}"]`)), job.candidates);
    return candidates;
  }

  await page.goto(base); await controlsReady();
  const referenceResponse = page.waitForResponse(response => response.url() === base + "/api/reference" && response.status() === 200);
  await page.locator("#preset").selectOption("bridge");
  const normalizedReference = await (await referenceResponse).json();
  assert.equal(normalizedReference.kind, "mesh");
  assert.ok(normalizedReference.mesh.indices.length > 0);
  await controlsReady(); await meshReady();
  assert.equal(await page.locator("#comparison").inputValue(), "reference");
  assert.match(await page.locator("#sourceStatus").innerText(), /Source SHA-256/);
  await capture("reference-before-generation");
  evidence.checks.push("built-in bridge selection and normalized reference preview");

  const imported = await importFiles();
  evidence.checks.push("exact GLB/sidecar upload, content hashes, persisted source, unchanged imported parameters");
  const bridges32 = await generate(32, 2);
  assert.ok(bridges32.every(candidate => candidate.valid), JSON.stringify(bridges32.map(candidate => candidate.problems)));
  assert.equal(bridges32[0].recipe.seed, "42");
  assert.notEqual(bridges32[0].artifacts.volume.hash, bridges32[1].artifacts.volume.hash, "batch did not vary geometry");
  const hashes = [];
  for (let index = 0; index < bridges32.length; index++) {
    await select(bridges32[index]);
    await page.locator("#comparison").selectOption("result");
    assert.match(await page.locator("#constraintStatus").innerText(), /checks passed at 32³/);
    hashes.push(await capture(`result32-${index + 1}`));
  }
  assert.notEqual(hashes[0], hashes[1], "rendered results are identical");
  const kept = bridges32[0];
  await select(kept);
  await page.locator("#comparison").selectOption("reference"); await capture("reference-comparison");
  await page.locator("#comparison").selectOption("overlay"); await capture("overlay32");
  await page.locator("#comparison").selectOption("result");
  const [glbDownload] = await Promise.all([page.waitForEvent("download"), page.locator("#downloadGLB").click()]);
  const glbPath = path.join(workspace, "bridge32.glb"); await glbDownload.saveAs(glbPath);
  const validation = await validator.validateBytes(new Uint8Array(await fs.readFile(glbPath)));
  assert.equal(validation.issues.numErrors, 0, JSON.stringify(validation.issues.messages));
  evidence.glb = { path: glbPath, errors: validation.issues.numErrors, warnings: validation.issues.numWarnings };
  const [zipDownload] = await Promise.all([page.waitForEvent("download"), page.locator("#downloadZIP").click()]);
  evidence.bundle = path.join(workspace, "bridge32.zip"); await zipDownload.saveAs(evidence.bundle);
  await page.locator("#favorite").click();
  await page.waitForFunction(() => document.querySelector("#favorite").textContent.includes("Kept"));
  await capture("desktop", true);
  await page.setViewportSize({ width: 390, height: 844 });
  await capture("mobile", true);
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "mobile horizontal overflow");
  await page.setViewportSize({ width: 1512, height: 1080 });
  await page.reload(); await controlsReady(); await select(kept);
  const restored = await api("candidates/" + kept.id);
  assert.equal(restored.feedback, "favorite");
  assert.deepEqual(restored.recipe, kept.recipe, "reload lost source/constraint/operator provenance");
  evidence.checks.push("32³ batch, distinct rendered geometry, reference/overlay, validated GLB and source ZIP, keep, reload, mobile");

  await importFiles();
  const bridges64 = await generate(64, 2);
  assert.ok(bridges64.every(candidate => candidate.valid), JSON.stringify(bridges64.map(candidate => candidate.problems)));
  assert.notEqual(bridges64[0].artifacts.volume.hash, bridges64[1].artifacts.volume.hash);
  assert.equal((await api("candidates/" + kept.id)).feedback, "favorite", "new batch lost kept candidate");
  assert.equal((await fetch(base + "/api/candidates/" + bridges32[1].id)).status, 404, "new batch retained unkept candidate");
  for (let index = 0; index < bridges64.length; index++) {
    await select(bridges64[index]); await page.locator("#comparison").selectOption("result");
    await capture(`result64-${index + 1}`);
  }
  await select(bridges64[1]); await page.locator("#reject").click();
  await page.waitForFunction(id => !document.querySelector(`.card[data-id="${id}"]`), bridges64[1].id);
  assert.equal((await fetch(base + "/api/candidates/" + bridges64[1].id)).status, 404);
  evidence.checks.push("64³ valid batch, kept candidate survives cleanup, unkept purged, reject permanently removes candidate");

  // Compare a second generation seed with the same starting recipe at both resolutions.
  await importFiles();
  await page.locator("#seed").fill("314159");
  for (const resolution of [32,64]) {
    const [seeded] = await generate(resolution,1);
    assert.ok(seeded.valid,JSON.stringify(seeded.problems));
    assert.equal(seeded.recipe.seed,"314159");
    const baseline = resolution===32 ? bridges32[0] : bridges64[0];
    assert.notEqual(seeded.artifacts.volume.hash,baseline.artifacts.volume.hash);
    await select(seeded);await page.locator("#comparison").selectOption("result");
    await capture(`seed314159-result${resolution}`);
    const seedGLB = new Uint8Array(await (await fetch(base+`/api/candidates/${seeded.id}/asset.glb`)).arrayBuffer());
    const seedValidation = await validator.validateBytes(seedGLB);
    assert.equal(seedValidation.issues.numErrors,0,JSON.stringify(seedValidation.issues.messages));
    await fs.writeFile(path.join(workspace,`seed314159-bridge${resolution}.glb`),seedGLB);
    await page.locator("#favorite").click();
    await page.waitForFunction(()=>document.querySelector("#favorite").textContent.includes("Kept"));
  }
  evidence.checks.push("second seed314159 with identical initial parameters at32/64; distinct geometry and Khronos-valid exports");

  const tightBudget = JSON.parse(sidecar); tightBudget.budget.max_triangles = 1;
  await importFiles(Buffer.from(JSON.stringify(tightBudget, null, 2)));
  const [invalid] = await generate(32, 1);
  assert.equal(invalid.valid, false);
  assert.ok(invalid.problems.some(problem => problem.includes("conditioned triangle budget exceeded")));
  await select(invalid);
  assert.match(await page.locator("#constraintStatus").innerText(), /Constraint failures:.*triangle budget exceeded/);
  assert.ok(await page.locator("#downloadGLB").evaluate(element => element.classList.contains("disabled")));
  assert.equal((await fetch(base + "/api/candidates/" + invalid.id + "/asset.glb")).status, 404);
  await capture("invalid-candidate");
  evidence.checks.push("invalid candidate remains inspectable with explicit budget failure and export disabled");

  const conflicting = JSON.parse(sidecar); conflicting.forbidden.push(structuredClone(conflicting.required[0]));
  const rejection = await importFiles(Buffer.from(JSON.stringify(conflicting)), 400);
  assert.match(rejection.error, /required region .* overlaps forbidden region/);
  await page.waitForFunction(() => !document.querySelector("#error").classList.contains("hidden") && document.querySelector("#error").textContent.includes("overlaps forbidden"));
  evidence.checks.push("conflicting required/forbidden sidecar rejected visibly");
  assert.deepEqual(evidence.browserErrors, []);
  evidence.source = imported.source;
  await fs.writeFile(path.join(workspace, "evidence.json"), JSON.stringify(evidence, null, 2));
  console.log(JSON.stringify({ ...evidence, batches: evidence.batches.map(batch => ({ resolution: batch.resolution, candidates: batch.candidates.map(candidate => ({ id: candidate.id, seed: candidate.recipe.seed, valid: candidate.valid, triangles: candidate.metrics.triangles, problems: candidate.problems })) })), evidence: path.join(workspace, "evidence.json") }, null, 2));
} catch (error) {
  evidence.error = String(error.stack || error);
  await fs.writeFile(path.join(workspace, "evidence.json"), JSON.stringify(evidence, null, 2));
  console.error(logs);
  console.error("Bridge browser evidence:", path.join(workspace, "evidence.json"));
  throw error;
} finally {
  await browser?.close();
  if (server.exitCode === null && !serverError) {
    const exited = new Promise(resolve => server.once("exit", resolve));
    server.kill("SIGTERM"); await exited;
  }
}
