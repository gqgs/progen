package progen

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type DescriptorSpace interface {
	Coordinates(Metrics) (float64, float64)
}
type MorphologySpace struct{}

// Coordinates scales the raw B/N tree metric from [0, .5) to [0, 1).
// For N vertices, B <= floor((N-1)/2). This is a fixed scale, not a
// normalization by the maximum for each candidate's particular vertex count.
func (MorphologySpace) Coordinates(m Metrics) (float64, float64) {
	return 2 * m.BranchFraction, m.Porosity
}

// LegacyMorphologySpace reproduces the original unscaled coordinates for
// comparisons and replay of runs whose initial_archive lacks a descriptor_version.
// ImplicitMorphologySpace uses voxel measurements and is intentionally
// versioned separately from tree-branch descriptors.
type ImplicitMorphologySpace struct{}

func (ImplicitMorphologySpace) Coordinates(m Metrics) (float64, float64) {
	return m.SolidFraction, m.SurfaceFraction
}
func (ImplicitMorphologySpace) Definition() DescriptorDefinition {
	return DescriptorDefinition{
		Version: "implicit-morphology-v1",
		Axes:    [2]string{"occupied-volume fraction", "exposed voxel-face fraction"},
		Ranges:  [2]DescriptorRange{{0, 1, false}, {0, 1, false}},
	}
}

type LegacyMorphologySpace struct{}

func (LegacyMorphologySpace) Coordinates(m Metrics) (float64, float64) {
	return m.BranchFraction, m.Porosity
}

type DescriptorRange struct {
	Min          float64 `json:"min"`
	Max          float64 `json:"max"`
	MaxExclusive bool    `json:"max_exclusive"`
}

// DescriptorDefinition describes normalized coordinates, not raw Metrics.
// Custom spaces may implement DescriptorMetadata without changing DescriptorSpace.
// Change Version whenever coordinate meaning changes.
type DescriptorDefinition struct {
	Version string
	Axes    [2]string
	Ranges  [2]DescriptorRange
}
type DescriptorMetadata interface {
	Definition() DescriptorDefinition
}

func (MorphologySpace) Definition() DescriptorDefinition {
	return DescriptorDefinition{
		Version: "morphology-v2",
		Axes:    [2]string{"scaled branch-point fraction (2 × B/N)", "removed-volume fraction"},
		Ranges:  [2]DescriptorRange{{0, 1, true}, {0, 1, false}},
	}
}
func (LegacyMorphologySpace) Definition() DescriptorDefinition {
	return DescriptorDefinition{
		Version: "morphology-v1",
		Axes:    [2]string{"branch-point fraction (B/N)", "removed-volume fraction"},
		Ranges:  [2]DescriptorRange{{0, .5, true}, {0, 1, false}},
	}
}

type Archive struct {
	ID                string             `json:"id"`
	Bins              int                `json:"bins"`
	Axes              [2]string          `json:"axes"`
	Cells             map[string]string  `json:"cells"`
	DescriptorVersion string             `json:"descriptor_version"`
	Ranges            [2]DescriptorRange `json:"ranges"`
}

// Compatible checks cell-coordinate semantics. Missing versions are legacy v1;
// their cells must never be interpreted as v2. Unversioned custom spaces cannot
// promise compatibility, even with one another.
func (a Archive) Compatible(b Archive) bool {
	return a.DescriptorVersion != "" && a.DescriptorVersion != "custom-unversioned" &&
		a.DescriptorVersion == b.DescriptorVersion && a.Bins == b.Bins &&
		a.Axes == b.Axes && a.Ranges == b.Ranges
}

// descriptorBin uses half-open bins, with the inclusive endpoint 1 in the last
// bin. Reject bad coordinates rather than silently clamping invalid metrics.
func descriptorBin(x float64, bins int) (int, bool) {
	if bins < 1 || !finite(x) || x < 0 || x > 1 {
		return 0, false
	}
	return min(bins-1, int(x*float64(bins))), true
}

