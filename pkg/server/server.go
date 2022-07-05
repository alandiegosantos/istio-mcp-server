package server

import (
	"context"
	"crypto/md5"
	"fmt"
	"net"
	"sync"

	"github.com/alandiegosantos/istio-mcp/pkg/registry"
	"github.com/alandiegosantos/istio-mcp/pkg/util"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	discovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
)

const (
	versionWithNoResources = "empty-version"
)

type server struct {
	ctx          context.Context
	cancelCtx    context.CancelFunc
	server       *grpc.Server
	healthServer *health.Server
	registry     registry.Registry
	logger       *logrus.Entry

	cachePerTypeUrl map[string]*cacheEntry
	cacheMutex      sync.RWMutex

	clients      map[string]*connection
	clientsMutex sync.RWMutex
}

type cacheEntry struct {
	Version   string
	Resources []*anypb.Any
}

func NewServer(reg registry.Registry, options []grpc.ServerOption) (*server, error) {

	s := &server{
		logger:          util.LoggerFor("server"),
		server:          grpc.NewServer(options...),
		healthServer:    health.NewServer(),
		registry:        reg,
		clients:         make(map[string]*connection),
		cachePerTypeUrl: make(map[string]*cacheEntry),
	}

	grpc_health_v1.RegisterHealthServer(s.server, s.healthServer)

	// Register reflection service on gRPC server.
	reflection.Register(s.server)

	discovery.RegisterAggregatedDiscoveryServiceServer(s.server, s)

	return s, nil

}

func (s *server) Serve(ctx context.Context, listeningAddress string) error {

	ctx, cancel := context.WithCancel(ctx)

	s.logger.Infof("Starting MCP server on tcp://%s", listeningAddress)

	s.ctx = ctx
	s.cancelCtx = cancel

	listener, err := net.Listen("tcp", listeningAddress)
	if err != nil {
		return err
	}

	s.healthServer.SetServingStatus("grpc.health.v1.Health", grpc_health_v1.HealthCheckResponse_SERVING)

	deregister := s.registry.RegisterCallback(s.updateCache)
	defer deregister()

	if err := s.server.Serve(listener); err != nil {
		s.logger.WithError(err).Error("Failed to start the grpc server")
		return err
	}

	return nil

}

func (s *server) Stop() {

	s.logger.Infof("Stopping MCP server")

	if s.cancelCtx != nil {
		s.cancelCtx()
	}
	s.healthServer.SetServingStatus("grpc.health.v1.Health", grpc_health_v1.HealthCheckResponse_NOT_SERVING)
	s.server.Stop()
}

func (s *server) StreamAggregatedResources(stream discovery.AggregatedDiscoveryService_StreamAggregatedResourcesServer) error {

	con := newConnection(stream)
	s.addConnection(con)
	defer s.removeConnection(con)

	ctx := stream.Context()
	go con.receive(ctx)

	var event *changeEvent

	for {
		// TODO: debouncing
		select {
		case event = <-con.pushChannel:
			typeUrl := event.TypeUrl

			version := versionWithNoResources
			resources := []*anypb.Any{}

			s.cacheMutex.RLock()
			if entry, found := s.cachePerTypeUrl[typeUrl]; found {
				version = entry.Version
				resources = entry.Resources
			}
			s.cacheMutex.RUnlock()

			if event.OldVersion == version {
				// If the version is the same,
				// wait for another event
				continue
			}

			response := &discovery.DiscoveryResponse{
				VersionInfo: version,
				Resources:   resources,
				TypeUrl:     typeUrl,
				Nonce:       version,
			}

			con.send(response)

		case <-ctx.Done():
			return nil
		}

	}

}

func (s *server) DeltaAggregatedResources(stream discovery.AggregatedDiscoveryService_DeltaAggregatedResourcesServer) error {
	return status.Errorf(codes.Unimplemented, "method DeltaAggregatedResources not implemented")
}

func (s *server) addConnection(con *connection) {
	s.clientsMutex.Lock()
	defer s.clientsMutex.Unlock()

	s.clients[con.ConID] = con

	s.logger.WithField("connection_id", con.ConID).Info("Receive connection")

	numConnectionsCnt.Add(1)

}

func (s *server) removeConnection(con *connection) {

	s.clientsMutex.Lock()
	defer s.clientsMutex.Unlock()

	delete(s.clients, con.ConID)
	s.logger.WithField("connection_id", con.ConID).Info("Remove connection")

	numConnectionsCnt.Add(-1)

}

func (s *server) updateCache(typeUrl string, resources []*anypb.Any) {

	hash := md5.New()
	for _, resource := range resources {
		hash.Write(resource.Value)
	}
	version := fmt.Sprintf("%x", hash.Sum(nil))

	cacheEntry := &cacheEntry{
		Version:   version,
		Resources: resources,
	}

	s.cacheMutex.Lock()
	oldVersion := versionWithNoResources
	if entry, found := s.cachePerTypeUrl[typeUrl]; found {
		oldVersion = entry.Version
	}

	s.cachePerTypeUrl[typeUrl] = cacheEntry
	s.cacheMutex.Unlock()

	s.clientsMutex.RLock()
	for _, conn := range s.clients {
		conn.pushChannel <- &changeEvent{
			TypeUrl:    typeUrl,
			OldVersion: oldVersion,
		}
	}
	s.clientsMutex.RUnlock()
}
