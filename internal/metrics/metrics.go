// Package metrics provides a dependency-free metrics registry for agent runs.
package metrics

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Histogram struct {
	Count int64   `json:"count"`
	Sum   float64 `json:"sum"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
}

type Snapshot struct {
	GeneratedAt time.Time            `json:"generated_at"`
	Counters    map[string]float64   `json:"counters"`
	Gauges      map[string]float64   `json:"gauges"`
	Histograms  map[string]Histogram `json:"histograms"`
}

type Registry struct {
	mu         sync.RWMutex
	enabled    bool
	counters   map[string]float64
	gauges     map[string]float64
	histograms map[string]Histogram
}

var defaultRegistry = NewRegistry()

func NewRegistry() *Registry {
	return &Registry{
		enabled:    strings.TrimSpace(os.Getenv("EPHEMERA_METRICS")) != "",
		counters:   map[string]float64{},
		gauges:     map[string]float64{},
		histograms: map[string]Histogram{},
	}
}

func Default() *Registry { return defaultRegistry }

func (r *Registry) SetEnabled(enabled bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.enabled = enabled
	r.mu.Unlock()
}

func (r *Registry) Enabled() bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	enabled := r.enabled
	r.mu.RUnlock()
	return enabled
}

func (r *Registry) Inc(name string) { r.Add(name, 1) }

func (r *Registry) Add(name string, value float64) {
	if r == nil || !r.Enabled() || strings.TrimSpace(name) == "" {
		return
	}
	r.mu.Lock()
	r.counters[name] += value
	r.mu.Unlock()
}

func (r *Registry) Set(name string, value float64) {
	if r == nil || !r.Enabled() || strings.TrimSpace(name) == "" {
		return
	}
	r.mu.Lock()
	r.gauges[name] = value
	r.mu.Unlock()
}

func (r *Registry) Observe(name string, value float64) {
	if r == nil || !r.Enabled() || strings.TrimSpace(name) == "" {
		return
	}
	r.mu.Lock()
	h := r.histograms[name]
	if h.Count == 0 || value < h.Min {
		h.Min = value
	}
	if h.Count == 0 || value > h.Max {
		h.Max = value
	}
	h.Count++
	h.Sum += value
	r.histograms[name] = h
	r.mu.Unlock()
}

func (r *Registry) Snapshot() Snapshot {
	result := Snapshot{
		GeneratedAt: time.Now().UTC(),
		Counters:    map[string]float64{},
		Gauges:      map[string]float64{},
		Histograms:  map[string]Histogram{},
	}
	if r == nil {
		return result
	}
	r.mu.RLock()
	for key, value := range r.counters {
		result.Counters[key] = value
	}
	for key, value := range r.gauges {
		result.Gauges[key] = value
	}
	for key, value := range r.histograms {
		result.Histograms[key] = value
	}
	r.mu.RUnlock()
	return result
}

// WriteJSON atomically exports the current metrics snapshot.
func (r *Registry) WriteJSON(path string) error {
	if r == nil || !r.Enabled() {
		return nil
	}
	data, err := json.MarshalIndent(r.Snapshot(), "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Prometheus renders a text exposition format suitable for stdout scraping or
// a tiny HTTP adapter without pulling a full metrics dependency graph.
func (r *Registry) Prometheus() string {
	snapshot := r.Snapshot()
	var lines []string
	keys := make([]string, 0, len(snapshot.Counters))
	for key := range snapshot.Counters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("%s %g", sanitizeName(key), snapshot.Counters[key]))
	}
	keys = keys[:0]
	for key := range snapshot.Gauges {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("%s %g", sanitizeName(key), snapshot.Gauges[key]))
	}
	keys = keys[:0]
	for key := range snapshot.Histograms {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		h := snapshot.Histograms[key]
		name := sanitizeName(key)
		lines = append(lines,
			fmt.Sprintf("%s_count %d", name, h.Count),
			fmt.Sprintf("%s_sum %g", name, h.Sum),
			fmt.Sprintf("%s_min %g", name, h.Min),
			fmt.Sprintf("%s_max %g", name, h.Max),
		)
	}
	return strings.Join(lines, "\n")
}

func sanitizeName(value string) string {
	var b strings.Builder
	for index, r := range value {
		valid := r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || index > 0 && r >= '0' && r <= '9'
		if valid {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}
