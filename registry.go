package progen

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

const EngineVersion = "progen-1.0.0"

type Param struct {
	Type    string  `json:"type"`
	Default float64 `json:"default"`
	Min     float64 `json:"min"`
	Max     float64 `json:"max"`
	Mutable bool    `json:"mutable"`
}
type Port struct {
	Kind     string `json:"kind"`
	Semantic string `json:"semantic"`
}
type Descriptor struct {
	ID           string           `json:"id"`
	Version      string           `json:"version"`
	Inputs       map[string]Port  `json:"inputs"`
	Outputs      map[string]Port  `json:"outputs"`
	Parameters   map[string]Param `json:"parameters"`
	Requirements string           `json:"requirements"`
}
type Inputs map[string]Artifact
type Outputs map[string]Artifact
type Operator interface {
	Run(context.Context, Domain, uint64, Inputs) (Outputs, error)
}
type OperatorFunc func(context.Context, Domain, uint64, Inputs) (Outputs, error)

func (f OperatorFunc) Run(c context.Context, d Domain, s uint64, i Inputs) (Outputs, error) {
	return f(c, d, s, i)
}

type Factory func(map[string]float64) (Operator, error)
type registration struct {
	desc    Descriptor
	factory Factory
}
type Registry struct{ entries map[string]registration }

func NewRegistry() *Registry { return &Registry{map[string]registration{}} }
func (r *Registry) Register(d Descriptor, f Factory) error {
	if d.ID == "" || d.Version == "" || f == nil || len(d.Outputs) == 0 {
		return fmt.Errorf("incomplete operator registration")
	}
	if _, ok := r.entries[d.ID]; ok {
		return fmt.Errorf("duplicate operator %s", d.ID)
	}
	r.entries[d.ID] = registration{d, f}
	return nil
}
func (r *Registry) Descriptors() []Descriptor {
	ds := []Descriptor{}
	for _, e := range r.entries {
		ds = append(ds, e.desc)
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i].ID < ds[j].ID })
	return ds
}
func parameter(def, min, max float64, integer, mutable bool) Param {
	t := "number"
	if integer {
		t = "integer"
	}
	return Param{t, def, min, max, mutable}
}
func Builtins() *Registry {
	r := NewRegistry()
	scalar := Port{"scalar", "potential"}
	vector := Port{"vector", "guidance"}
	graph := Port{"graph", "skeleton"}
	volume := Port{"volume", "solid"}
	mesh := Port{"mesh", "surface"}
	add := func(id string, in, out map[string]Port, p map[string]Param, f Factory) {
		if err := r.Register(Descriptor{id, "1", in, out, p, "CPU; bounded by domain resolution and declared growth budgets"}, f); err != nil {
			panic(err)
		}
	}
	add("eigenmode", nil, map[string]Port{"field": scalar}, map[string]Param{"order": parameter(3, 1, 12, true, true), "weight": parameter(1, .1, 3, false, false)}, func(p map[string]float64) (Operator, error) {
		e := Eigenmode{int(p["order"]), p["weight"]}
		return OperatorFunc(func(c context.Context, d Domain, _ uint64, _ Inputs) (Outputs, error) {
			v, e := e.Generate(c, d)
			return Outputs{"field": {Kind: "scalar", Scalar: v}}, e
		}), nil
	})
	add("gradient", map[string]Port{"field": scalar}, map[string]Port{"direction": vector}, nil, func(_ map[string]float64) (Operator, error) {
		return OperatorFunc(func(c context.Context, _ Domain, _ uint64, i Inputs) (Outputs, error) {
			v, e := Gradient(c, i["field"].Scalar)
			return Outputs{"direction": {Kind: "vector", Vector: v}}, e
		}), nil
	})
	add("branching", map[string]Port{"direction": vector}, map[string]Port{"graph": graph}, map[string]Param{"attractions": parameter(600, 32, 3000, true, true), "iterations": parameter(120, 1, 512, true, false), "max_vertices": parameter(1400, 2, 4096, true, false), "step": parameter(.07, .02, .15, false, true), "influence": parameter(.35, .1, 1, false, true), "kill": parameter(.09, .02, .2, false, false), "guidance": parameter(.3, 0, .8, false, true), "radius": parameter(.035, .02, .09, false, true)}, func(p map[string]float64) (Operator, error) {
		b := Branching{int(p["attractions"]), int(p["iterations"]), int(p["max_vertices"]), p["step"], p["influence"], p["kill"], p["guidance"], p["radius"]}
		return OperatorFunc(func(c context.Context, _ Domain, s uint64, i Inputs) (Outputs, error) {
			v, e := b.Grow(c, i["direction"].Vector, s)
			return Outputs{"graph": {Kind: "graph", Graph: v}}, e
		}), nil
	})
	add("solidify", map[string]Port{"graph": graph}, map[string]Port{"volume": volume}, nil, func(_ map[string]float64) (Operator, error) {
		return OperatorFunc(func(c context.Context, _ Domain, _ uint64, i Inputs) (Outputs, error) {
			v, e := (Capsules{}).Solidify(c, i["graph"].Graph)
			return Outputs{"volume": {Kind: "volume", Volume: v}}, e
		}), nil
	})
	add("porous", map[string]Port{"volume": volume}, map[string]Port{"volume": volume}, map[string]Param{"amount": parameter(.16, 0, .65, false, true), "frequency": parameter(3, 1, 7, false, true)}, func(p map[string]float64) (Operator, error) {
		op := Porous{p["amount"], p["frequency"]}
		return OperatorFunc(func(c context.Context, _ Domain, _ uint64, i Inputs) (Outputs, error) {
			v, e := op.Modify(c, i["volume"].Volume)
			return Outputs{"volume": {Kind: "volume", Volume: v}}, e
		}), nil
	})
	add("mesh", map[string]Port{"volume": volume}, map[string]Port{"mesh": mesh}, nil, func(_ map[string]float64) (Operator, error) {
		return OperatorFunc(func(c context.Context, _ Domain, _ uint64, i Inputs) (Outputs, error) {
			v, e := (Tetrahedra{}).Mesh(c, i["volume"].Volume)
			return Outputs{"mesh": {Kind: "mesh", Mesh: v}}, e
		}), nil
	})
	return r
}

