import fs from "node:fs/promises";
import path from "node:path";
import validator from "gltf-validator";
const root = process.argv[2];
if (!root) throw new Error("Usage: npm run test:glb -- <file.glb or workspace>");
let files = [];
if (root.endsWith(".glb")) files = [root];
else for (const id of await fs.readdir(path.join(root, "candidates"))) {
  const f = path.join(root, "candidates", id, "asset.glb");
  try {
    await fs.access(f);
    files.push(f);
  } catch {
  }
}
if (!files.length) throw new Error("No GLB files found");
for (const file of files) {
  const result = await validator.validateBytes(new Uint8Array(await fs.readFile(file)), { uri: path.basename(file) });
  console.log(JSON.stringify({ file, errors: result.issues.numErrors, warnings: result.issues.numWarnings, triangles: result.info?.totalTriangleCount }));
  if (result.issues.numErrors) throw new Error(JSON.stringify(result.issues.messages, null, 2));
}
