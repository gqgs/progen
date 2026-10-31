package progen

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"
)

type ArtifactCodec interface {
	Encode(Outputs) ([]byte, error)
	Decode([]byte) (Outputs, error)
}
type JSONCodec struct{}

func (JSONCodec) Encode(o Outputs) ([]byte, error) { return json.Marshal(o) }
func (JSONCodec) Decode(b []byte) (Outputs, error) {
	var o Outputs
	e := json.Unmarshal(b, &o)
	return o, e
}

type ArtifactRef struct {
	Key  string `json:"key"`
	Port string `json:"port"`
	Hash string `json:"hash"`
}
type NodeRecord struct {
	ID           string `json:"id"`
	Operator     string `json:"operator"`
	Version      string `json:"version"`
	Seed         Seed   `json:"seed"`
	CacheKey     string `json:"cache_key"`
	Cached       bool   `json:"cached"`
	Milliseconds int64  `json:"milliseconds"`
}
type Metrics struct {
	// BranchFraction is B/N, where B counts graph vertices with >1 child.
	// For a valid rooted tree it is in [0, .5); descriptor scaling is separate.
	BranchFraction    float64 `json:"branch_fraction"`
	Porosity          float64 `json:"porosity"`
	ConnectedFraction float64 `json:"connected_fraction"`
	Coverage          float64 `json:"coverage"`
	Vertices          int     `json:"vertices"`
	Triangles         int     `json:"triangles"`
}
type Candidate struct {
	ID           string                 `json:"id"`
	Parent       string                 `json:"parent,omitempty"`
	Recipe       Recipe                 `json:"recipe"`
	Engine       string                 `json:"engine"`
	Build        string                 `json:"build"`
	Artifacts    map[string]ArtifactRef `json:"artifacts"`
	Nodes        []NodeRecord           `json:"nodes"`
	Metrics      Metrics                `json:"metrics"`
	Valid        bool                   `json:"valid"`
	Problems     []string               `json:"problems"`
	Feedback     string                 `json:"feedback"`
	Created      time.Time              `json:"created"`
	Milliseconds int64                  `json:"milliseconds"`
}
type CandidateEvaluator interface {
	Evaluate(context.Context, Recipe, map[string]Artifact) (Metrics, []string, error)
}
type DefaultEvaluator struct{}

func (DefaultEvaluator) Evaluate(ctx context.Context, r Recipe, out map[string]Artifact) (Metrics, []string, error) {
	m := Metrics{}
	problems := []string{}
	mesh := out["mesh"].Mesh
	g := out["graph"].Graph
	v := out["volume"].Volume
	base := out["base"].Volume
	if mesh == nil || g == nil || v == nil || base == nil {
		return m, []string{"candidate recipes must expose mesh, graph, volume and base outputs"}, nil
	}
	if err := ValidateMesh(mesh, r.Domain); err != nil {
		problems = append(problems, err.Error())
	}
	m.Vertices = len(mesh.Positions)
	m.Triangles = len(mesh.Indices) / 3
	children := make([]int, len(g.Vertices))
	for i, node := range g.Vertices {
		if !node.Position.Finite() || !finite(node.Radius) || node.Radius <= 0 || node.Parent >= i || node.Parent < -1 || (i > 0 && node.Parent < 0) {
			problems = append(problems, "invalid graph structure")
			break
		}
		if node.Parent >= 0 {
			children[node.Parent]++
		}
	}
	branch := 0
	for _, c := range children {
		if c > 1 {
			branch++
		}
	}
	m.BranchFraction = float64(branch) / float64(max(1, len(children)))
	m.Coverage = float64(g.Reached) / float64(max(1, g.AttractionCount))
	n := r.Domain.Resolution
	count, original := 0, 0
	for i, x := range v.Values {
		if i%8192 == 0 {
			if err := ctx.Err(); err != nil {
				return m, nil, err
			}
		}
		if x < 0 {
			count++
			p := r.Domain.Point(i%n, (i/n)%n, i/(n*n))
			if !r.Domain.Allowed(p) {
				problems = append(problems, "solid violates sampled exclusion")
				break
			}
		}
		if base.Values[i] < 0 {
			original++
		}
	}
	if count == 0 {
		problems = append(problems, "empty solid")
	}
	if original > 0 {
		m.Porosity = 1 - float64(count)/float64(original)
	}
	seen := make([]bool, len(v.Values))
	largest := 0
	queue := []int{}
	for i, x := range v.Values {
		if x >= 0 || seen[i] {
			continue
		}
		queue = append(queue[:0], i)
		seen[i] = true
		for head := 0; head < len(queue); head++ {
			if head%4096 == 0 {
				if err := ctx.Err(); err != nil {
					return m, nil, err
				}
			}
			q := queue[head]
			x, y, z := q%n, (q/n)%n, q/(n*n)
			for _, a := range [][3]int{{x - 1, y, z}, {x + 1, y, z}, {x, y - 1, z}, {x, y + 1, z}, {x, y, z - 1}, {x, y, z + 1}} {
				if a[0] < 0 || a[1] < 0 || a[2] < 0 || a[0] >= n || a[1] >= n || a[2] >= n {
					continue
				}
				j := a[0] + n*(a[1]+n*a[2])
				if !seen[j] && v.Values[j] < 0 {
					seen[j] = true
					queue = append(queue, j)
				}
			}
		}
		largest = max(largest, len(queue))
	}
	if count > 0 {
		m.ConnectedFraction = float64(largest) / float64(count)
	}
	return m, problems, nil
}