type Reference struct {
	Node string `json:"node"`
	Port string `json:"port"`
}
type Node struct {
	ID       string               `json:"id"`
	Operator string               `json:"operator"`
	Version  string               `json:"version"`
	Params   map[string]float64   `json:"params,omitempty"`
	Inputs   map[string]Reference `json:"inputs,omitempty"`
}
type Recipe struct {
	Version int                  `json:"version"`
	Name    string               `json:"name"`
	Domain  Domain               `json:"domain"`
	Seed    Seed                 `json:"seed"`
	Nodes   []Node               `json:"nodes"`
	Outputs map[string]Reference `json:"outputs"`
}

func DefaultRecipe() Recipe {
	d := Domain{Box: Box{Vec3{-1, -1, -1}, Vec3{1, 1, 1}}, Resolution: 64, Frame: "right-handed-y-up", Unit: "meters", Boundary: "dirichlet"}
	return Recipe{1, "Eigenmode branching", d, 42, []Node{{"field", "eigenmode", "1", nil, nil}, {"guide", "gradient", "1", nil, map[string]Reference{"field": {"field", "field"}}}, {"growth", "branching", "1", nil, map[string]Reference{"direction": {"guide", "direction"}}}, {"solid", "solidify", "1", nil, map[string]Reference{"graph": {"growth", "graph"}}}, {"pores", "porous", "1", nil, map[string]Reference{"volume": {"solid", "volume"}}}, {"surface", "mesh", "1", nil, map[string]Reference{"volume": {"pores", "volume"}}}}, map[string]Reference{"mesh": {"surface", "mesh"}, "graph": {"growth", "graph"}, "base": {"solid", "volume"}, "volume": {"pores", "volume"}, "field": {"field", "field"}, "direction": {"guide", "direction"}}}
}
func cloneRecipe(r Recipe) Recipe {
	b, _ := json.Marshal(r)
	var out Recipe
	json.Unmarshal(b, &out)
	return out
}
func (r *Registry) Compile(recipe Recipe) (Recipe, []Node, error) {
	p := cloneRecipe(recipe)
	if p.Version != 1 {
		return p, nil, fmt.Errorf("unsupported recipe version")
	}
	if err := p.Domain.Validate(); err != nil {
		return p, nil, err
	}
	if len(p.Nodes) == 0 || len(p.Nodes) > 64 || len(p.Outputs) == 0 {
		return p, nil, fmt.Errorf("recipe needs 1..64 nodes and outputs")
	}
	nodes := map[string]Node{}
	for j, n := range p.Nodes {
		if !validName(n.ID) {
			return p, nil, fmt.Errorf("empty node ID")
		}
		if _, ok := nodes[n.ID]; ok {
			return p, nil, fmt.Errorf("duplicate node %s", n.ID)
		}
		entry, ok := r.entries[n.Operator]
		if !ok || entry.desc.Version != n.Version {
			return p, nil, fmt.Errorf("unknown operator/version %s@%s", n.Operator, n.Version)
		}
		if n.Params == nil {
			n.Params = map[string]float64{}
		}
		for k, v := range n.Params {
			schema, ok := entry.desc.Parameters[k]
			if !ok || !finite(v) || v < schema.Min || v > schema.Max || (schema.Type == "integer" && v != float64(int(v))) {
				return p, nil, fmt.Errorf("%s: invalid parameter %s", n.ID, k)
			}
		}
		for k, s := range entry.desc.Parameters {
			if _, ok := n.Params[k]; !ok {
				n.Params[k] = s.Default
			}
		}
		if len(n.Inputs) != len(entry.desc.Inputs) {
			return p, nil, fmt.Errorf("%s: incorrect input ports", n.ID)
		}
		nodes[n.ID] = n
		p.Nodes[j] = n
	}
	for _, n := range p.Nodes {
		for name, ref := range n.Inputs {
			want, ok := r.entries[n.Operator].desc.Inputs[name]
			src, exists := nodes[ref.Node]
			if !ok || !exists {
				return p, nil, fmt.Errorf("%s: unknown input or source", n.ID)
			}
			got, ok := r.entries[src.Operator].desc.Outputs[ref.Port]
			if !ok || got != want {
				return p, nil, fmt.Errorf("%s.%s: incompatible port semantics", n.ID, name)
			}
		}
	}
	for name, ref := range p.Outputs {
		if !validName(name) {
			return p, nil, fmt.Errorf("invalid output name %q", name)
		}
		src, ok := nodes[ref.Node]
		if !ok {
			return p, nil, fmt.Errorf("missing output node")
		}
		if _, ok := r.entries[src.Operator].desc.Outputs[ref.Port]; !ok {
			return p, nil, fmt.Errorf("missing output port")
		}
	}
	order := []Node{}
	done := map[string]bool{}
	for len(order) < len(nodes) {
		ready := []string{}
		for id, n := range nodes {
			if done[id] {
				continue
			}
			ok := true
			for _, ref := range n.Inputs {
				if !done[ref.Node] {
					ok = false
				}
			}
			if ok {
				ready = append(ready, id)
			}
		}
		if len(ready) == 0 {
			return p, nil, fmt.Errorf("recipe contains a cycle")
		}
		sort.Strings(ready)
		for _, id := range ready {
			order = append(order, nodes[id])
			done[id] = true
		}
	}
	sort.Slice(p.Nodes, func(i, j int) bool { return p.Nodes[i].ID < p.Nodes[j].ID })
	return p, order, nil
}
func hash(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func nodeSeed(seed uint64, id string) uint64 {
	s := sha256.Sum256([]byte(fmt.Sprintf("%d/%s", seed, id)))
	return binary.LittleEndian.Uint64(s[:8])
}

// validName keeps recipe identifiers and exported artifact filenames portable.
func validName(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// RecipePreset is a reproducible starting pipeline for a geometry family.
type RecipePreset struct {
 ID string `json:"id"`
 Name string `json:"name"`
 Recipe Recipe `json:"recipe"`
}
func RecipePresets() []RecipePreset {
 return []RecipePreset{{ID:"branching", Name:"Branching / coral", Recipe:DefaultRecipe()}}
}