func (e *Engine) family(r Recipe) string {
	p, _, err := e.Registry.Compile(r)
	if err != nil {
		return ""
	}
	for i, n := range p.Nodes {
		for k, s := range e.Registry.entries[n.Operator].desc.Parameters {
			if s.Mutable {
				p.Nodes[i].Params[k] = s.Default
			}
		}
	}
	return hash(p)
}
func better(a, b Candidate) bool {
	if a.Feedback != b.Feedback {
		if a.Feedback == "favorite" {
			return true
		}
		if b.Feedback == "favorite" {
			return false
		}
	}
	if a.Metrics.ConnectedFraction != b.Metrics.ConnectedFraction {
		return a.Metrics.ConnectedFraction > b.Metrics.ConnectedFraction
	}
	return a.ID < b.ID
}
func (e *Engine) descriptorFor(r Recipe) DescriptorSpace {
	if e.Descriptors != nil {
		return e.Descriptors
	}
	if _, hasGraph := r.Outputs["graph"]; hasGraph {
		return MorphologySpace{}
	}
	return ImplicitMorphologySpace{}
}
func (e *Engine) Archive(r Recipe) (Archive, error) {
	all, err := e.List()
	if err != nil {
		return Archive{}, err
	}
	id := e.family(r)
	elites := map[string]Candidate{}
	space := e.descriptorFor(r)
	def := DescriptorDefinition{
		Version: "custom-unversioned",
		Axes:    [2]string{"custom descriptor x", "custom descriptor y"},
		Ranges:  [2]DescriptorRange{{0, 1, false}, {0, 1, false}},
	}
	if metadata, ok := space.(DescriptorMetadata); ok {
		def = metadata.Definition()
		if def.Version == "" {
			return Archive{}, fmt.Errorf("descriptor version must not be empty")
		}
	}
	a := Archive{ID: id, Bins: 16, Axes: def.Axes, Cells: map[string]string{},
		DescriptorVersion: def.Version, Ranges: def.Ranges}
	for _, c := range all {
		if !c.Valid || c.Feedback == "rejected" || e.family(c.Recipe) != id {
			continue
		}
		x, y := space.Coordinates(c.Metrics)
		bx, okX := descriptorBin(x, a.Bins)
		by, okY := descriptorBin(y, a.Bins)
		inRange := func(v float64, r DescriptorRange) bool {
			return v >= r.Min && (v < r.Max || (!r.MaxExclusive && v == r.Max))
		}
		if !okX || !okY || !inRange(x, a.Ranges[0]) || !inRange(y, a.Ranges[1]) {
			continue
		}
		key := fmt.Sprintf("%d,%d", bx, by)
		if old, ok := elites[key]; !ok || better(c, old) {
			elites[key] = c
			a.Cells[key] = c.ID
		}
	}
	return a, nil
}

// RebuildArchives replaces derived cell maps using existing candidate records
// and the currently configured descriptors. Configure custom registries/spaces
// first. Candidate manifests, artifacts, feedback and historical runs are never
// rewritten. Each archive is atomically replaced; a failed rebuild can be retried.
func (e *Engine) RebuildArchives() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.rebuildArchives()
}

func (e *Engine) rebuildArchives() error {
	all, err := e.List()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, c := range all {
		id := e.family(c.Recipe)
		if seen[id] {
			continue
		}
		seen[id] = true
		a, err := e.Archive(c.Recipe)
		if err != nil {
			return err
		}
		if err = writeJSON(filepath.Join(e.Workspace, "archives", id+".json"), a); err != nil {
			return err
		}
	}
	return nil
}
func (e *Engine) PersistArchive(r Recipe) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, err := e.Archive(r)
	if err != nil {
		return err
	}
	return writeJSON(filepath.Join(e.Workspace, "archives", a.ID+".json"), a)
}
func (e *Engine) mutate(r Recipe, rng *rand.Rand, scale float64, random bool) Recipe {
	p := cloneRecipe(r)
	for i, n := range p.Nodes {
		keys := []string{}
		for key := range e.Registry.entries[n.Operator].desc.Parameters {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			s := e.Registry.parameterFor(p, n, key)
			if !s.Mutable {
				continue
			}
			v := n.Params[key]
			if random {
				v = s.Min + rng.Float64()*(s.Max-s.Min)
			} else {
				v += rng.NormFloat64() * scale * (s.Max - s.Min)
			}
			v = math.Max(s.Min, math.Min(s.Max, v))
			if s.Type == "integer" {
				v = math.Round(v)
			}
			p.Nodes[i].Params[key] = v
		}
	}
	return p
}

