# A0477A mesh-index investigation

The uploaded cache for candidate `a0477adbcccf36eba91b2e9e782c00859f0f72fb12cd0b895adf81016040372f` contains 37,562 vertices and 225,660 indices (75,220 triangles).

Comparing it with a fresh evaluation of its exact recipe:

- Every position and normal matches exactly.
- 118 indices differ; every difference sets extra high bits. For example, index 63,525 contains `0x600026b8` instead of `0x000026b8` (9912).
- All changed indices are at offsets congruent to 1 modulo 4, between offsets 63,525 and 71,541 inclusive. Their lower 24 bits match the clean indices.
- The cache envelope checksum is valid, and its artifact hash matches the candidate manifest (`32f13aaa3c161d04ac2906e6d06cd80e3a9aa4f901b6c775369063a2abad36fe`). The damage was present before those hashes were computed.
- The manifest records `cached: false` for the mesh node. Reusing a damaged mesh cache does not explain this original occurrence.
- Regeneration with Go 1.26.0 and Go 1.27.1, including `GOEXPERIMENT=nodwarf5`, produces the same valid mesh hash (`830548a311be366354a93ffa3aee11a3df824b6e4e86dc2d60d697620e72a3d8`). Its GLB validates without errors or warnings.

This establishes index-buffer corruption, but does not establish its underlying cause. A checksum cannot detect damage that precedes hashing. No compiler, runtime, hardware, or application-memory defect has been reproduced or identified.

`TestSpecimenA0477AFreshAndCachedMesh` replays the 118 damaged indices from `testdata/a0477a-corrupt-indices.json`, verifies rejection and deterministic cache regeneration, and then verifies clean cache reuse. Bounds validation remains strict; high bits are never stripped to make damaged indices appear valid.

Existing invalid manifests do not repair themselves when opened in the gallery. Regenerate the recipe after rebuilding, or purge the old generations with the studio's purge control. From the repository root, with the desired workspace path:

```sh
go run ./cmd/progen generate --recipe testdata/a0477a-recipe.json --count 1 --workspace .progen
```
