# Progen

A pure-Go framework for composing procedural asset generators through behavioral interfaces. The MVP grows organic specimens from an eigenmode field, turns their skeletons into porous solids, and exports real 3D meshes. A local gallery supports feedback, parameter exploration, and MAP-Elites search.

## Start

Requires Go 1.26 or newer. The compiled browser assets are included: Node.js, network access, a GPU compute backend, and a database are **not** required to run the application.

```sh
go run ./cmd/progen serve
```

Open **http://127.0.0.1:8080**. Generate a batch, inspect a specimen, favorite it, and select **Explore variations**. Variations retain the specimen's generation seed and mutate its parameters. Browser rendering uses WebGL; headless generation and export do not.

For a standalone binary:

```sh
go build -o progen ./cmd/progen
./progen serve --workspace .progen --addr 127.0.0.1:8080
```

The default batch contains 16 candidates at 64³ samples. The first candidate uses the displayed settings; subsequent candidates are random parameter-space samples. Use a count of 1 for an exact experiment. A 32³ grid makes exploratory batches cheaper; 96³ or 128³ resolves smaller features. Changing resolution changes the sampled geometry and its measurements.

## CLI

```sh
# Inspect contracts and validate a recipe.
go run ./cmd/progen operators
go run ./cmd/progen validate --recipe examples/organic.json

# Generate deterministic candidates. JSON goes to stdout; progress goes to stderr.
go run ./cmd/progen generate --recipe examples/organic.json --count 4 --search-seed 12 --workers 2

# Use a candidate ID from generation output or the gallery.
go run ./cmd/progen vary --parent CANDIDATE_ID --count 8 --scale 0.15 --search-seed 12
go run ./cmd/progen search --recipe examples/organic.json --count 64 --search-seed 12
go run ./cmd/progen export --id CANDIDATE_ID --out specimen.zip

# Print a normalized default recipe, including parameter defaults.
go run ./cmd/progen recipe
```

Flags follow the command. `--workspace` defaults to `.progen`. Use one writing process per workspace; the server supports queued jobs and concurrent batch workers internally. The local server has no user accounts and is intended for loopback use.

## Framework and interfaces

The root `progen` package is reusable independently of the CLI and browser. `FieldGenerator`, `GraphGrowth`, `Solidifier`, `SurfaceModifier`, and `Mesher` describe algorithm behavior. `ScalarField`, `VectorField`, and `Volume` describe sampling behavior; concrete grids implement them. A volume has negative interior values, but its values are **not necessarily signed distances**.

```go
engine, err := progen.NewEngine(".progen")
if err != nil {
    return err
}
candidate, err := engine.Evaluate(ctx, progen.DefaultRecipe(), "", nil)
if err != nil {
    return err
}
graph, err := engine.Artifact(candidate, "graph")
// graph.Graph retains positions, radii, parent links, and growth generations.
```

Each registered operator has named typed ports, semantic labels, parameter bounds/defaults, a version, and computational requirements. JSON factories are the dynamic boundary; implementations use typed Go inputs. The evaluator never dispatches on algorithm names.

To add a growth implementation:

1. Implement `GraphGrowth.Grow(context.Context, VectorField, uint64) (*Graph, error)`.
2. Register a `Descriptor` with a `vector/guidance` input and a `graph/skeleton` output. Its `Factory` decodes validated numeric parameters into your concrete configuration.
3. Return an `OperatorFunc` that calls your typed implementation and wraps the resulting graph in an `Artifact`.
4. Change only the growth node's operator ID/version in the recipe. The surrounding field, solidifier, modifier, and mesher remain compatible.

`TestOperatorSubstitution` is a complete executable example. Configure/register operators before concurrent evaluation. Artifacts are immutable after an operator returns: downstream operators must produce new outputs rather than modify inputs. Custom factories must honor context cancellation and declared budgets.

`CandidateEvaluator` replaces validity and quality measurement; `DescriptorSpace` replaces archive coordinates, which must be in `[0,1]`. The built-in evaluator expects recipe outputs named `mesh`, `graph`, `base`, and `volume`. Other outputs are preserved and exported as well. `ArtifactCodec` handles persistent output encoding; change `CodecVersion` when changing the encoding. The MVP supports the built-in artifact families, numeric parameter schemas, and bounded Euclidean box domains; arbitrary representations/domains require extending those contracts.

