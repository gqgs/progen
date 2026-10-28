package progen

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Tiny valid solids isolate graph measurement from generation and meshing.
func graphMetrics(t *testing.T, parents []int) Metrics {
	t.Helper()
	r := smallRecipe()
	r.Domain.Resolution = 8
	g := &Graph{Space: r.Domain}
	for _, p := range parents {
		g.Vertices = append(g.Vertices, Vertex{Parent: p, Radius: .01})
	}
	values := make([]float64, 512)
	for i := range values {
		values[i] = 1
	}
	values[3+8*(3+8*3)] = -1
	mesh := &Mesh{Positions: []Vec3{{0, 0, 0}, {.1, 0, 0}, {0, .1, 0}}, Normals: []Vec3{{0, 0, 1}, {0, 0, 1}, {0, 0, 1}}, Indices: []uint32{0, 1, 2}}
	v := &VolumeGrid{Space: r.Domain, Values: values}
	m, problems, err := (DefaultEvaluator{}).Evaluate(context.Background(), r, map[string]Artifact{
		"graph": {Graph: g}, "mesh": {Mesh: mesh}, "volume": {Volume: v}, "base": {Volume: v},
	})
	if err != nil || len(problems) != 0 {
		t.Fatalf("evaluation: %v %v", err, problems)
	}
	return m
}

func TestTreeDescriptorBounds(t *testing.T) {
	for n := 1; n <= 9; n++ {
		parents := make([]int, n)
		parents[0] = -1
		maxB := 0
		var visit func(int)
		visit = func(i int) {
			if i < n {
				for p := 0; p < i; p++ {
					parents[i] = p
					visit(i + 1)
				}
				return
			}
			children := make([]int, n)
			for _, p := range parents[1:] {
				children[p]++
			}
			b := 0
			for _, c := range children {
				if c > 1 {
					b++
				}
			}
			maxB = max(maxB, b)
			raw := float64(b) / float64(n)
			x, y := (MorphologySpace{}).Coordinates(Metrics{BranchFraction: raw, Porosity: .25})
			if 2*b > n-1 || raw >= .5 || x < 0 || x >= 1 || y != .25 {
				t.Fatalf("bounds: N=%d B=%d x=%g", n, b, x)
			}
		}
		visit(1)
		if maxB != (n-1)/2 {
			t.Fatalf("maximum N=%d: %d", n, maxB)
		}
	}
	for _, parents := range [][]int{{-1}, {-1, 0, 1, 2}, {-1, 0, 0, 0}, {-1, 0, 0, 1, 1, 2, 2}} {
		m := graphMetrics(t, parents)
		children := make([]int, len(parents))
		for _, p := range parents[1:] {
			children[p]++
		}
		b := 0
		for _, c := range children {
			if c > 1 {
				b++
			}
		}
		if m.BranchFraction != float64(b)/float64(len(parents)) {
			t.Fatal("raw metric changed", m)
		}
	}
	// A valid full binary tree with 33 vertices reaches the last scaled column.
	parents := make([]int, 33)
	parents[0] = -1
	for i := 1; i < len(parents); i++ {
		parents[i] = (i - 1) / 2
	}
	m := graphMetrics(t, parents)
	x, _ := (MorphologySpace{}).Coordinates(m)
	if bin, ok := descriptorBin(x, 16); !ok || bin != 15 {
		t.Fatalf("last column: %g %d", x, bin)
	}
	// Construct witnesses for every column, including the synthetic grid's B/N
	// values: a binary tree with B branch points, extended by a unary tail.
	for col := 0; col < 16; col++ {
		b := 2*col + 1
		parents := make([]int, 65)
		parents[0] = -1
		for i := 1; i < len(parents); i++ {
			parents[i] = i - 1
			if i <= 2*b {
				parents[i] = (i - 1) / 2
			}
		}
		m := graphMetrics(t, parents)
		x, _ := (MorphologySpace{}).Coordinates(m)
		if bin, ok := descriptorBin(x, 16); !ok || bin != col {
			t.Fatalf("column witness %d: %g -> %d", col, x, bin)
		}
	}
}

func TestDescriptorBinning(t *testing.T) {
	for i := 0; i <= 16; i++ {
		x := float64(i) / 16
		if got, ok := descriptorBin(x, 16); !ok || got != min(15, i) {
			t.Fatalf("boundary %g: %d", x, got)
		}
		if i > 0 {
			if got, _ := descriptorBin(math.Nextafter(x, 0), 16); got != i-1 {
				t.Fatalf("below boundary %g: %d", x, got)
			}
		}
	}
	for _, x := range []float64{-1, math.Nextafter(1, 2), math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, ok := descriptorBin(x, 16); ok {
			t.Fatalf("accepted %g", x)
		}
	}
}

