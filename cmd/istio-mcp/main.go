package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alandiegosantos/istio-mcp/pkg/registry"
	"github.com/alandiegosantos/istio-mcp/pkg/server"
	"github.com/alandiegosantos/istio-mcp/pkg/util"
	grpc_logrus "github.com/grpc-ecosystem/go-grpc-middleware/logging/logrus"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
	"k8s.io/apimachinery/pkg/labels"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	DefaultTime                 = 1 * time.Minute
	DefaultTimeout              = 5 * time.Minute
	MaxServerConnectionAge      = 30 * time.Minute
	MaxServerConnectionAgeGrace = 5 * time.Minute
	MaxRecvMsgSize              = 10 * 1024 * 1024 // 10 MB
	MaxStreams                  = 1024
)

func main() {

	logger := util.LoggerFor("main")

	grpcAddr := flag.String("grpc-addr", ":15013", "address to listen for grpc requests")
	resourcesSelector := flag.String("selector", "istio-mcp-sync=true", "selector used to filter resources to sync")
	clusterDomain := flag.String("cluster-domain", "cluster.local", "domain of the k8s cluster")
	flag.Parse()

	// Validating parameters
	selector, err := labels.Parse(*resourcesSelector)
	if err != nil {
		logger.WithError(err).Fatal("selector contains an invalid value")
	}

	if len(*clusterDomain) == 0 {
		logger.Fatal("--cluster-domain cannot be \"\"")
	}

	ctx, cancel := context.WithCancel(context.Background())

	g, ctx := errgroup.WithContext(ctx)

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, syscall.SIGINT, syscall.SIGTERM)

	config := ctrl.GetConfigOrDie()

	reg, err := registry.NewRegistry(config, selector, *clusterDomain)
	if err != nil {
		logger.WithError(err).Fatal("Failed to create registry")
	}

	grpcServer, err := server.NewServer(reg, defaultServerOptions())
	if err != nil {
		logger.WithError(err).Fatal("Failed to create MCP server")
	}

	g.Go(func() error {
		return grpcServer.Serve(ctx, *grpcAddr)
	})

	g.Go(func() error {
		return reg.Start(ctx)
	})

	select {
	case <-interrupt:
	case <-ctx.Done():
	}

	cancel()

	if grpcServer != nil {
		grpcServer.Stop()
	}

	if err := g.Wait(); err != nil {
		logrus.WithError(err).Fatal("Fatal error occurred")
	}
}

func defaultServerOptions() []grpc.ServerOption {

	grpcOptions := []grpc.ServerOption{
		grpc.MaxConcurrentStreams(uint32(MaxStreams)),
		grpc.MaxRecvMsgSize(MaxRecvMsgSize),
		// Ensure we allow clients sufficient ability to send keep alives. If this is higher than client
		// keep alive setting, it will prematurely get a GOAWAY sent.
		grpc.ChainUnaryInterceptor(grpc_logrus.UnaryServerInterceptor(util.LoggerFor("grpc"))),
		grpc.StreamInterceptor(grpc_logrus.StreamServerInterceptor(util.LoggerFor("grpc"))),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime: DefaultTime / 2,
		}),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:                  DefaultTime,
			Timeout:               DefaultTimeout,
			MaxConnectionAge:      MaxServerConnectionAge,
			MaxConnectionAgeGrace: MaxServerConnectionAgeGrace,
		}),
	}

	return grpcOptions

}
