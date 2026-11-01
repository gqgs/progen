# Canonical mesh repair at GLB precision

Bridge recipe seed `14644082841431334434`, resolution 64, previously produced
107,071 faces. Five nonzero-area float64 faces collapsed when their positions
were exported as float32. Six nearby vertices became the same exported point.
The exact recipe is retained in `testdata/bridge-quantized-collapse.json`.

The canonical tetrahedra mesher now detects repeated positions within a face
at GLB precision. Only in that case it:

- welds exactly equal float32 positions in deterministic encounter order;
- removes faces with repeated welded vertices, without raising the area cutoff;
- rejects remaining distinct-position degeneracies or reversed face normals;
- requires two opposite uses of every edge, no duplicate faces, and one cyclic
  link per vertex (including rejection of pinched, otherwise closed shells);
- removes unused vertices and recomputes area-weighted unit normals.

The repaired positions themselves are float32-exact. Generation and independent
canonical regeneration use this same mesher, so admission and export agree.
The existing canonical identity, required solid, anchor, forbidden clearance,
editable-region, and traversal checks are unchanged and operate on the actual
export positions. A topology failure is a candidate-local `ErrMeshPrecision`
rejection, recorded by batch generation/search rather than aborting other
candidates. No arbitrary face deletion or validation relaxation is performed.

Meshes without collapsed faces retain their existing representation. A
distinct-position sliver alone is not repaired; conditioned export-precision
validation continues to reject it. Repair is deliberately conservative: an
open or non-manifold result is rejected rather than attempting a general
remesh. The topology checks do not claim to prove absence of all geometric
self-intersections; the existing protected-region tests remain necessary.

## Identity and replay

The engine build identity now includes `mesh-f32-weld-1`, independently of the
executable checksum. Build identity participates in candidate and node cache
keys, so previous artifacts are not silently reused for newly evaluated
recipes. Recipe seed, source GLB hash, sidecar hash, and operator versions are
unchanged. No saved candidates or archives are rewritten automatically;
re-evaluate an old recipe to obtain the new candidate and geometry hashes.
The cache serialization format remains `json-2`.

## Verification

`TestBridgeQuantizedCollapseRegression` checks two fresh engines and their
cached replays, canonical regeneration, full constraint admission, and actual
exported GLB coordinates, indices, unit normals, topology, and constraints.
The result has 53,355 vertices and 107,066 triangles. Removing a retained face
still fails canonical admission.

`TestPrecisionWeld` verifies deterministic, non-mutating, idempotent repair.
Synthetic negative fixtures cover boundary edges, duplicate faces, inconsistent
winding, disconnected vertex links, and distinct-position slivers.
Batch tests cover generation and search precision failures.

Run `go test ./...` and `go vet ./...`. To additionally validate the actual
regression export with the glTF validator:

```sh
PROGEN_PRECISION_GLB=/tmp/progen-repaired.glb go test -run TestBridgeQuantizedCollapseRegression -count=1
npm ci
npm run test:glb -- /tmp/progen-repaired.glb
```
