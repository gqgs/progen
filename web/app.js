import * as THREE from "three";
import { OrbitControls } from "three/addons/controls/OrbitControls.js";
import { GLTFLoader } from "three/addons/loaders/GLTFLoader.js";
import { applyPreview, studioEnvironment } from "./material-preview.js";
const $ = (id) => document.getElementById(id);
let operatorSpecs = [], presetRecipes = [];
let recipeChangeEpoch = 0, recipeRandomizing = false, activeFamily = "branching";
let recipe, candidates = [], selected = null, activeJob = null, selectionEpoch = 0, submitting = false;
const loader = new GLTFLoader();
const thumbs = /* @__PURE__ */ new Map();
const bindings = { order: ["eigenmode", "order"], attractions: ["branching", "attractions"], guidance: ["branching", "guidance"], radius: ["branching", "radius"], amount: ["porous", "amount"], frequency: ["porous", "frequency"] };
function showError(error) {
  $("error").textContent = error.message || String(error);
  $("error").classList.remove("hidden");
}
async function api(path, options = {}) {
  const response = await fetch("/api/" + path, { ...options, headers: { "Content-Type": "application/json", ...options.headers } });
  const result = await response.json();
  if (!response.ok) throw new Error(result.error || response.statusText);
  return result;
}
function task(f) {
  return (...args) => Promise.resolve().then(() => f(...args)).catch(showError);
}
function readControls() {
  const r = structuredClone(recipe);
  for (const [id, [op, param]] of Object.entries(bindings)) {
    for (const node of r.nodes) if (node.operator === op) {
      node.params ??= {};
      node.params[param] = Number($(id).value);
    }
  }
  for (const input of document.querySelectorAll("#operatorParams input")) {
    const node = r.nodes.find(n => n.id === input.dataset.node);
    if (node) node.params[input.dataset.param] = Number(input.value);
  }
  r.seed = $("seed").value;
  r.domain.resolution = Number($("resolution").value);
  return r;
}
function syncControls() {
  for (const [id, [op, param]] of Object.entries(bindings)) {
    const node = recipe.nodes.find((n) => n.operator === op);
    $(id).disabled = !node;
    $(id).closest("label").hidden = !node;
    if (node) {
      $(id).value = node.params[param];
      $(id + "Value").value = $(id).value;
    }
  }
  const dynamic = $("operatorParams");
  dynamic.replaceChildren();
  const fixed = new Set(Object.values(bindings).map(([op,param]) => op+":"+param));
  for (const node of recipe.nodes) {
    const desc = operatorSpecs.find(d => d.id === node.operator);
    if (!desc) continue;
    for (const [param, spec] of Object.entries(desc.parameters || {})) {
      if (fixed.has(node.operator+":"+param)) continue;
      const label = document.createElement("label");
      label.textContent = node.operator === "structural_deck" ? (param === "density" ? "Structural floor density (batch varies)" : "Structural floor contour (batch varies)") : node.operator === "floor_relief" ? (param === "depth" ? "Floor depth (m, up to 0.06)" : "Floor relief strength (0 = flat)") : node.operator + " · " + param;
      const input = document.createElement("input");
      const bounds = recipe.source?.spec.parameter_bounds?.[node.id]?.[param] || spec;
      input.type = "number"; input.min = bounds.min; input.max = bounds.max;
      input.step = spec.type === "integer" ? "1" : "any";
      input.value = node.params[param];
      input.dataset.node = node.id; input.dataset.param = param;
      label.append(input); dynamic.append(label);
    }
  }
  $("seed").value = recipe.seed;
  const resolution = String(recipe.domain.resolution);
  if (![...$("resolution").options].some((o) => o.value === resolution)) $("resolution").add(new Option(resolution, resolution));
  $("resolution").value = resolution;
  for (const option of $("resolution").options) option.disabled = !!recipe.source && !["32","64"].includes(option.value);
  $("search").disabled = recipeRandomizing || !!recipe.source;
  $("count").max = recipe.source?.spec.budget.max_candidates || 256;
  if (recipe.source) $("count").value = Math.min(Number($("count").value),Number($("count").max));
  $("sourceStatus").textContent = recipe.source ? `Source SHA-256 ${recipe.source.glb_sha256.slice(0,16)} · masks v${recipe.source.spec.version}` : "";
}
// All entry points use the same server-side randomizer, which covers hidden
// parameters as well as seed and resolution. The epoch prevents late replies
// from overwriting a more recent family selection.
function setRecipeRandomizing(busy) {
  recipeRandomizing = busy;
  for (const id of ["generate", "search", "randomize"]) $(id).disabled = busy;
  $("search").disabled = busy || !!recipe?.source;
}
async function randomizeAllAttributes(baseRecipe) {
  const epoch = ++recipeChangeEpoch;
  setRecipeRandomizing(true);
  try {
    const updated = await api("recipe/randomize", {
      method: "POST",
      body: JSON.stringify(baseRecipe),
    });
    if (epoch !== recipeChangeEpoch) return false;
    recipe = updated;
    syncControls();
    $("reviewStatus").textContent = "Generation attributes randomized.";
    return true;
  } finally {
    if (epoch === recipeChangeEpoch) setRecipeRandomizing(false);
  }
}
for (const id of Object.keys(bindings)) $(id).addEventListener("input", () => $(id + "Value").value = $(id).value);
const scene = new THREE.Scene();
scene.add(new THREE.HemisphereLight(15398892, 3293752, .8));
const light = new THREE.DirectionalLight(16774361, 2);
light.position.set(3, 5, 4);
scene.add(light);
const rim = new THREE.DirectionalLight(9105617, 1);
rim.position.set(-3, 1, -2);
scene.add(rim);
const camera = new THREE.PerspectiveCamera(38, 1, 0.01, 100);
camera.position.set(2.8, 1.8, 3.5);
let renderer, controls, model, graphObject, referenceObject, environment;
let previewDomainSize = 1;
try {
  renderer = new THREE.WebGLRenderer({ alpha: true, antialias: true, preserveDrawingBuffer: true });
  renderer.setPixelRatio(Math.min(devicePixelRatio, 2));
  renderer.setClearColor(1516060, 0);
  renderer.outputColorSpace = THREE.SRGBColorSpace;
  renderer.toneMapping = THREE.ACESFilmicToneMapping;
  renderer.toneMappingExposure = 1;
  environment = studioEnvironment(renderer);
  scene.environment = environment.texture;
  scene.environmentIntensity = .8;
  scene.background = new THREE.Color(0x303840);
  $("viewer").append(renderer.domElement);
  controls = new OrbitControls(camera, renderer.domElement);
  controls.enableDamping = true;
  new ResizeObserver(() => {
    const w = $("viewer").clientWidth, h = $("viewer").clientHeight;
    renderer.setSize(w, h, false);
    camera.aspect = w / h;
    camera.updateProjectionMatrix();
  }).observe($("viewer"));
  renderer.setAnimationLoop(() => {
    controls.update();
    renderer.render(scene, camera);
  });
} catch (error) {
  showError(new Error("WebGL is unavailable. Generation and exports still work. " + error.message));
}
function dispose(object) {
  if (!object) return;
  object.traverse((o) => {
    o.geometry?.dispose();
    if (Array.isArray(o.material)) o.material.forEach((m) => m.dispose());
    else o.material?.dispose();
  });
  object.removeFromParent();
}
function frame(object, cam, orbit) {
  const box = new THREE.Box3().setFromObject(object);
  const center = box.getCenter(new THREE.Vector3());
  const size = box.getSize(new THREE.Vector3()).length();
  cam.position.copy(center).add(new THREE.Vector3(1, 0.65, 1.3).normalize().multiplyScalar(size * 1.6));
  cam.near = Math.max(1e-3, size / 100);
  cam.far = Math.max(100, size * 20);
  cam.updateProjectionMatrix();
  cam.lookAt(center);
  if (orbit) {
    orbit.target.copy(center);
    orbit.update();
  }
}
let thumbnailRenderer;
async function thumbnail(c) {
  if (thumbs.has(c.id)) return thumbs.get(c.id);
  if (!c.valid || !renderer) return "";
  thumbnailRenderer ??= new THREE.WebGLRenderer({ antialias: true, alpha: false, preserveDrawingBuffer: true });
  thumbnailRenderer.setSize(300, 250);
  thumbnailRenderer.setClearColor(1845793);
  thumbnailRenderer.toneMapping = THREE.ACESFilmicToneMapping;
  const gltf = await loader.loadAsync(`/api/candidates/${c.id}/asset.glb`);
  const s = new THREE.Scene();
  s.add(new THREE.HemisphereLight(15333353, 2505517, 2.5));
  const l = new THREE.DirectionalLight(16773336, 3);
  l.position.set(3, 5, 4);
  s.add(l, gltf.scene);
  const cam = new THREE.PerspectiveCamera(35, 1.2, 0.01, 100);
  frame(gltf.scene, cam);
  thumbnailRenderer.render(s, cam);
  const url = thumbnailRenderer.domElement.toDataURL("image/png");
  thumbs.set(c.id, url);
  dispose(gltf.scene);
  return url;
}
let galleryEpoch = 0;
const pendingThumbnails = new Set();
async function drawGallery() {
  const epoch = ++galleryEpoch;
  const filter = $("filter").value;
  const list = candidates.filter((c) => filter === "all" || c.feedback === filter);
  $("candidateCount").textContent = candidates.length;
  $("empty").classList.toggle("hidden", candidates.length > 0);
  const visible = new Set(list.map(c => c.id));
  // Snapshot the live HTMLCollection: removing while iterating it skips siblings.
  for (const card of [...$("gallery").children]) if (!visible.has(card.dataset.id)) card.remove();
  for (const c of list) {
    const existing = $("gallery").querySelector(`[data-id="${c.id}"]`);
    if (existing) {
      existing.classList.toggle("selected", selected?.id === c.id);
      existing.querySelector(".badge").textContent = c.feedback === "favorite" ? "★" : c.feedback === "rejected" ? "×" : "";
      existing.onclick = task(() => select(c));
      continue;
    }
    const card = document.createElement("button");
    card.className = "card" + (selected?.id === c.id ? " selected" : "");
    card.dataset.id = c.id;
    card.setAttribute("aria-label", `Inspect specimen ${c.id.slice(0, 8)}`);
    const img = document.createElement("img");
    img.alt = c.valid ? "Generated procedural specimen" : "Invalid specimen";
    const info = document.createElement("div");
    info.className = "card-info";
    const strong = document.createElement("strong");
    strong.textContent = `SPECIMEN ${c.id.slice(0, 6).toUpperCase()}`;
    const sub = document.createElement("span");
    const compositionLabel = c.artifacts?.graph
      ? `${Math.round(c.metrics.porosity * 100)}% porous`
      : `${Math.round(c.metrics.solid_fraction * 100)}% filled`;
    sub.textContent = c.valid ? `${c.metrics.triangles.toLocaleString()} faces · ${c.recipe.source?.spec.deck_policy ? "Exploratory · walking not certified" : compositionLabel}` : "Invalid geometry";
    const badge = document.createElement("span");
    badge.className = "badge";
    badge.textContent = c.feedback === "favorite" ? "\u2605" : c.feedback === "rejected" ? "\xD7" : "";
    info.append(badge, strong, sub);
    card.append(img, info);
    card.onclick = task(() => select(c));
    const next = list.slice(list.indexOf(c) + 1).map(item => $("gallery").querySelector(`[data-id="${item.id}"]`)).find(Boolean);
    $("gallery").insertBefore(card, next || null);
  }
  for (const c of list) {
    if (epoch !== galleryEpoch) return;
    try {
      const pending = thumbnail(c);
      pendingThumbnails.add(pending);
      let src;
      try { src = await pending; } finally { pendingThumbnails.delete(pending); }
      if (epoch !== galleryEpoch) return;
      const img = $("gallery").querySelector(`[data-id="${c.id}"] img`);
      if (img && src && img.getAttribute("src") !== src) {
        img.onload = () => img.classList.add("loaded");
        img.src = src;
      }
    } catch (err) {
      showError(err);
    }
  }
}
async function refresh() {
  candidates = await api("candidates");
  if (selected) selected = candidates.find((c) => c.id === selected.id) || null;
  updateSelection();
  void drawGallery();
  await drawArchive();
}
async function drawArchive() {
  const expectedSelection = selected?.id;
  const a = await api("archive" + (selected ? "?candidate=" + selected.id : "?preset=" + encodeURIComponent($("preset").value === "custom" ? "branching" : $("preset").value)));
  if (selected?.id !== expectedSelection) return;
  $("archive").replaceChildren();
  $("occupied").textContent = Object.keys(a.cells).length + " occupied";
  const range = (r) => `[${r.min}, ${r.max}${r.max_exclusive ? ")" : "]"}`;
  $("archiveX").textContent = `${a.axes[0]} ${range(a.ranges[0])} →`;
  $("archiveY").textContent = `${a.axes[1]} ${range(a.ranges[1])} ↑`;
  $("archive").setAttribute("aria-label", `${a.descriptor_version}: ${a.axes.join(", ")}`);
  for (let y = 15; y >= 0; y--) for (let x = 0; x < 16; x++) {
    const id = a.cells[`${x},${y}`];
    const cell = document.createElement("button");
    const interval = (i, axis) => `[${i / a.bins}, ${(i + 1) / a.bins}${i === a.bins - 1 && !a.ranges[axis].max_exclusive ? "]" : ")"}`;
    cell.title = `${a.axes[0]} ${interval(x, 0)}, ${a.axes[1]} ${interval(y, 1)}${id ? " · " + id.slice(0, 8) : " · empty"}`;
    cell.setAttribute("aria-label", cell.title);
    cell.disabled = !id;
    if (id) {
      cell.className = "filled";
      cell.onclick = task(async () => select(await api("candidates/" + id)));
    }
    $("archive").append(cell);
  }
}
function updateSelection() {
  const c = selected;
  if (!c) {
    ++selectionEpoch;
    dispose(model); dispose(graphObject); dispose(referenceObject);
    referenceObject = null;
    $("comparison").disabled = true;
    $("constraintStatus").classList.add("hidden");
    model = graphObject = null;
    $("selectedTitle").textContent = "No specimen selected";
    $("selectedId").textContent = "Your selection will appear here.";
    $("viewerHint").textContent = "Select a candidate to inspect";
    $("viewerHint").classList.remove("hidden");
    for (const id of ["favorite", "reject", "vary"]) $(id).disabled = true;
    for (const id of ["downloadGLB", "downloadZIP"]) { $(id).removeAttribute("href"); $(id).classList.add("disabled"); }
    $("metrics").replaceChildren();
    $("provenance").textContent = "No selection";
    return;
  }
  $("selectedTitle").textContent = `Specimen ${c.id.slice(0, 6).toUpperCase()}`;
  $("selectedId").textContent = `${c.feedback === "favorite" ? "KEPT" : c.feedback.toUpperCase()} \xB7 SEED ${c.recipe.seed} \xB7 ${c.id.slice(0, 16)}`;
  $("favorite").disabled = false;
  $("reject").disabled = false;
  $("favorite").textContent = c.feedback === "favorite" ? "\u2605 Kept" : "\u2606 Keep";
  $("reject").textContent = "Reject & delete";
  $("vary").disabled = !c.valid || c.feedback !== "favorite";
  $("metrics").replaceChildren();
  for (const [label, value] of [["TRIANGLES", c.metrics.triangles.toLocaleString()], ...(c.artifacts?.graph
    ? [["POROSITY", `${(c.metrics.porosity * 100).toFixed(1)}%`]]
    : [["SOLID VOLUME", `${(c.metrics.solid_fraction * 100).toFixed(1)}%`], ["EXPOSED FACES", `${(c.metrics.surface_fraction * 100).toFixed(1)}%`]]), ["CONNECTED SOLID", `${(c.metrics.connected_fraction * 100).toFixed(1)}%`], ["GENERATION", `${(c.milliseconds / 1e3).toFixed(2)}s`]]) {
    const div = document.createElement("div");
    div.className = "metric";
    const a = document.createElement("span");
    a.textContent = label;
    const b = document.createElement("strong");
    b.textContent = value;
    div.append(a, b);
    $("metrics").append(div);
  }
  for (const [id, path] of [["downloadGLB", "asset.glb"], ["downloadZIP", "export.zip"]]) {
    const a = $(id);
    a.classList.toggle("disabled", !c.valid);
    if (c.valid) {
      a.href = `/api/candidates/${c.id}/${path}`;
      a.download = `progen-${c.id.slice(0, 8)}.${path.split(".").at(-1)}`;
    }
  }
  $("constraintStatus").classList.toggle("hidden",!c.recipe.source);
  if (c.recipe.source?.spec.deck_policy === "structural-experiment") $("constraintStatus").textContent = c.valid
    ? `LESS PROTECTED EXPERIMENT · mesh/source constraints passed; not a game-ready walking floor. Walking-strip diagnostics: ${c.walking_checks?.evaluated ? (c.walking_checks.passed ? "sampled checks passed (not certification)" : c.walking_checks.problems.join("; ")) : "unavailable"}.`
    : `Experimental mesh/source failures: ${c.problems.join("; ")}`;
  else if (c.recipe.source) $("constraintStatus").textContent = c.valid
    ? `Deck, endpoints, clearance and editable-region checks passed at ${c.recipe.domain.resolution}³ (floor tolerance ${c.recipe.source.spec.traversal.surface_tolerance} m).`
    : `Constraint failures: ${c.problems.join("; ")}`;
  $("provenance").textContent = JSON.stringify({ id: c.id, parent: c.parent, engine: c.engine, valid: c.valid, problems: c.problems, walking_checks: c.walking_checks, nodes: c.nodes, recipe: c.recipe }, null, 2);
  if (!c.valid) showError(new Error(c.problems.join("; ")));
}
async function select(c) {
  selected = c;
  const epoch = ++selectionEpoch;
  updateSelection();
  for (const card of document.querySelectorAll(".card")) card.classList.toggle("selected", card.dataset.id === c.id);
  await drawArchive();
  if (epoch !== selectionEpoch) return;
  dispose(model);
  dispose(graphObject);
  dispose(referenceObject);
  referenceObject = null;
  $("comparison").disabled = !c.recipe.source;
  $("comparison").value = "result";
  model = null;
  graphObject = null;
  if ((!c.valid && !c.recipe.source) || !renderer) {
    $("viewerHint").textContent = c.valid ? "WebGL unavailable" : "No valid mesh";
    $("viewerHint").classList.remove("hidden");
    return;
  }
  $("viewerHint").textContent = "Loading specimen\u2026";
  $("viewerHint").classList.remove("hidden");
  const gltf = c.valid ? await loader.loadAsync(`/api/candidates/${c.id}/asset.glb`) : {scene: artifactMesh((await api(`candidates/${c.id}/artifacts/mesh`)).mesh)};
  if (epoch !== selectionEpoch) {
    dispose(gltf.scene);
    return;
  }
  model = gltf.scene;
  setPreviewDomain(c.recipe);
  applyPreview(model, $("materialPreset").value, previewDomainSize);
  scene.add(model);
  frame(model, camera, controls);
  $("viewerHint").classList.add("hidden");
  $("graphToggle").disabled = !c.artifacts?.graph;
  if (c.recipe.source) {
    const artifact = await api(`candidates/${c.id}/artifacts/reference`);
    if (epoch !== selectionEpoch) return;
    referenceObject = artifactMesh(artifact.mesh,true);
    scene.add(referenceObject);
    renderComparison();
  }
  if (!c.artifacts?.graph) return;
  const artifact = await api(`candidates/${c.id}/artifacts/graph`);
  if (epoch !== selectionEpoch) return;
  const positions = [];
  for (const v of artifact.graph.vertices) {
    if (v.parent < 0) continue;
    const p = artifact.graph.vertices[v.parent].position;
    positions.push(v.position.X, v.position.Y, v.position.Z, p.X, p.Y, p.Z);
  }
  const geometry = new THREE.BufferGeometry();
  geometry.setAttribute("position", new THREE.Float32BufferAttribute(positions, 3));
  graphObject = new THREE.LineSegments(geometry, new THREE.LineBasicMaterial({ color: 8716256, depthTest: false, transparent: true, opacity: 0.8 }));
  graphObject.renderOrder = 10;
  graphObject.visible = $("graphToggle").checked && $("comparison").value !== "reference";
  scene.add(graphObject);
}

