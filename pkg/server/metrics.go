package server

import "github.com/prometheus/client_golang/prometheus"

func init() {
	prometheus.MustRegister(numWatchesCnt)
}

var (
	numWatchesCnt = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "grpc_server",
		Name:      "num_watches",
		Help:      "Number of watches per resource",
	}, []string{"kind"})
	numConnectionsCnt = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "grpc_server",
		Name:      "num_connections",
		Help:      "Number of connections",
	})
)