type Engine struct {
	Registry     *Registry
	Workspace    string
	Codec        ArtifactCodec
	CodecVersion string
	Evaluator    CandidateEvaluator
	Descriptors  DescriptorSpace
	Build        string
	mu           sync.Mutex
}

func NewEngine(workspace string) (*Engine, error) {
	for _, d := range []string{"cache", "candidates", "jobs"} {
		if err := os.MkdirAll(filepath.Join(workspace, d), 0755); err != nil {
			return nil, err
		}
	}
	build := EngineVersion + "/" + runtime.Version()
	if exe, err := os.Executable(); err == nil {
		if f, err := os.Open(exe); err == nil {
			h := sha256.New()
			io.Copy(h, f)
			f.Close()
			build += "/" + hex.EncodeToString(h.Sum(nil))
		}
	}
	return &Engine{Registry: Builtins(), Workspace: workspace, Codec: JSONCodec{}, CodecVersion: "json-2", Evaluator: DefaultEvaluator{}, Build: build}, nil
}
func atomicWrite(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".pending-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
func writeJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return atomicWrite(path, b)
}
func safeID(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, e := hex.DecodeString(s)
	return e == nil
}

type cacheEnvelope struct {
	Key    string `json:"key"`
	Digest string `json:"digest"`
	Data   []byte `json:"data"`
}