func saveFixture(t *testing.T, e *Engine, c Candidate) string {
	t.Helper()
	path := filepath.Join(e.Workspace, "candidates", c.ID, "candidate.json")
	if err := writeJSON(path, c); err != nil {
		t.Fatal(err)
	}
	return path
}
func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestArchiveRebuildAndCompatibility(t *testing.T) {
	e := testEngine(t)
	r := smallRecipe()
	candidates := []Candidate{}
	// 16 synthetic B/N values attainable at N=65, across all 16 porosity bins.
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			c := Candidate{ID: fmt.Sprintf("%064x", 1+x+16*y), Recipe: r, Valid: true, Feedback: "neutral", Metrics: Metrics{BranchFraction: float64(2*x+1) / 65, Porosity: float64(y) / 16, ConnectedFraction: .9}}
			candidates = append(candidates, c)
		}
	}
	favorite := candidates[0]
	favorite.ID = fmt.Sprintf("%064x", 1000)
	favorite.Feedback = "favorite"
	favorite.Metrics.ConnectedFraction = .1
	rejected := candidates[1]
	rejected.ID = fmt.Sprintf("%064x", 1001)
	rejected.Feedback = "rejected"
	rejected.Metrics.ConnectedFraction = 1
	invalid := candidates[2]
	invalid.ID = fmt.Sprintf("%064x", 1002)
	invalid.Valid = false
	invalid.Feedback = "favorite"
	candidates = append(candidates, favorite, rejected, invalid)
	snapshots := map[string][]byte{}
	for _, c := range candidates {
		path := saveFixture(t, e, c)
		snapshots[path] = readBytes(t, path)
	}
	e.Descriptors = LegacyMorphologySpace{}
	before, err := e.Archive(r)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an original archive with no version or range metadata.
	old := map[string]any{"id": before.ID, "bins": 16, "axes": before.Axes, "cells": before.Cells}
	archivePath := filepath.Join(e.Workspace, "archives", before.ID+".json")
	if err = writeJSON(archivePath, old); err != nil {
		t.Fatal(err)
	}
	runPath := filepath.Join(e.Workspace, "runs", "historical.json")
	if err = writeJSON(runPath, map[string]any{"initial_archive": old}); err != nil {
		t.Fatal(err)
	}
	snapshots[runPath] = readBytes(t, runPath)
	e.Descriptors = nil
	after, err := e.Archive(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Cells) != 128 || len(after.Cells) != 256 {
		t.Fatalf("synthetic occupancy: %d -> %d", len(before.Cells), len(after.Cells))
	}
	t.Logf("SYNTHETIC identical records (%d total): %d/256 -> %d/256; not a generation benchmark", len(candidates), len(before.Cells), len(after.Cells))
	var legacy Archive
	if err = json.Unmarshal(readBytes(t, archivePath), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Compatible(after) || before.Compatible(after) || !after.Compatible(after) {
		t.Fatal("version compatibility")
	}
	if after.DescriptorVersion != "morphology-v2" || after.Ranges[0] != (DescriptorRange{0, 1, true}) {
		t.Fatal("missing descriptor metadata")
	}
	if after.Cells["0,0"] != favorite.ID || after.Cells["0,1"] == rejected.ID {
		t.Fatal("feedback ranking lost")
	}
	if err = e.RebuildArchives(); err != nil {
		t.Fatal(err)
	}
	first := readBytes(t, archivePath)
	var rebuilt Archive
	if err = json.Unmarshal(first, &rebuilt); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, rebuilt) {
		t.Fatal("rebuild differs from fresh archive")
	}
	if err = e.RebuildArchives(); err != nil {
		t.Fatal(err)
	}
	if string(first) != string(readBytes(t, archivePath)) {
		t.Fatal("non-idempotent rebuild")
	}
	for path, want := range snapshots {
		if string(want) != string(readBytes(t, path)) {
			t.Fatalf("rebuild rewrote %s", path)
		}
	}
	// Reopen preserves all decisions; no artifact loading or reevaluation is needed.
	reopened, err := NewEngine(e.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.RebuildArchives(); err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Archive(r)
	if err != nil || !reflect.DeepEqual(got, after) {
		t.Fatal("restart", err)
	}
	// Record order cannot change tie winners or membership.
	for i := len(candidates) - 1; i >= 0; i-- {
		c := candidates[i]
		c.Created = c.Created.Add(1)
		saveFixture(t, e, c)
	}
	got, err = e.Archive(r)
	if err != nil || !reflect.DeepEqual(got, after) {
		t.Fatal("order-dependent archive", err)
	}
}

type customCoordinates struct{}

func (customCoordinates) Coordinates(m Metrics) (float64, float64) {
	return m.Porosity, m.BranchFraction
}
func TestCustomDescriptors(t *testing.T) {
	e := testEngine(t)
	e.Descriptors = customCoordinates{}
	c := Candidate{ID: fmt.Sprintf("%064x", 1), Recipe: smallRecipe(), Valid: true, Metrics: Metrics{Porosity: 1, BranchFraction: .25}}
	saveFixture(t, e, c)
	a, err := e.Archive(c.Recipe)
	if err != nil {
		t.Fatal(err)
	}
	if a.Cells["15,4"] != c.ID || a.DescriptorVersion != "custom-unversioned" || a.Compatible(a) {
		t.Fatal("custom coordinates mislabeled or scaled", a)
	}
}

// A small controlled generation run, distinct from the synthetic occupancy grid
// and from any user's collection. Compare both scales on the exact same records.
func TestGeneratedRecordOccupancyComparison(t *testing.T) {
	e := testEngine(t)
	r := smallRecipe()
	generated, err := e.Run(context.Background(), RunRequest{Kind: "generate", Recipe: &r, Count: 16, SearchSeed: 42, Workers: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	valid := 0
	for _, c := range generated {
		if c.Valid {
			valid++
		}
	}
	e.Descriptors = LegacyMorphologySpace{}
	before, err := e.Archive(r)
	if err != nil {
		t.Fatal(err)
	}
	e.Descriptors = nil
	after, err := e.Archive(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("MEASURED controlled generation: smallRecipe resolution=20, attractions=70, iterations=60, max_vertices=300, batch=16, search_seed=42; valid=%d; same records: v1=%d/256 v2=%d/256", valid, len(before.Cells), len(after.Cells))
}
