package progen

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ConditionedSource records exact input bytes separately from their parsed semantics.
// The hashes participate in recipe, candidate, family, and node cache identity.
type ConditionedSource struct {
	GLBSHA256     string         `json:"glb_sha256"`
	SidecarSHA256 string         `json:"sidecar_sha256"`
	Spec          ConstraintSpec `json:"spec"`
}

func (s ConditionedSource) Validate(d Domain) error {
	if !safeID(s.GLBSHA256) || !safeID(s.SidecarSHA256) {
		return fmt.Errorf("source fingerprints must be SHA-256 hex")
	}
	return s.Spec.ValidateDomain(d)
}
func sourceDigest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

//go:embed examples/bridge.glb
var bridgeGLB []byte

//go:embed examples/bridge.constraints.json
var bridgeSidecar []byte

//go:embed examples/bridge-relief.constraints.json
var bridgeReliefSidecar []byte

//go:embed examples/bridge-structural.constraints.json
var bridgeStructuralSidecar []byte

func sourceRecipe(glb, sidecar []byte) (Recipe, error) {
	s, err := ParseConstraintSpec(sidecar)
	if err != nil {
		return Recipe{}, err
	}
	r := Recipe{Version: 1, Name: "Bridge / constrained decoration", Domain: s.Domain, Seed: 42,
		Source: &ConditionedSource{sourceDigest(glb), sourceDigest(sidecar), s}}
	r.Nodes = []Node{
		{ID: "reference", Operator: "archetype", Version: "1"},
		{ID: "periodic", Operator: "tpms", Version: "1", Params: map[string]float64{"frequency": 2, "thickness": .25, "envelope": .95}},
		{ID: "cells", Operator: "voronoi_foam", Version: "1", Params: map[string]float64{"cells": 16, "wall": .05, "envelope": .95}},
		{ID: "combine", Operator: "csg", Version: "1", Params: map[string]float64{"mode": 0}, Inputs: map[string]Reference{"a": {"periodic", "volume"}, "b": {"cells", "volume"}}},
		{ID: "deform", Operator: "warp", Version: "1", Params: map[string]float64{"amplitude": .03}, Inputs: map[string]Reference{"volume": {"combine", "volume"}}},
		{ID: "condition", Operator: "constrain", Version: "1", Inputs: map[string]Reference{"reference": {"reference", "volume"}, "decoration": {"deform", "volume"}, "required": {"reference", "required"}, "forbidden": {"reference", "forbidden"}, "editable": {"reference", "editable"}}},
		{ID: "surface", Operator: "mesh", Version: "1", Inputs: map[string]Reference{"volume": {"condition", "volume"}}},
	}
	for i, node := range r.Nodes {
		for key, value := range node.Params {
			if bounds, ok := s.ParameterBounds[node.ID][key]; ok {
				r.Nodes[i].Params[key] = max(bounds.Min, min(bounds.Max, value))
			}
		}
	}
	r.Outputs = map[string]Reference{"mesh": {"surface", "mesh"}, "base": {"reference", "volume"}, "volume": {"condition", "volume"}, "reference": {"reference", "mesh"}, "reference_volume": {"reference", "volume"}, "required": {"reference", "required"}, "forbidden": {"reference", "forbidden"}, "editable": {"reference", "editable"}}
	if s.FloorRelief != nil {
		r = withFloorRelief(r)
	}
	if s.DeckPolicy == "structural-experiment" {
		r = withStructuralDeck(r)
	}
	return r, nil
}
func BridgeRecipe() Recipe {
	r, err := sourceRecipe(bridgeGLB, bridgeSidecar)
	if err != nil {
		panic(err)
	}
	return r
}

const sourceStoreLimit = 64 << 20