function artifactMesh(mesh, reference = false) {
  const geometry = new THREE.BufferGeometry();
  geometry.setAttribute("position", new THREE.Float32BufferAttribute(mesh.positions.flatMap(p=>[p.X,p.Y,p.Z]),3));
  geometry.setIndex(mesh.indices);
  geometry.computeVertexNormals();
  const material = new THREE.MeshStandardMaterial({color: reference ? 0xd6a76d : 0x70b7a9,roughness:.65,side:THREE.DoubleSide});
  return new THREE.Mesh(geometry,material);
}
function setPreviewDomain(r) {
  const b = r.domain.bounds;
  previewDomainSize = Math.max(b.Max.X-b.Min.X,b.Max.Y-b.Min.Y,b.Max.Z-b.Min.Z);
}
function renderComparison() {
  const mode = $("comparison").value;
  if (model) model.visible = mode !== "reference";
  if (graphObject) graphObject.visible = mode !== "reference" && $("graphToggle").checked;
  if (referenceObject) {
    referenceObject.visible = mode !== "result" || !model;
    applyPreview(referenceObject, $("materialPreset").value, previewDomainSize, mode === "overlay");
  }
}
$("materialPreset").onchange = () => {
  applyPreview(model, $("materialPreset").value, previewDomainSize);
  renderComparison();
  $("glassHelp").hidden = $("materialPreset").value !== "glass";
};
$("previewBackground").onchange = () => {
  scene.background = new THREE.Color($("previewBackground").value);
};
$("comparison").onchange = renderComparison;
async function previewReference() {
  const epoch = ++selectionEpoch;
  const artifact = await api("reference",{method:"POST",body:JSON.stringify(readControls())});
  if (epoch !== selectionEpoch || !renderer) return;
  dispose(model);dispose(graphObject);dispose(referenceObject);
  model = graphObject = null;
  setPreviewDomain(readControls());
  referenceObject = artifactMesh(artifact.mesh,true);scene.add(referenceObject);
  frame(referenceObject,camera,controls);
  $("comparison").disabled = false;$("comparison").value = "reference";
  $("graphToggle").disabled = true;
  $("viewerHint").classList.add("hidden");renderComparison();
}
$("importArchetype").onclick = task(async () => {
  const glb = $("archetypeFile").files[0], sidecar = $("sidecarFile").files[0];
  if (!glb || !sidecar) throw new Error("Select both a GLB archetype and its constraint sidecar.");
  if (glb.size > 8*1024*1024 || sidecar.size > 64*1024) throw new Error("Import limit: 8 MiB GLB and 64 KiB sidecar.");
  const epoch = ++recipeChangeEpoch;
  setRecipeRandomizing(true);
  try {
    const bytes = new Uint8Array(await glb.arrayBuffer());
    let binary = "";for (const b of bytes) binary += String.fromCharCode(b);
    const sidecarBytes = new Uint8Array(await sidecar.arrayBuffer());
    let sidecarBinary = "";for (const b of sidecarBytes) sidecarBinary += String.fromCharCode(b);
    const imported = await api("archetypes",{method:"POST",body:JSON.stringify({glb:btoa(binary),sidecar:btoa(sidecarBinary)})});
    if (epoch !== recipeChangeEpoch) return;
    recipe=imported;activeFamily="custom";$("preset").value="custom";
    selected=null;updateSelection();syncControls();await previewReference();
    $("reviewStatus").textContent = "Archetype and semantic masks imported. Generate to explore decorative variations.";
  } finally {if(epoch===recipeChangeEpoch)setRecipeRandomizing(false);}
});

