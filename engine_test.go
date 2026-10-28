package progen

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func smallRecipe() Recipe {
	r := DefaultRecipe()
	r.Domain.Resolution = 20
	for i, n := range r.Nodes {
		switch n.Operator {
		case "branching":
			r.Nodes[i].Params = map[string]float64{"attractions": 70, "iterations": 60, "max_vertices": 300, "radius": .07}
		case "porous":
			r.Nodes[i].Params = map[string]float64{"amount": 0}
		}
	}
	return r
}
func testEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := NewEngine(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func TestRecipeValidation(t *testing.T) {
	registry := Builtins()
	base := smallRecipe()
	if _, _, err := registry.Compile(base); err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*Recipe){"cycle": func(r *Recipe) { r.Nodes[4].Inputs["volume"] = Reference{"pores", "volume"} }, "missing": func(r *Recipe) { r.Nodes[1].Inputs["field"] = Reference{"missing", "field"} }, "semantic mismatch": func(r *Recipe) { r.Nodes[2].Inputs["direction"] = Reference{"field", "field"} }, "unknown parameter": func(r *Recipe) { r.Nodes[0].Params = map[string]float64{"bad": 1} }, "bounds": func(r *Recipe) { r.Nodes[0].Params = map[string]float64{"order": 13} }, "integer": func(r *Recipe) { r.Nodes[0].Params = map[string]float64{"order": 1.5} }, "resolution": func(r *Recipe) { r.Domain.Resolution = 1000 }, "duplicate": func(r *Recipe) { r.Nodes[1].ID = r.Nodes[0].ID }, "version": func(r *Recipe) { r.Nodes[0].Version = "9" }, "frame": func(r *Recipe) { r.Domain.Frame = "left-handed" }, "output": func(r *Recipe) { r.Outputs["mesh"] = Reference{"surface", "missing"} }}
	for name, edit := range tests {
		t.Run(name, func(t *testing.T) {
			r := cloneRecipe(base)
			edit(&r)
			if _, _, err := registry.Compile(r); err == nil {
				t.Fatal("accepted invalid recipe")
			}
		})
	}
	p, order, err := registry.Compile(base)
	if err != nil {
		t.Fatal(err)
	}
	if p.Nodes[0].Params == nil || len(order) != 6 {
		t.Fatal("missing defaults/order")
	}
	if base.Nodes[0].Params != nil {
		t.Fatal("compile mutated input")
	}
}
func TestEigenmodeAndGradient(t *testing.T) {
	d := DefaultRecipe().Domain
	d.Resolution = 33
	f, err := (Eigenmode{1, 1}).Generate(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(f.Sample(Vec3{})-1) > 1e-10 {
		t.Fatal("fundamental center value")
	}
	if math.Abs(f.Sample(Vec3{-1, 0, 0})) > 1e-10 {
		t.Fatal("Dirichlet boundary")
	}
	v, err := Gradient(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	p := Vec3{.5, 0, 0}
	want := -math.Pi / 2 * math.Sin(math.Pi*.25)
	if math.Abs(v.SampleVector(p).X-want) > .01 {
		t.Fatalf("gradient %v want %v", v.SampleVector(p).X, want)
	}
	if _, err = (Eigenmode{0, 1}).Generate(context.Background(), d); err == nil {
		t.Fatal("invalid mode")
	}
}
func TestCapsulesPorosityAndMesh(t *testing.T) {
	d := DefaultRecipe().Domain
	d.Resolution = 24
	g := &Graph{Space: d, Vertices: []Vertex{{Vec3{0, -.5, 0}, .25, -1, 0}, {Vec3{0, .5, 0}, .25, 0, 1}}}
	v, err := (Capsules{}).Solidify(context.Background(), g)
	if err != nil {
		t.Fatal(err)
	}
	if v.SignedValue(Vec3{}) >= 0 || v.SignedValue(Vec3{.8, 0, 0}) <= 0 {
		t.Fatal("capsule sign")
	}
	unchanged, err := (Porous{0, 3}).Modify(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	for i, x := range v.Values {
		if math.Abs(x-unchanged.Values[i]) > 1e-12 {
			t.Fatalf("zero porosity changed sample %d", i)
		}
	}
	porous, err := (Porous{.3, 3}).Modify(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	before, after := 0, 0
	for i, x := range v.Values {
		if x < 0 {
			before++
		}
		if porous.Values[i] < 0 {
			after++
		}
	}
	if after >= before {
		t.Fatal("pores did not remove material")
	}
	m, err := (Tetrahedra{}).Mesh(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateMesh(m, d); err != nil {
		t.Fatal(err)
	}
	edges := map[[2]uint32]int{}
	for i := 0; i < len(m.Indices); i += 3 {
		a, b, c := m.Indices[i], m.Indices[i+1], m.Indices[i+2]
		for _, pair := range [][2]uint32{{a, b}, {b, c}, {c, a}} {
			if pair[0] > pair[1] {
				pair[0], pair[1] = pair[1], pair[0]
			}
			edges[pair]++
		}
	}
	for edge, count := range edges {
		if count != 2 {
			t.Fatalf("non-manifold edge %v: %d faces", edge, count)
		}
	}
	glb, err := EncodeGLB(m)
	if err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(glb[:4]) != 0x46546c67 || int(binary.LittleEndian.Uint32(glb[8:12])) != len(glb) {
		t.Fatal("invalid GLB header")
	}
	d.Exclusions = []Box{{Vec3{-.12, -.3, -.3}, Vec3{.12, .3, .3}}}
	g.Space = d
	v, err = (Capsules{}).Solidify(context.Background(), g)
	if err != nil {
		t.Fatal(err)
	}
	if v.SignedValue(Vec3{}) < 0 {
		t.Fatal("exclusion not carved")
	}
	if segmentAllowed(d, Vec3{-.5, 0, 0}, Vec3{.5, 0, 0}) {
		t.Fatal("segment crossed exclusion")
	}
}
func TestReproducibilityCacheAndExport(t *testing.T) {
	e := testEngine(t)
	r := smallRecipe()
	c, err := e.Evaluate(context.Background(), r, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Valid {
		t.Fatalf("invalid fixture: %v", c.Problems)
	}
	again, err := e.Evaluate(context.Background(), r, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != again.ID || !reflect.DeepEqual(c.Artifacts, again.Artifacts) {
		t.Fatal("not reproducible")
	}
	for _, n := range again.Nodes {
		if !n.Cached {
			t.Fatalf("cache miss: %s", n.ID)
		}
	}
	if err = e.Feedback(c.ID, "favorite"); err != nil {
		t.Fatal(err)
	}
	restart, err := NewEngine(e.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := restart.Get(c.ID)
	if err != nil || saved.Feedback != "favorite" {
		t.Fatal("feedback not persisted", err)
	}
	r.Nodes[4].Params["amount"] = .2
	changed, err := e.Evaluate(context.Background(), r, c.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range changed.Nodes {
		want := n.ID != "pores" && n.ID != "surface"
		if n.Cached != want {
			t.Fatalf("cache invalidation %s cached=%v", n.ID, n.Cached)
		}
	}
	key := again.Nodes[0].CacheKey
	if err = os.WriteFile(filepath.Join(e.Workspace, "cache", key+".json.gz"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	repaired, err := e.Evaluate(context.Background(), smallRecipe(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Nodes[0].Cached {
		t.Fatal("corrupt cache accepted")
	}
	var b bytes.Buffer
	if err = e.Export(context.Background(), c.ID, &b); err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(b.Bytes()), int64(b.Len()))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range z.File {
		names[f.Name] = true
	}
	for _, name := range []string{"asset.glb", "candidate.json", "recipe.json", "artifacts/graph.json", "artifacts/field.json", "artifacts/volume.json"} {
		if !names[name] {
			t.Fatal("missing export", name)
		}
	}
}
func TestCancellationAndWorkers(t *testing.T) {
	e := testEngine(t)
	r := smallRecipe()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Evaluate(ctx, r, "", nil); err == nil {
		t.Fatal("ignored cancellation")
	}
	request := RunRequest{Kind: "vary", Parent: "bad", Count: 1}
	if _, _, err := e.Prepare(request); err == nil {
		t.Fatal("accepted missing parent")
	}
	request = RunRequest{Kind: "generate", Recipe: &r, Count: 3, SearchSeed: 44, Workers: 1}
	one, err := e.Run(context.Background(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	other := testEngine(t)
	request.Workers = 3
	many, err := other.Run(context.Background(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range one {
		if !reflect.DeepEqual(one[i].Artifacts, many[i].Artifacts) {
			t.Fatalf("worker count changed candidate %d", i)
		}
	}
	for _, c := range many {
		for _, n := range c.Nodes {
			if n.Cached {
				t.Fatal("unexpected cross-workspace cache")
			}
		}
	}
}

type alternateGrowth struct{}

func (alternateGrowth) Grow(_ context.Context, f VectorField, _ uint64) (*Graph, error) {
	return &Graph{Space: f.Domain(), Vertices: []Vertex{{Vec3{0, -.5, 0}, .2, -1, 0}, {Vec3{0, .5, 0}, .2, 0, 1}}}, nil
}

var _ GraphGrowth = alternateGrowth{}

func TestOperatorSubstitution(t *testing.T) {
	e := testEngine(t)
	d := e.Registry.entries["branching"].desc
	d.ID = "straight-growth"
	d.Parameters = nil
	err := e.Registry.Register(d, func(_ map[string]float64) (Operator, error) {
		var growth GraphGrowth = alternateGrowth{}
		return OperatorFunc(func(ctx context.Context, _ Domain, seed uint64, in Inputs) (Outputs, error) {
			g, err := growth.Grow(ctx, in["direction"].Vector, seed)
			return Outputs{"graph": {Kind: "graph", Graph: g}}, err
		}), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	r := smallRecipe()
	r.Nodes[2].Operator = d.ID
	r.Nodes[2].Params = nil
	c, err := e.Evaluate(context.Background(), r, "", nil)
	if err != nil || !c.Valid {
		t.Fatal("replacement failed", c, err)
	}
}
func TestMutationArchiveAndFeedback(t *testing.T) {
	e := testEngine(t)
	r, _, err := e.Registry.Compile(smallRecipe())
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewPCG(2, 3))
	for i := 0; i < 100; i++ {
		m := e.mutate(r, rng, .8, false)
		if m.Seed != r.Seed {
			t.Fatal("mutation changed generation seed")
		}
		if _, _, err = e.Registry.Compile(m); err != nil {
			t.Fatal(err)
		}
	}
	makeCandidate := func(id string, quality float64) Candidate {
		return Candidate{ID: strings.Repeat(id, 64), Recipe: r, Valid: true, Feedback: "neutral", Metrics: Metrics{BranchFraction: .1, Porosity: .2, ConnectedFraction: quality}}
	}
	a, b := makeCandidate("a", .9), makeCandidate("b", .8)
	for _, c := range []Candidate{a, b} {
		if err = writeJSON(filepath.Join(e.Workspace, "candidates", c.ID, "candidate.json"), c); err != nil {
			t.Fatal(err)
		}
	}
	archive, err := e.Archive(r)
	if err != nil {
		t.Fatal(err)
	}
	if archive.Cells["3,3"] != a.ID {
		t.Fatal("wrong quality elite")
	}
	if err = e.Feedback(b.ID, "favorite"); err != nil {
		t.Fatal(err)
	}
	archive, _ = e.Archive(r)
	if archive.Cells["3,3"] != b.ID {
		t.Fatal("favorite did not rank first")
	}
	e.Feedback(b.ID, "rejected")
	archive, _ = e.Archive(r)
	if archive.Cells["3,3"] != a.ID {
		t.Fatal("rejected elite remained")
	}
	e.Feedback(a.ID, "rejected")
	archive, _ = e.Archive(r)
	if len(archive.Cells) != 0 {
		t.Fatal("rejected candidate admitted")
	}
}
func TestSearchDeterministic(t *testing.T) {
	r := smallRecipe()
	e1, e2 := testEngine(t), testEngine(t)
	request := RunRequest{Kind: "search", Recipe: &r, Count: 3, SearchSeed: 9}
	a, err := e1.Run(context.Background(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := e2.Run(context.Background(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			t.Fatal("search not deterministic")
		}
	}
	archive, err := e1.Archive(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(e1.Workspace, "archives", archive.ID+".json")); err != nil {
		t.Fatal("archive not persisted", err)
	}
}
func TestAPIAndInterruptedJob(t *testing.T) {
	e := testEngine(t)
	s, err := NewServer(e)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := s.Handler()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	if w := request("GET", "/api/operators", ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	r := smallRecipe()
	b, _ := json.Marshal(RunRequest{Kind: "generate", Recipe: &r, Count: 1})
	w := request("POST", "/api/jobs", string(b))
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	var job Job
	json.Unmarshal(w.Body.Bytes(), &job)
	deadline := time.Now().Add(20 * time.Second)
	for {
		w = request("GET", "/api/jobs/"+job.ID, "")
		json.Unmarshal(w.Body.Bytes(), &job)
		if job.Status != "queued" && job.Status != "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if job.Status != "completed" || len(job.Candidates) != 1 {
		t.Fatal(job)
	}
	id := job.Candidates[0]
	if w = request("GET", "/api/candidates/"+id+"/asset.glb", ""); w.Code != 200 || !bytes.HasPrefix(w.Body.Bytes(), []byte("glTF")) {
		t.Fatal("GLB unavailable", w.Code)
	}
	if w = request("POST", "/api/candidates/"+id+"/feedback", `{"state":"favorite"}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	req := httptest.NewRequest("POST", "/api/jobs", bytes.NewReader(b))
	req.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatal("cross-origin mutation accepted")
	}
	if w = request("POST", "/api/jobs", `{"count":9999}`); w.Code != 400 {
		t.Fatal("unbounded count accepted")
	}
	pending := Job{ID: "interrupted", Status: "running"}
	writeJSON(filepath.Join(e.Workspace, "jobs", "interrupted.json"), pending)
	restart, err := NewServer(e)
	if err != nil {
		t.Fatal(err)
	}
	defer restart.Close()
	if restart.jobs["interrupted"].Status != "interrupted" {
		t.Fatal("restart lost interrupted job")
	}
}
func BenchmarkGeneration(b *testing.B) {
	e, err := NewEngine(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	r := DefaultRecipe()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		r.Seed = Seed(i + 100)
		c, err := e.Evaluate(context.Background(), r, "", nil)
		if err != nil {
			b.Fatal(err)
		}
		if !c.Valid {
			b.Fatal(c.Problems)
		}
	}
}

func TestRectangularModeOrder(t *testing.T) {
	if got := modeAt(2, Vec3{10, 1, 1}); got != [3]int{2, 1, 1} {
		t.Fatalf("incorrect rectangular-domain mode ordering: %v", got)
	}
}
func TestExportNamesAndRunManifest(t *testing.T) {
	r := smallRecipe()
	r.Outputs["../escape"] = r.Outputs["mesh"]
	if _, _, err := Builtins().Compile(r); err == nil {
		t.Fatal("unsafe output filename accepted")
	}
	e := testEngine(t)
	r = smallRecipe()
	_, err := e.Run(context.Background(), RunRequest{Recipe: &r, Count: 1, SearchSeed: 123}, nil)
	if err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(filepath.Join(e.Workspace, "runs"))
	if err != nil || len(files) != 1 {
		t.Fatal("missing run record", err)
	}
	b, err := os.ReadFile(filepath.Join(e.Workspace, "runs", files[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var record RunRecord
	if err = json.Unmarshal(b, &record); err != nil {
		t.Fatal(err)
	}
	if record.Request.SearchSeed != 123 || record.Status != "completed" || len(record.Candidates) != 1 {
		t.Fatalf("incomplete run record: %+v", record)
	}
}
func TestQueuedJobCancellation(t *testing.T) {
	e := testEngine(t)
	s, err := NewServer(e)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.gate <- struct{}{}
	r := smallRecipe()
	body, _ := json.Marshal(RunRequest{Recipe: &r, Count: 2})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/api/jobs", bytes.NewReader(body)))
	var job Job
	json.Unmarshal(w.Body.Bytes(), &job)
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("DELETE", "/api/jobs/"+job.ID, nil))
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	s.wg.Wait()
	<-s.gate
	s.mu.Lock()
	status := s.jobs[job.ID].Status
	s.mu.Unlock()
	if status != "cancelled" {
		t.Fatal(status)
	}
	all, err := e.List()
	if err != nil || len(all) != 0 {
		t.Fatal("cancelled queue generated candidates", err)
	}
}

func TestSeedJSONPrecision(t *testing.T) {
	for _, input := range []string{`18446744073709551615`, `"18446744073709551615"`} {
		var seed Seed
		if err := json.Unmarshal([]byte(input), &seed); err != nil {
			t.Fatal(err)
		}
		if uint64(seed) != ^uint64(0) {
			t.Fatal("seed truncated")
		}
		b, err := json.Marshal(seed)
		if err != nil || string(b) != `"18446744073709551615"` {
			t.Fatal("unsafe seed JSON", string(b), err)
		}
	}
	for _, input := range []string{`-1`, `1.5`, `18446744073709551616`, `null`} {
		var seed Seed
		if err := json.Unmarshal([]byte(input), &seed); err == nil {
			t.Fatal("invalid seed accepted", input)
		}
	}
}

func TestInterruptedCandidateDirectory(t *testing.T) {
	e := testEngine(t)
	incomplete := filepath.Join(e.Workspace, "candidates", strings.Repeat("a", 64))
	if err := os.MkdirAll(incomplete, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(incomplete, "asset.glb"), []byte("uncommitted"), 0600); err != nil {
		t.Fatal(err)
	}
	all, err := e.List()
	if err != nil || len(all) != 0 {
		t.Fatal("incomplete candidate blocked the gallery", err)
	}
}