type RunRequest struct {
	Kind       string  `json:"kind"`
	Recipe     *Recipe `json:"recipe,omitempty"`
	Parent     string  `json:"parent,omitempty"`
	Count      int     `json:"count"`
	Scale      float64 `json:"scale"`
	SearchSeed Seed    `json:"search_seed"`
	Workers    int     `json:"workers"`
}

func (e *Engine) Prepare(req RunRequest) (RunRequest, Recipe, error) {
	if req.Kind == "" {
		req.Kind = "generate"
	}
	if req.Kind != "generate" && req.Kind != "vary" && req.Kind != "search" {
		return req, Recipe{}, fmt.Errorf("unknown job kind")
	}
	if req.Count == 0 {
		req.Count = 16
		if req.Kind == "search" {
			req.Count = 64
		}
	}
	if req.Count < 1 || req.Count > 256 {
		return req, Recipe{}, fmt.Errorf("count must be 1..256")
	}
	if req.Workers == 0 {
		req.Workers = 1
	}
	if req.Workers < 1 || req.Workers > 4 {
		return req, Recipe{}, fmt.Errorf("workers must be 1..4")
	}
	if req.Scale == 0 {
		req.Scale = .15
	}
	if !finite(req.Scale) || req.Scale <= 0 || req.Scale > 1 {
		return req, Recipe{}, fmt.Errorf("scale must be (0,1]")
	}
	r := DefaultRecipe()
	if req.Recipe != nil {
		r = *req.Recipe
	}
	if req.Kind == "vary" {
		c, err := e.Get(req.Parent)
		if err != nil {
			return req, r, err
		}
		if !c.Valid || c.Feedback == "rejected" {
			return req, r, fmt.Errorf("choose a valid, non-rejected parent")
		}
		r = c.Recipe
	}
	p, _, err := e.Registry.Compile(r)
	if err == nil && p.Source != nil {
		if req.Kind == "search" {
			return req, p, fmt.Errorf("conditioned MAP-Elites is a separate milestone; use generate or vary")
		}
		if req.Count > p.Source.Spec.Budget.MaxCandidates {
			return req, p, fmt.Errorf("count exceeds sidecar max_candidates (%d)", p.Source.Spec.Budget.MaxCandidates)
		}
		_, _, err = e.sourceBytes(p.Source)
	}
	return req, p, err
}
func (e *Engine) run(ctx context.Context, request RunRequest, progress func(int, int, string), onSkip func(int, Recipe, error)) ([]*Candidate, error) {
	req, base, err := e.Prepare(request)
	if err != nil {
		return nil, err
	}
	rng := rand.New(rand.NewPCG(uint64(req.SearchSeed), uint64(req.SearchSeed)^0xda3e39cb94b95bdb))
	if req.Kind == "search" {
		out := []*Candidate{}
		for i := 0; i < req.Count; i++ {
			if err := ctx.Err(); err != nil {
				return out, err
			}
			a, err := e.Archive(base)
			if err != nil {
				return out, err
			}
			r := base
			parent := ""
			if len(a.Cells) > 0 && rng.Float64() >= .1 {
				ids := []string{}
				for _, id := range a.Cells {
					ids = append(ids, id)
				}
				sort.Strings(ids)
				parent = ids[rng.IntN(len(ids))]
				c, err := e.Get(parent)
				if err != nil {
					return out, err
				}
				r = e.mutate(c.Recipe, rng, req.Scale, false)
			} else {
				r = e.mutate(base, rng, req.Scale, true)
			}
			c, err := e.Evaluate(ctx, r, parent, func(node string) {
				if progress != nil {
					progress(i, req.Count, node)
				}
			})
			if err != nil {
				if errors.Is(err, ErrMeshTriangleLimit) || errors.Is(err, ErrMeshPrecision) {
					if onSkip != nil {
						onSkip(i, r, err)
					}
					if progress != nil {
						progress(i+1, req.Count, "skipped")
					}
					continue
				}
				return out, err
			}
			out = append(out, c)
			if err = e.PersistArchive(base); err != nil {
				return out, err
			}
			if progress != nil {
				progress(i+1, req.Count, "completed")
			}
		}
		return out, nil
	}
	recipes := make([]Recipe, req.Count)
	for i := range recipes {
		if i == 0 && req.Kind == "generate" {
			recipes[i] = cloneRecipe(base)
		} else {
			recipes[i] = e.mutate(base, rng, req.Scale, req.Kind == "generate")
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := make([]*Candidate, req.Count)
	jobs := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	completed := 0
	for w := 0; w < req.Workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				c, err := e.Evaluate(ctx, recipes[i], req.Parent, func(node string) {
					mu.Lock()
					defer mu.Unlock()
					if progress != nil {
						progress(completed, req.Count, node)
					}
				})
				mu.Lock()
				if err != nil && !(errors.Is(err, ErrMeshTriangleLimit) || errors.Is(err, ErrMeshPrecision)) {
					if first == nil {
						first = err
						cancel()
					}
				} else {
					stage := "completed"
					if err != nil {
						stage = "skipped"
						if onSkip != nil {
							onSkip(i, recipes[i], err)
						}
					} else {
						out[i] = c
					}
					completed++
					if progress != nil {
						progress(completed, req.Count, stage)
					}
				}
				mu.Unlock()
			}
		}()
	}