func (e *Engine) readCache(key string) (Outputs, error) {
	if !safeID(key) {
		return nil, fmt.Errorf("invalid cache key")
	}
	f, err := os.Open(filepath.Join(e.Workspace, "cache", key+".json.gz"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	b, err := io.ReadAll(io.LimitReader(gz, 512<<20))
	if err != nil {
		return nil, err
	}
	var env cacheEnvelope
	if err = json.Unmarshal(b, &env); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(env.Data)
	if env.Key != key || env.Digest != hex.EncodeToString(sum[:]) {
		return nil, fmt.Errorf("cache checksum mismatch")
	}
	return e.Codec.Decode(env.Data)
}
func (e *Engine) writeCache(key string, o Outputs) error {
	b, err := e.Codec.Encode(o)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	env := cacheEnvelope{key, hex.EncodeToString(sum[:]), b}
	raw, err := json.Marshal(env)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	gz, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if _, err = gz.Write(raw); err != nil {
		return err
	}
	if err = gz.Close(); err != nil {
		return err
	}
	return atomicWrite(filepath.Join(e.Workspace, "cache", key+".json.gz"), buf.Bytes())
}
func validateArtifact(a Artifact, d Domain) error {
	if space, ok := a.Space(); ok && !sameDomain(space, d) {
		return fmt.Errorf("output domain mismatch")
	}
	n := d.Resolution * d.Resolution * d.Resolution
	var values []float64
	switch a.Kind {
	case "scalar":
		if a.Scalar == nil {
			return fmt.Errorf("missing scalar")
		}
		values = a.Scalar.Values
	case "volume":
		if a.Volume == nil {
			return fmt.Errorf("missing volume")
		}
		values = a.Volume.Values
	case "vector":
		if a.Vector == nil || len(a.Vector.Values) != n {
			return fmt.Errorf("invalid vector grid")
		}
		for _, v := range a.Vector.Values {
			if !v.Finite() {
				return fmt.Errorf("non-finite vector")
			}
		}
	case "graph":
		if a.Graph == nil || len(a.Graph.Vertices) > 8192 {
			return fmt.Errorf("invalid graph")
		}
		for i, v := range a.Graph.Vertices {
			if !v.Position.Finite() || !finite(v.Radius) || v.Radius <= 0 || v.Parent >= i || v.Parent < -1 {
				return fmt.Errorf("invalid graph vertex")
			}
		}
	case "mesh":
		if a.Mesh == nil {
			return fmt.Errorf("missing mesh")
		}
		return validateMeshStructure(a.Mesh, d)
	default:
		return fmt.Errorf("unsupported artifact kind %s", a.Kind)
	}
	if values != nil {
		if len(values) != n {
			return fmt.Errorf("invalid grid length")
		}
		for _, x := range values {
			if !finite(x) {
				return fmt.Errorf("non-finite sample")
			}
		}
	} else if a.Kind == "scalar" || a.Kind == "volume" {
		return fmt.Errorf("missing grid values")
	}
	return nil
}
func (e *Engine) Evaluate(ctx context.Context, recipe Recipe, parent string, progress func(string)) (*Candidate, error) {
	start := time.Now()
	p, nodes, err := e.Registry.Compile(recipe)
	if err != nil {
		return nil, err
	}
	id := hash(struct {
		Recipe Recipe
		Build  string
	}{p, e.Build})
	candidate := &Candidate{ID: id, Parent: parent, Recipe: p, Engine: EngineVersion, Build: e.Build, Artifacts: map[string]ArtifactRef{}, Feedback: "neutral", Created: time.Now().UTC()}
	results := map[string]Outputs{}
	refs := map[string]map[string]ArtifactRef{}
	for _, node := range nodes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if progress != nil {
			progress(node.ID)
		}
		t := time.Now()
		inputs := Inputs{}
		upstream := map[string]string{}
		for port, ref := range node.Inputs {
			inputs[port] = results[ref.Node][ref.Port]
			upstream[port] = refs[ref.Node][ref.Port].Hash
		}
		seed := nodeSeed(uint64(p.Seed), node.ID)
		key := hash(struct {
			Build, Codec string
			Node         Node
			Domain       Domain
			Seed         uint64
			Inputs       map[string]string
		}{e.Build, e.CodecVersion, node, p.Domain, seed, upstream})
		reg := e.Registry.entries[node.Operator]
		validate := func(o Outputs) error {
			if len(o) != len(reg.desc.Outputs) {
				return fmt.Errorf("unexpected output count")
			}
			for name, port := range reg.desc.Outputs {
				a, ok := o[name]
				if !ok || a.Kind != port.Kind {
					return fmt.Errorf("invalid output %s", name)
				}
				if err := validateArtifact(a, p.Domain); err != nil {
					return err
				}
			}
			return nil
		}
		out, cacheErr := e.readCache(key)
		if cacheErr == nil {
			cacheErr = validate(out)
		}
		cached := cacheErr == nil
		if !cached {
			op, err := reg.factory(node.Params)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", node.ID, err)
			}
			out, err = op.Run(ctx, p.Domain, seed, inputs)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", node.ID, err)
			}
			if err = validate(out); err != nil {
				return nil, fmt.Errorf("%s: %w", node.ID, err)
			}
			if err = e.writeCache(key, out); err != nil {
				return nil, err
			}
		}
		results[node.ID] = out
		refs[node.ID] = map[string]ArtifactRef{}
		for name, a := range out {
			refs[node.ID][name] = ArtifactRef{key, name, hash(a)}
		}
		candidate.Nodes = append(candidate.Nodes, NodeRecord{node.ID, node.Operator, node.Version, Seed(seed), key, cached, time.Since(t).Milliseconds()})
	}
	selected := map[string]Artifact{}
	for name, ref := range p.Outputs {
		candidate.Artifacts[name] = refs[ref.Node][ref.Port]
		selected[name] = results[ref.Node][ref.Port]
	}
	candidate.Metrics, candidate.Problems, err = e.Evaluator.Evaluate(ctx, p, selected)
	if err != nil {
		return nil, err
	}
	candidate.Valid = len(candidate.Problems) == 0
	candidate.Milliseconds = time.Since(start).Milliseconds()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if old, err := e.Get(id); err == nil {
		candidate.Feedback = old.Feedback
		candidate.Created = old.Created
		if old.Parent != "" {
			candidate.Parent = old.Parent
		}
	}
	dir := filepath.Join(e.Workspace, "candidates", id)
	if candidate.Valid {
		glb, err := EncodeGLB(selected["mesh"].Mesh)
		if err != nil {
			return nil, err
		}
		if err = atomicWrite(filepath.Join(dir, "asset.glb"), glb); err != nil {
			return nil, err
		}
	}
	if err = writeJSON(filepath.Join(dir, "candidate.json"), candidate); err != nil {
		return nil, err
	}
	return candidate, nil
}
func (e *Engine) Get(id string) (*Candidate, error) {
	if !safeID(id) {
		return nil, fmt.Errorf("invalid candidate ID")
	}
	b, err := os.ReadFile(filepath.Join(e.Workspace, "candidates", id, "candidate.json"))
	if err != nil {
		return nil, err
	}
	var c Candidate
	if err = json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}