// ImportArchetype validates before persisting exact source bytes in a bounded,
// content-addressed asset library. Existing hashes are verified on every reload.
func (e *Engine) ImportArchetype(ctx context.Context, glb, sidecar []byte) (Recipe, error) {
	if len(glb) > 8<<20 || len(sidecar) > 64<<10 {
		return Recipe{}, fmt.Errorf("import exceeds 8 MiB GLB / 64 KiB sidecar limit")
	}
	r, err := sourceRecipe(glb, sidecar)
	if err != nil {
		return r, err
	}
	if _, _, err = DecodeArchetypeGLB(ctx, glb, r.Source.Spec.Transform, r.Domain); err != nil {
		return r, err
	}
	if _, _, _, err = r.Source.Spec.Masks(ctx, r.Domain); err != nil {
		return r, err
	}
	r, _, err = e.Registry.Compile(r)
	if err != nil {
		return r, err
	}
	if err = ctx.Err(); err != nil {
		return r, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err = ctx.Err(); err != nil {
		return r, err
	}
	dir := filepath.Join(e.Workspace, "sources")
	if err = os.MkdirAll(dir, 0755); err != nil {
		return r, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return r, err
	}
	size := int64(0)
	for _, f := range entries {
		info, err := f.Info()
		if err != nil {
			return r, err
		}
		size += info.Size()
	}
	files := map[string][]byte{r.Source.GLBSHA256 + ".glb": glb, r.Source.SidecarSHA256 + ".json": sidecar}
	for name, b := range files {
		if _, err = os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
			size += int64(len(b))
		} else if err != nil {
			return r, err
		}
	}
	if size > sourceStoreLimit {
		return r, fmt.Errorf("source library exceeds 64 MiB; remove unused imported source pairs before importing")
	}
	for name, b := range files {
		if err = atomicWrite(filepath.Join(dir, name), b); err != nil {
			return r, err
		}
	}
	return r, nil
}
func (e *Engine) sourceBytes(s *ConditionedSource) ([]byte, []byte, error) {
	if s == nil {
		return nil, nil, fmt.Errorf("missing archetype source")
	}
	read := func(digest, ext string, builtin []byte, limit int64) ([]byte, error) {
		if !safeID(digest) {
			return nil, fmt.Errorf("invalid source fingerprint")
		}
		var b []byte
		var err error
		if sourceDigest(builtin) == digest {
			b = builtin
		} else {
			f, openErr := os.Open(filepath.Join(e.Workspace, "sources", digest+ext))
			if openErr != nil {
				err = openErr
			} else {
				b, err = io.ReadAll(io.LimitReader(f, limit+1))
				closeErr := f.Close()
				if err == nil {
					err = closeErr
				}
			}
		}
		if err != nil {
			return nil, fmt.Errorf("source %s%s unavailable: import the original GLB + sidecar into this workspace: %w", digest, ext, err)
		}
		if int64(len(b)) > limit {
			return nil, fmt.Errorf("source %s exceeds %d-byte reload limit", digest, limit)
		}
		if sourceDigest(b) != digest {
			return nil, fmt.Errorf("source fingerprint mismatch: %s", digest)
		}
		return b, nil
	}
	glb, err := read(s.GLBSHA256, ".glb", bridgeGLB, 8<<20)
	if err != nil {
		return nil, nil, err
	}
	builtinSidecar := bridgeSidecar
	if s.SidecarSHA256 == sourceDigest(bridgeReliefSidecar) {
		builtinSidecar = bridgeReliefSidecar
	}
	if s.SidecarSHA256 == sourceDigest(bridgeStructuralSidecar) {
		builtinSidecar = bridgeStructuralSidecar
	}
	sidecar, err := read(s.SidecarSHA256, ".json", builtinSidecar, 64<<10)
	if err != nil {
		return nil, nil, err
	}
	spec, err := ParseConstraintSpec(sidecar)
	if err != nil {
		return nil, nil, err
	}
	if hash(spec) != hash(s.Spec) {
		return nil, nil, fmt.Errorf("sidecar semantics differ from fingerprinted source")
	}
	return glb, sidecar, nil
}

// Recipe inputs are bound through a behavioral interface, keeping the evaluator
// independent of operator names and keeping source files out of numeric params.
type SourceOperator interface {
	BindSource(*ConditionedSource, []byte) Operator
}
type archetypeOperator struct {
	source *ConditionedSource
	glb    []byte
}