## The example recipe

| Operator | Contract | Implementation |
| --- | --- | --- |
| `eigenmode` | Domain → scalar potential | Analytic rectangular-box Dirichlet eigenfunction, ordered by eigenvalue with stable ties |
| `gradient` | Scalar potential → guidance vector field | Explicit finite-difference representation adapter |
| `branching` | Guidance → spatial graph | Space colonization with attraction points, field bias, exclusions, and bounded growth |
| `solidify` | Graph → implicit solid | Varying-radius capsule union sampled near segments |
| `porous` | Solid → modified solid | Periodic gyroid-like carving; amount zero preserves the input |
| `mesh` | Solid → triangle mesh | Marching tetrahedra with shared intersection vertices and outward normals |

The domain specifies bounds, resolution, right-handed Y-up coordinates, meter units, Dirichlet field boundaries, and optional axis-aligned exclusion boxes. Exclusions affect growth and solidification; eigenfunctions remain those of the full rectangular box, not a perforated-domain eigensolve. Field gradients use one-sided differences at boundaries.

Recipes are DAGs with explicit connections and representation adapters. Compilation rejects cycles, duplicate IDs, missing/unknown ports, incompatible kind/semantic pairs, unsupported versions, and invalid parameters. Output names and node IDs use letters, numbers, underscores, and hyphens, up to 64 characters. All nodes share one domain; runtime output checks reject incompatible domains.

Limits: 64 recipe nodes, resolution 8–128 per axis, 1–256 candidates per job, 1–4 batch workers, 8 queued/running server jobs, and at most two million mesh triangles. Growth exposes attraction, iteration, and vertex budgets with registry-enforced limits. These are experimental assets: fine branches can disappear on coarse grids, pore carving can disconnect solids, and exporting does not certify manifoldness or gameplay suitability.

## Search and feedback

MAP-Elites keeps one elite per cell in a 16×16 archive. The built-in axes are measured branch-point fraction (vertices with more than one child / all vertices) and removed-volume fraction (occupied samples removed / occupied base samples). Connected-solid quality is the fraction of occupied samples in the largest **six-neighbor grid component**, not a mesh-topology guarantee.

Validity requires finite nonempty geometry, valid indices and graph links, domain bounds, and sampled exclusion compliance. Invalid candidates remain inspectable but cannot enter the archive or be exported as valid GLB assets. Feedback does not override validity.

Within a cell, a favorite outranks a neutral candidate; then connected-solid quality wins; exact ties use a stable candidate ID. Rejected candidates remain on disk but are excluded from archive membership and parent selection. Changing feedback rebuilds persisted archives.

Search uses uniform elite selection, Gaussian mutation in normalized parameter space, integer rounding, clamping, and a 10% random-restart probability. An empty archive is initialized by random sampling. Archive families share topology, domain, generation seed, operator versions, and immutable settings; mutable parameters vary within the family. The gallery shows the selected candidate's family, or the default family before selection.

`--search-seed` controls parameter sampling independently of the recipe's generation seed. The public `Seed` type accepts unsigned decimal JSON numbers or strings and emits strings, preserving all 64 bits in browsers. Replaying search requires the same starting archive and feedback. Live feedback intentionally affects subsequent selections. Each run records the request, initial archive, search seed, results, and status; each candidate records its actual evaluated parameters and parent.

## Storage and reproducibility

Workspace contents:

- `candidates/<id>/candidate.json` and `asset.glb`: candidate metadata and baked geometry.
- `cache/<key>.json.gz`: checksummed intermediate fields, vector grids, graphs, volumes, and meshes. These also serve as durable source artifacts; keep them when retaining candidates.
- `archives/`: persisted elite maps, recomputed from candidate records.
- `runs/`: CLI/library generation and search manifests.
- `jobs/`: server job requests and terminal status. An interrupted server job is marked on restart; rerunning reuses completed work and valid caches.

Writes use a temporary file and atomic rename. Corrupt cache entries are recomputed on evaluation. Cancellation preserves completed candidates. Batch merge order is independent of worker scheduling; every node has an independent random stream derived from its stable ID and the recipe seed.