func (e *Engine) List() ([]Candidate, error) {
	dirs, err := os.ReadDir(filepath.Join(e.Workspace, "candidates"))
	if err != nil {
		return nil, err
	}
	out := []Candidate{}
	for _, d := range dirs {
		if d.IsDir() && safeID(d.Name()) {
			c, err := e.Get(d.Name())
			// The manifest commits a candidate. A crash between writing the GLB
			// and the manifest can leave an incomplete directory to ignore.
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Created.Equal(out[j].Created) {
			return out[i].ID < out[j].ID
		}
		return out[i].Created.After(out[j].Created)
	})
	return out, nil
}
func (e *Engine) Artifact(c *Candidate, name string) (Artifact, error) {
	ref, ok := c.Artifacts[name]
	if !ok {
		return Artifact{}, fmt.Errorf("unknown artifact")
	}
	o, err := e.readCache(ref.Key)
	if err != nil {
		return Artifact{}, err
	}
	a, ok := o[ref.Port]
	if !ok || hash(a) != ref.Hash {
		return Artifact{}, fmt.Errorf("artifact checksum mismatch")
	}
	return a, validateArtifact(a, c.Recipe.Domain)
}
func (e *Engine) Feedback(id, state string) error {
	if state != "neutral" && state != "favorite" && state != "rejected" {
		return fmt.Errorf("invalid feedback")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	c, err := e.Get(id)
	if err != nil {
		return err
	}
	c.Feedback = state
	if err = writeJSON(filepath.Join(e.Workspace, "candidates", id, "candidate.json"), c); err != nil {
		return err
	}
	return e.rebuildArchives()
}
func (e *Engine) Export(ctx context.Context, id string, w io.Writer) error {
	c, err := e.Get(id)
	if err != nil {
		return err
	}
	if !c.Valid {
		return fmt.Errorf("candidate is invalid")
	}
	z := zip.NewWriter(w)
	defer z.Close()
	add := func(name string, b []byte) error {
		f, err := z.Create(name)
		if err != nil {
			return err
		}
		_, err = f.Write(b)
		return err
	}
	glb, err := os.ReadFile(filepath.Join(e.Workspace, "candidates", id, "asset.glb"))
	if err != nil {
		return err
	}
	if err = add("asset.glb", glb); err != nil {
		return err
	}
	for name, v := range map[string]any{"candidate.json": c, "recipe.json": c.Recipe} {
		b, _ := json.MarshalIndent(v, "", "  ")
		if err = add(name, b); err != nil {
			return err
		}
	}
	names := []string{}
	for name := range c.Artifacts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err = ctx.Err(); err != nil {
			return err
		}
		a, err := e.Artifact(c, name)
		if err != nil {
			return err
		}
		b, _ := json.Marshal(a)
		if err = add("artifacts/"+name+".json", b); err != nil {
			return err
		}
	}
	return z.Close()
}