send:
	for i := range recipes {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break send
		}
	}
	close(jobs)
	wg.Wait()
	if first != nil {
		return out, first
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if err = e.PersistArchive(base); err != nil {
		return out, err
	}
	// Return surviving candidates in request order. A rejected specimen has no
	// mesh to preview, and must not appear as a nil entry in the CLI output.
	survivors := make([]*Candidate, 0, len(out))
	for _, c := range out {
		if c != nil {
			survivors = append(survivors, c)
		}
	}
	return survivors, nil
}

// CandidateFailure retains enough information to reproduce an oversized
// specimen without polluting the candidate atlas with an unusable GLB.
type CandidateFailure struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
	Recipe Recipe `json:"recipe"`
}

// RunRecord records parameter-search randomness separately from generation seeds.
type RunRecord struct {
	ID             string             `json:"id"`
	Request        RunRequest         `json:"request"`
	InitialArchive Archive            `json:"initial_archive"`
	Status         string             `json:"status"`
	Created        time.Time          `json:"created"`
	Finished       time.Time          `json:"finished,omitempty"`
	Candidates     []string           `json:"candidates"`
	Failures       []CandidateFailure `json:"failures,omitempty"`
	Error          string             `json:"error,omitempty"`
}

// Run generates an ordered batch or runs sequential MAP-Elites. Callbacks must
// return promptly; registration and engine configuration must finish before Run.
func (e *Engine) Run(ctx context.Context, request RunRequest, progress func(int, int, string)) ([]*Candidate, error) {
	results, _, err := e.RunWithFailures(ctx, request, progress)
	return results, err
}

// RunWithFailures reports candidate-local budget rejections separately from
// fatal storage/configuration errors and explicit cancellation.
func (e *Engine) RunWithFailures(ctx context.Context, request RunRequest, progress func(int, int, string)) (results []*Candidate, failures []CandidateFailure, runErr error) {
	req, recipe, err := e.Prepare(request)
	if err != nil {
		return nil, nil, err
	}
	req.Recipe = &recipe
	archive, err := e.Archive(recipe)
	if err != nil {
		return nil, nil, err
	}
	record := RunRecord{Request: req, InitialArchive: archive, Status: "running", Created: time.Now().UTC(), Candidates: []string{}}
	record.ID = hash(record)
	path := filepath.Join(e.Workspace, "runs", record.ID+".json")
	if err = writeJSON(path, record); err != nil {
		return nil, nil, err
	}
	defer func() {
		record.Finished = time.Now().UTC()
		record.Failures = failures
		record.Status = "completed"
		if runErr != nil {
			record.Status = "failed"
			record.Error = runErr.Error()
			if errors.Is(runErr, context.Canceled) {
				record.Status = "cancelled"
			}
		}
		for _, c := range results {
			if c != nil {
				record.Candidates = append(record.Candidates, c.ID)
			}
		}
		if err := writeJSON(path, record); err != nil && runErr == nil {
			runErr = err
		}
	}()
	results, runErr = e.run(ctx, req, progress, func(index int, recipe Recipe, err error) {
		failures = append(failures, CandidateFailure{Index: index, Reason: err.Error(), Recipe: recipe})
	})
	// Workers may finish in a different order; manifest failures remain stable.
	sort.Slice(failures, func(i, j int) bool { return failures[i].Index < failures[j].Index })
	return results, failures, runErr
}