Candidate identity includes the normalized recipe and executable build fingerprint. Node cache keys additionally cover parameters, node seed, domain/constraints, upstream hashes, operator version, and codec version. Timestamps, timings, and cache-hit flags do not affect deterministic identity. Rebuilding changed code or embedded UI deliberately invalidates caches. Bitwise equality is verified for the same executable/platform, not promised across toolchains or CPU architectures.

The ZIP export contains `asset.glb`, `recipe.json`, `candidate.json`, and all selected intermediate artifacts under `artifacts/`. GLB uses indexed geometry, normals, and a simple material. It can be imported by glTF-capable tools; collision proxies, navigation, LODs, and direct Godot integration are outside this MVP.

## HTTP API

| Method | Route | Purpose |
| --- | --- | --- |
| GET | `/api/operators`, `/api/recipe` | Contracts and normalized default recipe |
| POST | `/api/validate` | Validate/normalize a recipe body |
| POST | `/api/jobs` | Submit `{kind, recipe?, parent?, count, scale, search_seed, workers}` |
| GET | `/api/jobs`, `/api/jobs/{id}` | Job state and progress |
| DELETE | `/api/jobs/{id}` | Cancel an active job |
| GET | `/api/candidates`, `/api/candidates/{id}` | Candidate records |
| POST | `/api/candidates/{id}/feedback` | Set `{state: "neutral" | "favorite" | "rejected"}` |
| GET | `/api/candidates/{id}/artifacts/{name}` | Read a typed artifact |
| GET | `/api/candidates/{id}/asset.glb`, `/api/candidates/{id}/export.zip` | Download output |
| GET | `/api/archive?candidate={id}` | Inspect a recipe-family archive |

`kind` is `generate`, `vary`, or `search`. Variation uses the parent's recipe. Requests are bounded, reject unknown JSON fields, and reject cross-origin mutations. Long operations run outside request handlers; one job runs at a time, with optional parallel workers within a generation batch.

## Development and validation

Only browser development/testing needs Node.js and npm. Browser source is `web/app.js`; bundled modules and third-party license notices are embedded from `web/dist`. After changing browser code, rebuild the bundle before rebuilding Go.

```sh
npm ci
npm run build
go test -race ./...
go vet ./...
go build -o /tmp/progen-mvp ./cmd/progen
npm run test:browser
npm run test:glb -- .progen
go test -run '^$' -bench BenchmarkGeneration -benchtime=1x -benchmem
```

The browser test uses `/usr/bin/chromium`, creates its own temporary workspace, and starts/stops its own server. Override `PROGEN_BIN`, `CHROMIUM_PATH`, or `PROGEN_TEST_PORT` when needed. It exercises generation, preview, skeleton display, feedback, reload, variation, search, exports, mobile layout, and server restart, and validates the downloaded GLB with Khronos tooling. Desktop/mobile screenshots and exported artifacts remain in the reported temporary directory.

Go tests cover numerical samples, zero-porosity identity, exclusions, capsule meshing, a closed-mesh fixture, operator replacement, invalid recipes, cache corruption/invalidation, worker determinism, mutation bounds, archive ranking, cancellation, exports, API behavior, and persistence.

The initial 64³ example produced approximately 105,000 triangles; the cold-generation benchmark measured 1.33 seconds and 597 MB of cumulative Go allocations (not peak resident memory) on the development machine, including persistence. A 16-candidate 64³ batch with two workers completed in 11.93 seconds with 340 MiB peak resident memory; all 16 candidates passed the built-in validity checks. These are observed examples, not performance guarantees; use the included benchmark for your machine and configuration.

## References and deferred work

The architecture follows the [shared framework proposal](https://chatgpt.com/s/t_6ae05b5877488191b6ce00434ed792f9). Growth adapts [space colonization](https://algorithmicbotany.org/papers/colonization.egwnp2007.pdf); exploration implements [basic MAP-Elites](https://arxiv.org/abs/1504.04909). GLB validation uses the [Khronos validator](https://github.com/KhronosGroup/glTF-Validator). The supplied survey and atlas remain reference material; the atlas images are not used as generated output.

Deferred: coupled simulations, particle/cell-complex operators, GPU generation, covariance adaptation, learned preferences, evolving recipe topology, production collision generation, and engine-specific integration.
