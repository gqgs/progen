// Command rebuild-archives migrates derived archive membership without generation.
package main

import (
	"flag"
	"fmt"
	"log"

	"progen"
)

func main() {
	workspace := flag.String("workspace", ".progen", "existing workspace (stop writers before rebuilding)")
	flag.Parse()
	e, err := progen.NewEngine(*workspace)
	if err != nil {
		log.Fatal(err)
	}
	all, err := e.List()
	if err != nil {
		log.Fatal(err)
	}
	seen := map[string]bool{}
	type comparison struct {
		id            string
		before, after int
	}
	comparisons := []comparison{}
	for _, c := range all {
		e.Descriptors = nil
		after, err := e.Archive(c.Recipe)
		if err != nil {
			log.Fatal(err)
		}
		if seen[after.ID] {
			continue
		}
		seen[after.ID] = true
		e.Descriptors = progen.LegacyMorphologySpace{}
		before, err := e.Archive(c.Recipe)
		if err != nil {
			log.Fatal(err)
		}
		comparisons = append(comparisons, comparison{after.ID, len(before.Cells), len(after.Cells)})
	}
	e.Descriptors = nil
	if err = e.RebuildArchives(); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Existing records: %d. No generation or candidate edits performed.\n", len(all))
	for _, c := range comparisons {
		fmt.Printf("%s: morphology-v1 %d/256 -> morphology-v2 %d/256\n", c.id, c.before, c.after)
	}
}
