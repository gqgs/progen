package progen

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	mathrand "math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed web/dist/*
var webFiles embed.FS

type Job struct {
	ID         string     `json:"id"`
	Request    RunRequest `json:"request"`
	Status     string     `json:"status"`
	Completed  int        `json:"completed"`
	Skipped    int        `json:"skipped"`
	Total      int        `json:"total"`
	Stage      string     `json:"stage"`
	Candidates []string   `json:"candidates"`
	Failures   []CandidateFailure `json:"failures,omitempty"`
	Error      string     `json:"error,omitempty"`
	Created    time.Time  `json:"created"`
}
type Server struct {
	Engine  *Engine
	mu      sync.Mutex
	jobs    map[string]*Job
	cancels map[string]context.CancelFunc
	gate    chan struct{}
	wg      sync.WaitGroup
}

func NewServer(e *Engine) (*Server, error) {
	s := &Server{Engine: e, jobs: map[string]*Job{}, cancels: map[string]context.CancelFunc{}, gate: make(chan struct{}, 1)}
	entries, err := os.ReadDir(filepath.Join(e.Workspace, "jobs"))
	if err != nil {
		return nil, err
	}
	for _, f := range entries {
		if !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(e.Workspace, "jobs", f.Name()))
		if err != nil {
			return nil, err
		}
		var j Job
		if err = json.Unmarshal(b, &j); err != nil {
			return nil, err
		}
		if j.Status == "running" || j.Status == "queued" {
			j.Status = "interrupted"
			j.Error = "Server stopped before this job completed; rerun to reuse cached work."
			if err = writeJSON(filepath.Join(e.Workspace, "jobs", f.Name()), j); err != nil {
				return nil, err
			}
		}
		s.jobs[j.ID] = &j
	}
	return s, nil
}
func (s *Server) Close() {
	s.mu.Lock()
	for _, cancel := range s.cancels {
		cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, status int, e error) {
	respond(w, status, map[string]string{"error": e.Error()})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err == nil {
		return fmt.Errorf("multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// Snapshot events also recover missed completions after reconnecting.
	mux.HandleFunc("GET /api/events", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			problem(w, 500, fmt.Errorf("streaming unavailable"))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		previous := ""
		heartbeat := 0
		for {
			candidates, err := s.Engine.List()
			if err != nil {
				return
			}
			payload, err := json.Marshal(candidates)
			if err != nil {
				return
			}
			current := string(payload)
			if current != previous {
				if _, err := fmt.Fprintf(w, "data: %s\n\n", current); err != nil {
					return
				}
				previous = current
				flusher.Flush()
			} else if heartbeat%60 == 0 {
				if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
					return
				}
				flusher.Flush()
			}
			heartbeat++
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
			}
		}
	})
	mux.HandleFunc("GET /api/operators", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, s.Engine.Registry.Descriptors()) })
	mux.HandleFunc("GET /api/recipes", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, RecipePresets()) })
	mux.HandleFunc("GET /api/recipe", func(w http.ResponseWriter, r *http.Request) {
		p, _, err := s.Engine.Registry.Compile(DefaultRecipe())
		if err != nil {
			problem(w, 500, err)
			return
		}
		respond(w, 200, p)
	})
	mux.HandleFunc("POST /api/validate", func(w http.ResponseWriter, r *http.Request) {
		var recipe Recipe
		if err := decode(w, r, &recipe); err != nil {
			problem(w, 400, err)
			return
		}
		p, _, err := s.Engine.Registry.Compile(recipe)
		if err != nil {
			problem(w, 400, err)
			return
		}
		respond(w, 200, p)
	})
	mux.HandleFunc("POST /api/recipe/randomize", func(w http.ResponseWriter, r *http.Request) {
		var recipe Recipe
		if err := decode(w, r, &recipe); err != nil {
			problem(w, 400, err)
			return
		}
		p, _, err := s.Engine.Registry.Compile(recipe)
		if err != nil {
			problem(w, 400, err)
			return
		}
		var entropy [16]byte
		if _, err := rand.Read(entropy[:]); err != nil {
			problem(w, 500, err)
			return
		}
		rng := mathrand.New(mathrand.NewPCG(binary.LittleEndian.Uint64(entropy[:8]), binary.LittleEndian.Uint64(entropy[8:])))
		p.Seed = Seed(rng.Uint64())
		for i, node := range p.Nodes {
			for key, schema := range s.Engine.Registry.entries[node.Operator].desc.Parameters {
				value := schema.Min + rng.Float64()*(schema.Max-schema.Min)
				if schema.Type == "integer" {
					value = schema.Min + float64(rng.IntN(int(schema.Max-schema.Min)+1))
					value = math.Round(value)
				}
				p.Nodes[i].Params[key] = value
			}
		}
		p.Domain.Resolution = []int{32, 64, 96, 128}[rng.IntN(4)]
		p, _, err = s.Engine.Registry.Compile(p)
		if err != nil {
			problem(w, 400, err)
			return
		}
		respond(w, 200, p)
	})
	mux.HandleFunc("POST /api/candidates/reject-unkept", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.cancels) > 0 {
			problem(w, 409, fmt.Errorf("wait for active jobs before bulk rejection"))
			return
		}
		count, err := s.Engine.RejectUnkept(nil)
		if err != nil {
			problem(w, 500, err)
			return
		}
		s.reloadJobs()
		respond(w, 200, map[string]int{"rejected": count})
	})
	mux.HandleFunc("DELETE /api/generations", func(w http.ResponseWriter, r *http.Request) {
		var confirmation struct {
			Confirm string `json:"confirm"`
		}
		if err := decode(w, r, &confirmation); err != nil || confirmation.Confirm != "purge-all-generations" {
			problem(w, 400, fmt.Errorf("explicit purge confirmation required"))
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.cancels) > 0 {
			problem(w, 409, fmt.Errorf("cancel or finish active jobs before purging"))
			return
		}
		if err := s.Engine.Purge(); err != nil {
			problem(w, 500, err)
			return
		}
		s.jobs = map[string]*Job{}
		respond(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/candidates", func(w http.ResponseWriter, r *http.Request) {
		c, err := s.Engine.List()
		if err != nil {
			problem(w, 500, err)
			return
		}
		respond(w, 200, c)
	})
	mux.HandleFunc("GET /api/candidates/{id}", func(w http.ResponseWriter, r *http.Request) {
		c, err := s.Engine.Get(r.PathValue("id"))
		if err != nil {
			problem(w, 404, err)
			return
		}
		respond(w, 200, c)
	})
	mux.HandleFunc("POST /api/candidates/{id}/feedback", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			State string `json:"state"`
		}
		if err := decode(w, r, &v); err != nil {
			problem(w, 400, err)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if v.State == "rejected" && len(s.cancels) > 0 {
			problem(w, 409, fmt.Errorf("finish or cancel the batch before permanently rejecting specimens"))
			return
		}
		if err := s.Engine.Feedback(r.PathValue("id"), v.State); err != nil {
			problem(w, 400, err)
			return
		}
		s.reloadJobs()
		respond(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/candidates/{id}/asset.glb", func(w http.ResponseWriter, r *http.Request) {
		c, err := s.Engine.Get(r.PathValue("id"))
		if err != nil || !c.Valid {
			problem(w, 404, fmt.Errorf("valid candidate not found"))
			return
		}
		w.Header().Set("Content-Type", "model/gltf-binary")
		http.ServeFile(w, r, filepath.Join(s.Engine.Workspace, "candidates", c.ID, "asset.glb"))
	})
	mux.HandleFunc("GET /api/candidates/{id}/artifacts/{name}", func(w http.ResponseWriter, r *http.Request) {
		c, err := s.Engine.Get(r.PathValue("id"))
		if err != nil {
			problem(w, 404, err)
			return
		}
		a, err := s.Engine.Artifact(c, r.PathValue("name"))
		if err != nil {
			problem(w, 404, err)
			return
		}
		respond(w, 200, a)
	})
	mux.HandleFunc("GET /api/candidates/{id}/export.zip", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		c, err := s.Engine.Get(id)
		if err != nil || !c.Valid {
			problem(w, 404, fmt.Errorf("valid candidate not found"))
			return
		}
		f, err := os.CreateTemp(filepath.Join(s.Engine.Workspace), ".export-*.zip")
		if err != nil {
			problem(w, 500, err)
			return
		}
		defer os.Remove(f.Name())
		defer f.Close()
		if err = s.Engine.Export(r.Context(), id, f); err != nil {
			problem(w, 500, err)
			return
		}
		if _, err = f.Seek(0, 0); err != nil {
			problem(w, 500, err)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=progen-%s.zip", id[:12]))
		http.ServeContent(w, r, "export.zip", time.Time{}, f)
	})
	mux.HandleFunc("GET /api/archive", func(w http.ResponseWriter, r *http.Request) {
		recipe := DefaultRecipe()
		if presetID := r.URL.Query().Get("preset"); presetID != "" {
			found := false
			for _, preset := range RecipePresets() {
				if preset.ID == presetID { recipe = preset.Recipe; found = true; break }
			}
			if !found { problem(w, 400, fmt.Errorf("unknown preset")); return }
		}
		if id := r.URL.Query().Get("candidate"); id != "" {
			c, err := s.Engine.Get(id)
			if err != nil {
				problem(w, 404, err)
				return
			}
			recipe = c.Recipe
		}
		a, err := s.Engine.Archive(recipe)
		if err != nil {
			problem(w, 500, err)
			return
		}
		respond(w, 200, a)
	})
	mux.HandleFunc("POST /api/jobs", s.startJob)
	mux.HandleFunc("GET /api/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		j, ok := s.jobs[r.PathValue("id")]
		if !ok {
			problem(w, 404, fmt.Errorf("unknown job"))
			return
		}
		respond(w, 200, j)
	})
	mux.HandleFunc("GET /api/jobs", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		jobs := []Job{}
		for _, j := range s.jobs {
			jobs = append(jobs, *j)
		}
		respond(w, 200, jobs)
	})
	mux.HandleFunc("DELETE /api/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		cancel, ok := s.cancels[r.PathValue("id")]
		if !ok {
			problem(w, 404, fmt.Errorf("active job not found"))
			return
		}
		cancel()
		respond(w, 202, map[string]bool{"ok": true})
	})
	files, _ := fs.Sub(webFiles, "web/dist")
	mux.Handle("/", http.FileServer(http.FS(files)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		if r.Method != "GET" && r.Method != "HEAD" {
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || u.Host != r.Host {
					problem(w, 403, fmt.Errorf("cross-origin writes are not allowed"))
					return
				}
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func (s *Server) startJob(w http.ResponseWriter, r *http.Request) {
	var req RunRequest
	if err := decode(w, r, &req); err != nil {
		problem(w, 400, err)
		return
	}
	req, recipe, err := s.Engine.Prepare(req)
	if err != nil {
		problem(w, 400, err)
		return
	}
	req.Recipe = &recipe
	if req.Kind == "vary" {
		c, err := s.Engine.Get(req.Parent)
		if err != nil || c.Feedback != "favorite" {
			problem(w, 400, fmt.Errorf("keep the parent specimen before exploring variations"))
			return
		}
	}
	s.mu.Lock()
	if len(s.cancels) == 0 {
		if _, err := s.Engine.RejectUnkept(nil); err != nil {
			s.mu.Unlock()
			problem(w, 500, err)
			return
		}
		s.reloadJobs()
	}
	if len(s.cancels) >= 8 {
		s.mu.Unlock()
		problem(w, 429, fmt.Errorf("job queue is full"))
		return
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		s.mu.Unlock()
		problem(w, 500, err)
		return
	}
	id := hex.EncodeToString(random[:])
	j := &Job{ID: id, Request: req, Status: "queued", Total: req.Count, Created: time.Now().UTC(), Candidates: []string{}}
	if err = writeJSON(filepath.Join(s.Engine.Workspace, "jobs", id+".json"), j); err != nil {
		s.mu.Unlock()
		problem(w, 500, err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.jobs[id] = j
	s.cancels[id] = cancel
	s.wg.Add(1)
	respond(w, 202, j)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		defer cancel()
		select {
		case s.gate <- struct{}{}:
			defer func() { <-s.gate }()
		case <-ctx.Done():
		}
		s.mu.Lock()
		j.Status = "running"
		writeJSON(filepath.Join(s.Engine.Workspace, "jobs", id+".json"), j)
		s.mu.Unlock()
		results, failures, runErr := s.Engine.RunWithFailures(ctx, req, func(done, total int, node string) {
			s.mu.Lock()
			defer s.mu.Unlock()
			j.Completed = done
			if node == "skipped" { j.Skipped++ }
			j.Total = total
			j.Stage = node
		})
		s.mu.Lock()
		defer s.mu.Unlock()
		j.Status = "completed"
		j.Failures = failures
		j.Skipped = len(failures)
		if runErr != nil {
			j.Status = "failed"
			j.Error = runErr.Error()
			if errors.Is(runErr, context.Canceled) {
				j.Status = "cancelled"
			}
		}
		for _, c := range results {
			if c != nil {
				j.Candidates = append(j.Candidates, c.ID)
			}
		}
		delete(s.cancels, id)
		if err := writeJSON(filepath.Join(s.Engine.Workspace, "jobs", id+".json"), j); err != nil {
			j.Status = "failed"
			j.Error = err.Error()
		}
	}()
}

// Caller holds s.mu; records are already validated by the deletion operation.
func (s *Server) reloadJobs() {
	if len(s.cancels) > 0 {
		return
	}
	for id := range s.jobs {
		data, err := os.ReadFile(filepath.Join(s.Engine.Workspace, "jobs", id+".json"))
		if os.IsNotExist(err) {
			delete(s.jobs, id)
			continue
		}
		if err == nil {
			var j Job
			if json.Unmarshal(data, &j) == nil {
				s.jobs[id] = &j
			}
		}
	}
}
