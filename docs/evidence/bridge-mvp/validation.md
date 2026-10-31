# Bridge MVP verification

Implementation: `5ae7fcf482a77943589b96563dee24aa05b72d60`.
Reviewed code: `83fa949d99e7c9a65cc2a86667f4fc12ebfe386e`.
The publication commit adds these review documents/images and lets the implicit browser test use CI’s bundled Chromium by default (`CHROMIUM_PATH` remains an override). Application source is unchanged. Exact publication-head hosted CI is reported on the draft PR; this document does not predeclare a CI result.

## Local final verification

| Check | Result |
|---|---|
| Full `go test -race -count=1 -timeout=20m ./...` | Passed, 489.544 s |
| Full `go test -count=1 ./...` | Passed, 52.376 s |
| `go vet ./...`; binary build | Passed |
| Clean npm install; bundle syntax; browser build | Passed |
| Original Chromium workflow | Passed: generation, review, reload, variation, search, export, restart, cleanup and cancellation |
| TPMS/Voronoi/hybrid Chromium workflow | Passed |
| Bridge Chromium workflow | Passed: import, selection, reference/result/overlay, both resolutions/seeds, failures, review, reload and mobile layout |
| Four demo GLBs, Khronos validator | Passed: zero errors and warnings |
| Deterministic replay and fresh-workspace source reload | Passed, including nonidentity scale/translation |
| Invalid candidate admission | Passed: keeping an invalid specimen enables neither archive admission nor GLB/ZIP download |

Runtime: Go 1.26.10 linux/amd64, Node 24.19.0 and installed system Chromium. The local run used the provided browser; hosted CI independently uses its configured Node 22 environment. No further 386 execution was attempted because repository CI has no 386 requirement/matrix. Timings are observations from a shared test environment, not performance guarantees.

| Resolution | Generation seed | Triangles |
|---|---:|---:|
| 32³ | 42 | 21,964 |
| 32³ | 314159 | 22,780 |
| 64³ | 42 | 102,088 |
| 64³ | 314159 | 109,364 |

The committed screenshots were inspected in rendered browser views. Owner visual acceptance remains pending.

## Critical-review findings addressed

- An unrelated enclosing mesh shell could evade corridor surface-crossing tests while its separate intended volume stayed clear. Conditioned output now must match the canonical marching-tetrahedra surface of the exposed result volume in GLB precision.
- Occupied grid signs could hide a thin cut through required solid or an anchor. Actual emitted triangles are also checked against protected interiors.
- Geometry predicates now use GLB float32 positions and reject export-quantization collapse. Tests cover a forbidden-region intrusion introduced only by float32 rounding.
- Very small valid importer faces could be zeroed by a generic vector normalization cutoff. Face/vertex normalization was corrected without weakening shell topology or convexity checks.
- Source reloads are bounded before hashing; unsigned GLB chunk sizes are checked before integer narrowing. Native malformed-input regressions passed; no 386 runtime pass is claimed.

## Exact source and scope

Reference GLB SHA-256: `ad7877aa31d8dd94ee0557dce21a92e28029e53a25e5848d873dd47d58eb756a`.

Sidecar SHA-256: `8fe3868bfc7dd2fc6654486b6f254b502facb40b0a24111710ac16d03e8e17a3`.

Input supports static, closed, outward-oriented convex-shell unions with explicit positive coordinate transforms into right-handed Y-up meters. Unsupported open/concave/nonmanifold shells, external resources, skins, animation, morphs and extensions fail explicitly. Negative values mean volume interior; samples are not exact signed distances.

Traversal is a bounded sampled strip/deck check with actual mesh surfaces and corridor triangle tests, using a 0.1 m floor tolerance in the fixture. Editable-region compliance remains sampled. Custom/reordered conditioned surfaces are outside this milestone's canonical-mesher contract. These checks do not prove continuous navigation, collision behavior or real-world structural safety.

Support synthesis, constrained MAP-Elites extensions, arbitrary mesh repair and curved/sloped traversal remain separate milestones. Existing unconditioned generation, archive descriptor versions and keep/reject behavior remain covered by regression checks.