$("graphToggle").onchange = () => {
  if (graphObject) graphObject.visible = $("graphToggle").checked && $("comparison").value !== "reference";
};
async function feedback(state) {
  if (state === "rejected") {
    events.close(); ++galleryEpoch;
    await Promise.allSettled([...pendingThumbnails]);
  }
  try {
  await api(`candidates/${selected.id}/feedback`, { method: "POST", body: JSON.stringify({ state }) });
  await refresh();
  updateSelection();
  } finally { if (state === "rejected") connectEvents(); }
}
$("favorite").onclick = task(() => feedback(selected.feedback === "favorite" ? "neutral" : "favorite"));
$("reject").onclick = task(() => feedback("rejected"));
$("randomize").onclick = task(() => randomizeAllAttributes(readControls()));
$("rejectUnkept").onclick = task(async () => {
  events.close(); ++galleryEpoch;
  await Promise.allSettled([...pendingThumbnails]);
  let result;
  try { result = await api("candidates/reject-unkept", { method: "POST" }); }
  finally { connectEvents(); }
  await refresh();
  $("reviewStatus").textContent = `Permanently deleted ${result.rejected} unkept specimens. Kept specimens preserved.`;
});
$("purge").onclick = task(async () => {
  if (!confirm("Permanently delete ALL generations, including kept specimens, meshes, caches, archives, and job history?")) return;
  events.close();
  ++galleryEpoch;
  await Promise.allSettled([...pendingThumbnails]);
  try {
    await api("generations", { method: "DELETE", body: JSON.stringify({ confirm: "purge-all-generations" }) });
  } catch (error) { connectEvents(); throw error; }
  thumbs.clear();
  $("error").classList.add("hidden");
  $("job").classList.add("hidden");
  await refresh();
  $("reviewStatus").textContent = "All generations purged.";
  connectEvents();
});
$("filter").onchange = () => void drawGallery();
async function pollJob() {
  if (!activeJob) return;
  const j = await api("jobs/" + activeJob);
  $("jobState").textContent = j.status.toUpperCase();
  $("jobProgress").textContent = `${j.completed}/${j.total} \xB7 ${j.stage || j.request.kind}${j.skipped ? ` \xB7 ${j.skipped} skipped` : ""}`;
  $("progress").max = j.total;
  $("progress").value = j.completed;
  const running = ["queued", "running"].includes(j.status);
  $("cancel").disabled = !running;
  if (running) {
    setTimeout(() => task(pollJob)(), 700);
  } else {
    activeJob = null;
    await refresh();
    if (j.error) showError(new Error(j.error));
    if (j.failures?.length) {
      const first = j.failures[0];
      $("reviewStatus").textContent = `Batch finished: ${j.candidates.length} generated, ${j.failures.length} skipped. Candidate ${first.index + 1}: ${first.reason}`;
    }
    if (j.candidates?.length) {
      const c = candidates.find((c2) => c2.id === j.candidates[0]);
      if (c) await select(c);
    }
  }
}
async function start(kind) {
  if (recipeRandomizing) throw new Error("Wait for the geometry settings to finish randomizing.");
  if (activeJob || submitting) throw new Error("Wait for the active job or cancel it first.");
  submitting = true;
  try {
    $("error").classList.add("hidden");
    $("jobState").textContent = "SUBMITTING";
    $("job").classList.remove("hidden");
    const req = { kind, recipe: readControls(), count: Number($("count").value), scale: Number($("scale").value), search_seed: crypto.getRandomValues(new Uint32Array(1))[0], workers: 1 };
    if (kind === "vary") {
      req.parent = selected.id;
      delete req.recipe;
    }
    events.close(); ++galleryEpoch; ++selectionEpoch;
    await Promise.allSettled([...pendingThumbnails]);
    const j = await api("jobs", { method: "POST", body: JSON.stringify(req) });
    activeJob = j.id;
    $("job").classList.remove("hidden");
    await pollJob();
  } finally {
    events.close(); connectEvents();
    submitting = false;
  }
}
$("controls").onsubmit = (event) => {
  event.preventDefault();
  task(() => start("generate"))();
};
$("search").onclick = task(() => start("search"));
$("vary").onclick = task(() => start("vary"));
$("cancel").onclick = task(async () => {
  if (activeJob) await api("jobs/" + activeJob, { method: "DELETE" });
});
$("recipeFile").onchange = task(async (event) => {
  const file = event.target.files[0];
  if (!file) return;
  const epoch = ++recipeChangeEpoch;
  setRecipeRandomizing(true);
  try {
    const imported = await api("validate", { method: "POST", body: await file.text() });
    if (epoch !== recipeChangeEpoch) return;
    recipe = imported;
    activeFamily = "custom";
    $("preset").value = "custom";
    syncControls();
    if (recipe.source) await previewReference();
  } finally {
    if (epoch === recipeChangeEpoch) setRecipeRandomizing(false);
  }
});
$("downloadRecipe").onclick = () => {
  const url = URL.createObjectURL(new Blob([JSON.stringify(readControls(), null, 2)], { type: "application/json" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = "recipe.json";
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1e3);
};
await task(async () => {
  operatorSpecs = await api("operators");
  presetRecipes = await api("recipes");
  for (const p of presetRecipes) $("preset").add(new Option(p.name, p.id));
  $("preset").add(new Option("Imported recipe", "custom"));
  $("preset").value = "branching";
  $("preset").onchange = task(async () => {
    const family = $("preset").value;
    const found = presetRecipes.find(p => p.id === family);
    if (!found) return;
    try {
      if (!await randomizeAllAttributes(found.recipe)) return;
      activeFamily = family;
      selected = null;
      updateSelection();
      if (recipe.source) await previewReference();
      await drawArchive();
    } catch (error) {
      if ($("preset").value === family) $("preset").value = activeFamily;
      throw error;
    }
  });
  await randomizeAllAttributes(await api("recipe"));
  await refresh();
  if (candidates.length) await select(candidates[0]);
  const jobs = await api("jobs");
  const active = jobs.find((j) => ["running", "queued"].includes(j.status));
  if (active) {
    activeJob = active.id;
    $("job").classList.remove("hidden");
    await pollJob();
  } else {
    const interrupted = jobs.find((j) => j.status === "interrupted");
    if (interrupted) showError(new Error(interrupted.error));
  }
})();


let events;
function connectEvents() {
events = new EventSource("/api/events");
events.onmessage = task(async event => {
  candidates = JSON.parse(event.data);
  if (selected) selected = candidates.find(c => c.id === selected.id) || null;
  updateSelection();
  await drawGallery();
  await drawArchive();
});
}
connectEvents();
window.addEventListener("pagehide", () => events.close());
