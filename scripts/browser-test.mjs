import { chromium } from "playwright";
import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";
import os from "node:os";
import assert from "node:assert/strict";
import validator from "gltf-validator";
const workspace = await fs.mkdtemp(path.join(os.tmpdir(), "progen-browser-"));
const binary = process.env.PROGEN_BIN || "/tmp/progen-mvp";
const port = process.env.PROGEN_TEST_PORT || "18080";
const base = `http://127.0.0.1:${port}`;
let server, browser;
let log = "";
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function start() {
  server = spawn(binary, ["serve", "--workspace", workspace, "--addr", `127.0.0.1:${port}`], { stdio: ["ignore", "pipe", "pipe"] });
  server.stdout.on("data", (d) => log += d);
  server.stderr.on("data", (d) => log += d);
  let startError;
  server.once("error", (e) => startError = e);
  for (let i = 0; i < 100; i++) {
    if (startError) throw startError;
    if (server.exitCode !== null) throw new Error(log);
    try {
      const r = await fetch(base + "/api/operators");
      if (r.ok) return;
    } catch {
    }
    await sleep(100);
  }
  throw new Error("Server unavailable: " + log);
}
async function stop() {
  if (!server || server.exitCode !== null) return;
  const exited = new Promise((r) => server.once("exit", r));
  server.kill("SIGTERM");
  await exited;
}
try {
  await start();
  browser = await chromium.launch({ executablePath: process.env.CHROMIUM_PATH || "/usr/bin/chromium", headless: true, args: ["--no-sandbox", "--use-angle=swiftshader", "--enable-unsafe-swiftshader"] });
  const page = await browser.newPage({ viewport: { width: 1512, height: 1080 } });
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (msg) => {
    if (msg.type() === "error" && !msg.text().includes("favicon")) errors.push(msg.text());
  });
  await page.goto(base);
  await page.waitForFunction(() => document.querySelector("#order").disabled === false && document.querySelectorAll("#archive button").length === 256);
  await page.locator("#seed").fill("18446744073709551615");
  await page.locator("#count").fill("2");
  await page.locator("#resolution").selectOption("32");
  await page.locator("#generate").click();
  await page.waitForFunction(() => document.querySelector("#jobState").textContent === "COMPLETED", {}, { timeout: 6e4 });
  await page.waitForFunction(() => document.querySelectorAll(".card").length === 2 && document.querySelector("#favorite").disabled === false);
  await page.waitForFunction(() => document.querySelector("#viewerHint").classList.contains("hidden"), {}, { timeout: 2e4 });
  await page.waitForFunction(() => [...document.querySelectorAll(".card img")].filter((i) => i.src.startsWith("data:")).length >= 1);
  const initial = await (await fetch(base + "/api/candidates")).json();
  assert.equal(initial.length, 2);
  assert.ok(initial.every((c) => c.feedback === "neutral"), "new batches remain reviewable");
  assert.equal(initial[0].recipe.seed, "18446744073709551615");
  const first = await page.locator(".card.selected").getAttribute("data-id");
  assert.ok(initial.find((c) => c.id === first).valid);
  await page.locator("#favorite").click();
  await page.waitForFunction(() => document.querySelector("#favorite").textContent.includes("Kept"));
  await page.reload();
  await page.waitForFunction(() => document.querySelectorAll(".card").length === 2);
  await page.locator(`.card[data-id="${first}"]`).click();
  await page.waitForFunction(() => document.querySelector("#favorite").textContent.includes("Kept"));
  await page.locator("#graphToggle").check();
  await page.locator("#count").fill("1");
  await page.locator("#vary").click();
  await page.waitForFunction(() => document.querySelector("#jobState").textContent === "COMPLETED" && document.querySelectorAll(".card").length === 2, {}, { timeout: 6e4 });
  const after = await (await fetch(base + "/api/candidates")).json();
  const child = after.find((c) => c.parent === first);
  assert.ok(child);
  assert.equal(child.recipe.seed, initial.find((c) => c.id === first).recipe.seed);
  await page.locator(`.card[data-id="${child.id}"]`).click();
  await page.locator("#reject").click();
  await page.waitForFunction(() => document.querySelectorAll(".card").length === 1);
  assert.equal((await fetch(base + "/api/candidates/" + child.id)).status, 404);
  await page.locator(`.card[data-id="${first}"]`).click();
  const [download] = await Promise.all([page.waitForEvent("download"), page.locator("#downloadGLB").click()]);
  const glbPath = path.join(workspace, "download.glb");
  await download.saveAs(glbPath);
  const glb = await fs.readFile(glbPath);
  const validation = await validator.validateBytes(new Uint8Array(glb));
  assert.equal(validation.issues.numErrors, 0, JSON.stringify(validation.issues.messages));
  const [bundle] = await Promise.all([page.waitForEvent("download"), page.locator("#downloadZIP").click()]);
  await bundle.saveAs(path.join(workspace, "bundle.zip"));
  await page.locator("#resolution").selectOption("32");
  await page.locator("#count").fill("2");
  await page.locator("#search").click();
  await page.waitForFunction(() => document.querySelector("#jobState").textContent === "COMPLETED", {}, { timeout: 6e4 });
  await page.locator(`.card[data-id="${first}"]`).click();
  await page.waitForFunction(() => document.querySelectorAll("#archive .filled").length > 0);
  await page.waitForFunction(() => document.querySelector("#viewerHint").classList.contains("hidden"));
  await page.screenshot({ path: path.join(workspace, "desktop.png"), fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({ path: path.join(workspace, "mobile.png"), fullPage: true });
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1), "mobile overflow");
  await page.goto("about:blank");
  await stop();
  await start();
  await page.goto(base);
  await page.waitForFunction(() => document.querySelectorAll(".card").length >= 3);
  const restored = await (await fetch(base + "/api/candidates/" + first)).json();
  assert.equal(restored.feedback, "favorite");
  const beforeRandomize = await page.locator("#seed").inputValue();
  await page.locator("#randomize").click();
  await page.waitForFunction((seed) => document.querySelector("#seed").value !== seed, beforeRandomize);
  await page.locator("#rejectUnkept").click();
  await page.waitForFunction(() => document.querySelector("#reviewStatus").textContent.includes("Kept specimens preserved"));
  assert.equal((await (await fetch(base + "/api/candidates/" + first)).json()).feedback, "favorite");
  page.once("dialog", (dialog) => dialog.dismiss());
  await page.locator("#purge").click();
  assert.ok((await (await fetch(base + "/api/candidates")).json()).length > 0);
  page.once("dialog", (dialog) => dialog.accept());
  await page.locator("#purge").click();
  await page.waitForFunction(() => document.querySelector("#candidateCount").textContent === "0");
  assert.equal(await page.locator("#favorite").isDisabled(), true);
  assert.equal(await page.locator("#downloadGLB").getAttribute("href"), null);
  assert.equal((await (await fetch(base + "/api/jobs")).json()).length, 0);
  assert.deepEqual(errors, []);
  console.log(JSON.stringify({ workspace, candidates: (await (await fetch(base + "/api/candidates")).json()).length, glbErrors: validation.issues.numErrors, glbWarnings: validation.issues.numWarnings, browserErrors: errors, checks: ["generate", "inspect", "favorite", "reload", "vary", "permanent rejection", "next-batch cleanup", "graph overlay", "GLB export", "source ZIP", "MAP-Elites", "responsive layout", "server restart", "unreviewed batch results", "bulk rejection", "randomize", "cancel purge", "purge"] }, null, 2));
} catch (error) {
  console.error(log);
  throw error;
} finally {
  await browser?.close();
  await stop();
}