func (archetypeOperator) BindSource(s *ConditionedSource, b []byte) Operator {
	return archetypeOperator{s, b}
}
func (op archetypeOperator) Run(ctx context.Context, d Domain, _ uint64, _ Inputs) (Outputs, error) {
	if op.source == nil {
		return nil, fmt.Errorf("archetype operator requires a fingerprinted source")
	}
	mesh, v, err := DecodeArchetypeGLB(ctx, op.glb, op.source.Spec.Transform, d)
	if err != nil {
		return nil, err
	}
	required, forbidden, editable, err := op.source.Spec.Masks(ctx, d)
	if err != nil {
		return nil, err
	}
	return Outputs{"mesh": {Kind: "mesh", Mesh: mesh}, "volume": {Kind: "volume", Volume: v}, "required": {Kind: "volume", Volume: required}, "forbidden": {Kind: "volume", Volume: forbidden}, "editable": {Kind: "volume", Volume: editable}}, nil
}
func registerConditioned(r *Registry) {
	registerStructuralDeck(r)
	if err := r.Register(Descriptor{ID: "floor_relief", Version: "1", Inputs: map[string]Port{"volume": {"volume", "solid"}, "field": {"volume", "solid"}}, Outputs: map[string]Port{"volume": {"volume", "solid"}}, Parameters: map[string]Param{"strength": parameter(1, 0, 1, false, false), "depth": parameter(.06, 0, .06, false, false)}, Requirements: "CPU; source-authorized bounded floor deformation from shared wall field"}, func(p map[string]float64) (Operator, error) {
		return floorReliefOperator{strength: p["strength"], depth: p["depth"]}, nil
	}); err != nil {
		panic(err)
	}

	volume := Port{"volume", "solid"}
	reference := Port{"volume", "reference-solid"}
	required := Port{"volume", "required-solid"}
	forbidden := Port{"volume", "forbidden-solid"}
	editable := Port{"volume", "editable-region"}
	err := r.Register(Descriptor{ID: "archetype", Version: "1", Outputs: map[string]Port{"mesh": {"mesh", "reference"}, "volume": reference, "required": required, "forbidden": forbidden, "editable": editable}, Requirements: "CPU; GLB 8 MiB, convex closed shells only; resolution 32 or 64"}, func(map[string]float64) (Operator, error) { return archetypeOperator{}, nil })
	if err != nil {
		panic(err)
	}
	err = r.Register(Descriptor{ID: "constrain", Version: "1", Inputs: map[string]Port{"reference": reference, "decoration": volume, "required": required, "forbidden": forbidden, "editable": editable}, Outputs: map[string]Port{"volume": volume}, Requirements: "CPU; bounded domain; replace editable regions, retain required, carve forbidden"}, func(map[string]float64) (Operator, error) {
		return OperatorFunc(func(ctx context.Context, _ Domain, _ uint64, i Inputs) (Outputs, error) {
			v, err := ConstrainDecoration(ctx, i["reference"].Volume, i["decoration"].Volume, i["required"].Volume, i["forbidden"].Volume, i["editable"].Volume)
			return Outputs{"volume": {Kind: "volume", Volume: v}}, err
		}), nil
	})
	if err != nil {
		panic(err)
	}
}

// GLB extras retain deterministic provenance with direct downloads as well as ZIPs.
func EncodeCandidateGLB(mesh *Mesh, c *Candidate) ([]byte, error) {
	raw, err := EncodeGLB(mesh)
	if err != nil {
		return nil, err
	}
	if c.Recipe.Source == nil {
		return raw, nil
	}
	provenance := map[string]any{"id": c.ID, "engine": c.Engine, "build": c.Build, "recipe": c.Recipe}
	if c.WalkingChecks != nil {
		provenance["walking_checks"] = c.WalkingChecks
	}
	return glbWithExtras(raw, map[string]any{"progen": provenance})
}

// This only rewrites our own trusted GLB JSON chunk, never imported GLB data.
func glbWithExtras(raw []byte, extras any) ([]byte, error) {
	var doc map[string]any
	length := int(uint32(raw[12]) | uint32(raw[13])<<8 | uint32(raw[14])<<16 | uint32(raw[15])<<24)
	if err := json.Unmarshal(raw[20:20+length], &doc); err != nil {
		return nil, err
	}
	doc["extras"] = extras
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	for len(b)%4 != 0 {
		b = append(b, ' ')
	}
	var out bytes.Buffer
	if err := binary.Write(&out, binary.LittleEndian, []uint32{0x46546c67, 2, uint32(20 + len(b) + len(raw) - (20 + length)), uint32(len(b)), 0x4e4f534a}); err != nil {
		return nil, err
	}
	out.Write(b)
	out.Write(raw[20+length:])
	return out.Bytes(), nil
}
