package server

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alandiegosantos/istio-mcp/pkg/util"
	discovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/peer"
)

const pushChannelBufferSize = 1000

type changeEvent struct {
	// TypeUrl of the change Event
	TypeUrl string
	// OldVersion before the event
	OldVersion string
}

// Connection holds information about connected client.
type connection struct {
	sync.RWMutex

	// ConID is the connection identifier, used as a key in the connection table.
	// Currently based on the sink id, peer addr and a counter.
	ConID string

	// PeerAddr is the address of the client, from network layer.
	PeerAddr string

	// Time of connection, for debugging
	Connect time.Time

	// Internal logger
	logger *logrus.Entry

	pushChannel chan *changeEvent

	// MCP stream implement this interface
	stream discovery.AggregatedDiscoveryService_StreamAggregatedResourcesServer
	// LastResponse stores the last response nonce to each sink
	LastResponse map[string]string
}

var (
	// Tracks connections, increment on each new connection.
	connectionNumber = int64(0)
)

func newConnection(stream discovery.AggregatedDiscoveryService_StreamAggregatedResourcesServer) *connection {

	ctx := stream.Context()
	peerAddr := "0.0.0.0"
	if peerInfo, ok := peer.FromContext(ctx); ok {
		peerAddr = peerInfo.Addr.String()
	}

	id := atomic.AddInt64(&connectionNumber, 1)
	conId := peerAddr + "-" + strconv.FormatInt(id, 10)
	return &connection{
		PeerAddr:     peerAddr,
		Connect:      time.Now(),
		ConID:        conId,
		stream:       stream,
		pushChannel:  make(chan *changeEvent, pushChannelBufferSize),
		LastResponse: make(map[string]string),
		logger:       util.LoggerFor("connection").WithField("connection", conId),
	}
}

func (c *connection) receive(ctx context.Context) {

	for ctx.Err() == nil {
		req, err := c.stream.Recv()
		if err != nil {
			c.logger.WithError(err).Warn("stream terminated with error")
		}

		c.pushChannel <- &changeEvent{
			TypeUrl:    req.GetTypeUrl(),
			OldVersion: req.GetVersionInfo(),
		}
	}
}

func (con *connection) send(response *discovery.DiscoveryResponse) {
	con.Lock()
	err := con.stream.Send(response)
	con.LastResponse[response.GetTypeUrl()] = response.Nonce
	con.Unlock()

	if err != nil {
		fmt.Printf("RESOURCE:%s: RESPONSE ERROR %s %v\n", response.GetTypeUrl(), con.ConID, err)
	} else {
		fmt.Printf("RESOURCE:%s: RESPONSE SUCCESS WITH %v RESOURCES\n", response.GetTypeUrl(), len(response.Resources))
	}
}
