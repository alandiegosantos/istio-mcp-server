package registry

import (
	"github.com/prometheus/client_golang/prometheus"
)

func init() {
	prometheus.MustRegister(handledEventsCnt, convertionErrorsCnt, debouncedEventsCnt)
}

var (
	handledEventsCnt = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "registry",
		Name:      "handled_events",
		Help:      "Total number of events received by Remote Cluster Watcher",
	}, []string{"event", "kind"})
	convertionErrorsCnt = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "registry",
		Name:      "convertion_errors",
		Help:      "Total number of errors while converting the objects into mcp.Resources",
	}, []string{"type"})
	debouncedEventsCnt = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "registry",
		Name:      "debounced_events",
		Help:      "Number of events debounced in the registry",
	}, []string{"type"})
)
