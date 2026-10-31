// Command progen runs the local studio and headless generation workflows.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"progen"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: progen serve|recipe|operators|validate|generate|vary|search|export [flags]")
	}
	command := os.Args[1]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	workspace := flags.String("workspace", ".progen", "local workspace")
	addr := flags.String("addr", "127.0.0.1:8080", "server address")
	recipePath := flags.String("recipe", "", "recipe JSON path")
	count := flags.Int("count", 0, "number of candidates")
	scale := flags.Float64("scale", .15, "variation scale")
	workers := flags.Int("workers", 1, "generation workers")
	searchSeed := flags.Uint64("search-seed", 0, "parameter search seed")
	parent := flags.String("parent", "", "parent candidate ID")
	id := flags.String("id", "", "candidate ID to export")
	out := flags.String("out", "specimen.zip", "export path")
	if err := flags.Parse(os.Args[2:]); err != nil {
		return err
	}
	e, err := progen.NewEngine(*workspace)
	if err != nil {
		return err
	}
	recipe := progen.DefaultRecipe()
	if *recipePath != "" {
		b, err := os.ReadFile(*recipePath)
		if err != nil {
			return err
		}
		recipe = progen.Recipe{}
		if err := json.Unmarshal(b, &recipe); err != nil {
			return err
		}
	}
	printJSON := func(value any) error {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(value)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch command {
	case "recipe", "validate":
		p, _, err := e.Registry.Compile(recipe)
		if err != nil {
			return err
		}
		return printJSON(p)
	case "operators":
		return printJSON(e.Registry.Descriptors())
	case "generate", "vary", "search":
		results, err := e.Run(ctx, progen.RunRequest{Kind: command, Recipe: &recipe, Parent: *parent, Count: *count, Scale: *scale, SearchSeed: progen.Seed(*searchSeed), Workers: *workers}, nil)
		if err != nil {
			return err
		}
		return printJSON(results)
	case "export":
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		defer f.Close()
		return e.Export(ctx, *id, f)
	case "serve":
		s, err := progen.NewServer(e)
		if err != nil {
			return err
		}
		defer s.Close()
		server := &http.Server{Addr: *addr, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			<-ctx.Done()
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			server.Shutdown(shutdown)
		}()
		log.Printf("Progen: http://%s", *addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}
