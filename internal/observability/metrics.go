package observability

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

type Metrics struct {
	mu       sync.Mutex
	counters map[string]int64
	gauges   map[string]float64
}

func New() *Metrics {
	return &Metrics{counters: make(map[string]int64), gauges: make(map[string]float64)}
}

func (metrics *Metrics) Inc(name string) {
	if metrics == nil {
		return
	}
	metrics.mu.Lock()
	metrics.counters[name]++
	metrics.mu.Unlock()
}

func (metrics *Metrics) Set(name string, value float64) {
	if metrics == nil {
		return
	}
	metrics.mu.Lock()
	metrics.gauges[name] = value
	metrics.mu.Unlock()
}

func (metrics *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/plain; version=0.0.4")
		metrics.mu.Lock()
		names := make([]string, 0, len(metrics.counters)+len(metrics.gauges))
		for name := range metrics.counters {
			names = append(names, name)
		}
		for name := range metrics.gauges {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if _, ok := metrics.counters[name]; ok {
				_, _ = fmt.Fprintf(response, "# TYPE %s counter\n%s %d\n", name, name, metrics.counters[name])
			} else {
				_, _ = fmt.Fprintf(response, "# TYPE %s gauge\n%s %g\n", name, name, metrics.gauges[name])
			}
		}
		metrics.mu.Unlock()
	})
}

func (metrics *Metrics) Render() string {
	var builder strings.Builder
	metrics.mu.Lock()
	names := make([]string, 0, len(metrics.counters)+len(metrics.gauges))
	for name := range metrics.counters {
		names = append(names, name)
	}
	for name := range metrics.gauges {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if value, ok := metrics.counters[name]; ok {
			fmt.Fprintf(&builder, "%s %d\n", name, value)
		} else {
			fmt.Fprintf(&builder, "%s %g\n", name, metrics.gauges[name])
		}
	}
	metrics.mu.Unlock()
	return builder.String()
}
