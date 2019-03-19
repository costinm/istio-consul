package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/envoyproxy/go-control-plane/envoy/api/v2"
	ads "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v2"
	"github.com/gogo/protobuf/proto"
	middleware "github.com/grpc-ecosystem/go-grpc-middleware"
	grpcprometheus "github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"istio.io/api/mcp/v1alpha1"
	mcp "istio.io/api/mcp/v1alpha1"
	"github.com/costinm/istio-discovery/pkg/features/pilot"
)

// Main implementation of the XDS, MCP and SDS services, using a common internal structures and model.

var (
	nacks = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "xds_nack",
		Help: "Nacks.",
	}, []string{"node", "type"})

	acks = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "xds_ack",
		Help: "Aacks.",
	}, []string{"type"})

	// key is the XDS/MCP type
	resourceHandler = map[string]typeHandler{}
)

// typeHandler is called when a request for a type is first received.
// It should send the list of resources on the connection.
type typeHandler func(s *AdsService, con *Connection, rtype string, res []string) error

//
type AdsService struct {
	grpcServer *grpc.Server

	// mutex used to modify structs, non-blocking code only.
	mutex sync.RWMutex

	// clients reflect active gRPC channels, for both ADS and MCP.
	// key is Connection.ConID
	clients map[string]*Connection

	connectionNumber int
}

func (s *AdsService) StreamSecrets(ads.SecretDiscoveryService_StreamSecretsServer) error {
	return nil
}

func (s *AdsService) FetchSecrets(context.Context, *v2.DiscoveryRequest) (*v2.DiscoveryResponse, error) {
	return nil, nil
}

type Connection struct {
	mu sync.RWMutex

	// PeerAddr is the address of the client envoy, from network layer
	PeerAddr string

	NodeID string

	// Time of connection, for debugging
	Connect time.Time

	// ConID is the connection identifier, used as a key in the connection table.
	// Currently based on the node name and a counter.
	ConID string

	// doneChannel will be closed when the client is closed.
	doneChannel chan int

	// Metadata key-value pairs extending the Node identifier
	Metadata map[string]string

	// Watched resources for the connection
	Watched map[string][]string

	NonceSent  map[string]string
	NonceAcked map[string]string

	// Only one can be set.
	Stream Stream

	active bool
}

// NewService initialized MCP and ADS servers.
func NewService(addr string) *AdsService {
	adss := &AdsService{
		clients: map[string]*Connection{},
	}

	adss.initGrpcServer()

	ads.RegisterAggregatedDiscoveryServiceServer(adss.grpcServer, adss)
	mcp.RegisterResourceSourceServer(adss.grpcServer, adss)
	ads.RegisterSecretDiscoveryServiceServer(adss.grpcServer, adss)

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal(err)
	}

	go adss.grpcServer.Serve(lis)

	return adss
}

type Stream interface {
	// can be mcp.RequestResources or v2.DiscoveryRequest
	Send(proto.Message) error
	// mcp.Resources or v2.DiscoveryResponse
	Recv() (proto.Message, error)

	Context() context.Context
	Process(s *AdsService, con *Connection, message proto.Message) error
}

type mcpStream struct {
	stream mcp.ResourceSource_EstablishResourceStreamServer
}

type adsStream struct {
	stream ads.AggregatedDiscoveryService_StreamAggregatedResourcesServer
}

func (mcps *mcpStream) Send(p proto.Message) error {
	if mp, ok := p.(*mcp.Resources); ok {
		return mcps.stream.Send(mp)
	}
	return errors.New("Invalid stream")
}

func (mcps *adsStream) Send(p proto.Message) error {
	if mp, ok := p.(*v2.DiscoveryResponse); ok {
		return mcps.stream.Send(mp)
	}
	return errors.New("Invalid stream")
}

func (mcps *mcpStream) Recv() (proto.Message, error) {
	p, err := mcps.stream.Recv()

	if err != nil {
		return nil, err
	}

	return p, err
}

func (mcps *adsStream) Recv() (proto.Message, error) {
	p, err := mcps.stream.Recv()

	if err != nil {
		return nil, err
	}

	return p, err
}

func (mcps *adsStream) Context() context.Context {
	return context.Background()
}

func (mcps *mcpStream) Context() context.Context {
	return context.Background()
}

// Compared with ADS:
//  req.Node -> req.SinkNode
//  metadata struct -> Annotations
//  TypeUrl -> Collection
//  no on-demand (Watched)
func (mcps *mcpStream) Process(s *AdsService, con *Connection, msg proto.Message) error {
	req := msg.(*mcp.RequestResources)
	if !con.active {
		var id string
		if req.SinkNode == nil || req.SinkNode.Id == "" {
			log.Println("Missing node id ", req.String())
			id = con.PeerAddr
		} else {
			id = req.SinkNode.Id
		}

		con.mu.Lock()
		con.NodeID = id
		con.Metadata = req.SinkNode.Annotations
		con.ConID = s.connectionID(con.NodeID)
		con.mu.Unlock()

		s.mutex.Lock()
		s.clients[con.ConID] = con
		s.mutex.Unlock()

		con.active = true
	}

	rtype := req.Collection

	if req.ErrorDetail != nil && req.ErrorDetail.Message != "" {
		nacks.With(prometheus.Labels{"node": con.NodeID, "type": rtype}).Add(1)
		log.Println("NACK: ", con.NodeID, rtype, req.ErrorDetail)
		return nil
	}
	if req.ErrorDetail != nil && req.ErrorDetail.Code == 0 {
		con.mu.Lock()
		con.NonceAcked[rtype] = req.ResponseNonce
		con.mu.Unlock()
		acks.With(prometheus.Labels{"type": rtype}).Add(1)
		return nil
	}

	if req.ResponseNonce != "" {
		// This shouldn't happen
		con.mu.Lock()
		lastNonce := con.NonceSent[rtype]
		con.mu.Unlock()

		if lastNonce == req.ResponseNonce {
			acks.With(prometheus.Labels{"type": rtype}).Add(1)
			con.mu.Lock()
			con.NonceAcked[rtype] = req.ResponseNonce
			con.mu.Unlock()
			return nil
		} else {
			// will resent the resource, set the nonce - next response should be ok.
			log.Println("Unmatching nonce ", req.ResponseNonce, lastNonce)
		}
	}

	// Blocking - read will continue
	err := s.push(con, rtype, nil)
	if err != nil {
		// push failed - disconnect
		log.Println("Closing connection ", err)
		return err
	}

	return err
}

func AddHandler(typ string, handler typeHandler) {
	log.Println("HANDLER: ", typ)
	resourceHandler[typ] = handler
}

func (mcps *adsStream) Process(s *AdsService, con *Connection, msg proto.Message) error {
	req := msg.(*v2.DiscoveryRequest)
	if !con.active {
		if req.Node == nil || req.Node.Id == "" {
			log.Println("Missing node id ", req.String())
			return errors.New("Missing node id")
		}

		con.mu.Lock()
		con.NodeID = req.Node.Id
		con.Metadata = parseMetadata(req.Node.Metadata)
		con.ConID = s.connectionID(con.NodeID)
		con.mu.Unlock()

		s.mutex.Lock()
		s.clients[con.ConID] = con
		s.mutex.Unlock()

		con.active = true
	}

	rtype := req.TypeUrl

	if req.ErrorDetail != nil && req.ErrorDetail.Message != "" {
		nacks.With(prometheus.Labels{"node": con.NodeID, "type": rtype}).Add(1)
		log.Println("NACK: ", con.NodeID, rtype, req.ErrorDetail)
		return nil
	}
	if req.ErrorDetail != nil && req.ErrorDetail.Code == 0 {
		con.mu.Lock()
		con.NonceAcked[rtype] = req.ResponseNonce
		con.mu.Unlock()
		acks.With(prometheus.Labels{"type": rtype}).Add(1)
		return nil
	}

	if req.ResponseNonce != "" {
		// This shouldn't happen
		con.mu.Lock()
		lastNonce := con.NonceSent[rtype]
		con.mu.Unlock()

		if lastNonce == req.ResponseNonce {
			acks.With(prometheus.Labels{"type": rtype}).Add(1)
			con.mu.Lock()
			con.NonceAcked[rtype] = req.ResponseNonce
			con.mu.Unlock()
			return nil
		} else {
			// will resent the resource, set the nonce - next response should be ok.
			log.Println("Unmatching nonce ", req.ResponseNonce, lastNonce)
		}
	}

	con.mu.Lock()
	// TODO: find added/removed resources, push only those.
	con.Watched[rtype] = req.ResourceNames
	con.mu.Unlock()

	// Blocking - read will continue
	err := s.push(con, rtype, req.ResourceNames)
	if err != nil {
		// push failed - disconnect
		return err
	}

	return err
}



func (s *AdsService) EstablishResourceStream(mcps mcp.ResourceSource_EstablishResourceStreamServer) error {
	return s.stream(&mcpStream{stream: mcps})
}

// StreamAggregatedResources implements the Envoy variant. Can be used directly with EDS.
func (s *AdsService) StreamAggregatedResources(stream ads.AggregatedDiscoveryService_StreamAggregatedResourcesServer) error {
	return s.stream(&adsStream{stream: stream})
}

func (s *AdsService) stream(stream Stream) error {

		peerInfo, ok := peer.FromContext(stream.Context())
	peerAddr := "0.0.0.0"
	if ok {
		peerAddr = peerInfo.Addr.String()
	}

	t0 := time.Now()

	con := &Connection{
		Connect:     t0,
		PeerAddr:    peerAddr,
		Stream:      stream,
		NonceSent:   map[string]string{},
		Metadata:    map[string]string{},
		Watched:     map[string][]string{},
		NonceAcked:  map[string]string{},
		doneChannel: make(chan int, 2),
	}
	// Unlike pilot, this uses the more direct 'main thread handles read' mode.
	// It also means we don't need 2 goroutines per connection.

	firstReq := true

	defer func() {
		if firstReq {
			return // didn't get first req, not added
		}
		s.mutex.Lock()
		delete(s.clients, con.ConID)
		s.mutex.Unlock()

	}()

	for {
		// Blocking. Separate go-routines may use the stream to push.
		req, err := stream.Recv()
		if err != nil {
			if status.Code(err) == codes.Canceled || err == io.EOF {
				log.Printf("ADS: %q %s terminated %v", con.PeerAddr, con.ConID, err)
				return nil
			}
			log.Printf("ADS: %q %s terminated with errors %v", con.PeerAddr, con.ConID, err)
			return err
		}
		err = stream.Process(s, con, req)
		if err != nil {
			return err
		}
	}

	return nil
}

// Push a single resource type on the connection. This is blocking.
func (s *AdsService) push(con *Connection, rtype string, res []string) error {
	h, f := resourceHandler[rtype]
	if !f {
		// TODO: push some 'not found'
		log.Println("Resource not found ", rtype)
		r := &v1alpha1.Resources{}
		r.Collection = rtype
		s.Send(con, rtype, r)
		return nil

	}
	return h(s, con, rtype, res)
}


// IncrementalAggregatedResources is not implemented.
func (s *AdsService) DeltaAggregatedResources(stream ads.AggregatedDiscoveryService_DeltaAggregatedResourcesServer) error {
	return status.Errorf(codes.Unimplemented, "not implemented")
}

// Callbacks from the lower layer

func (s *AdsService) initGrpcServer() {
	grpcOptions := s.grpcServerOptions()
	s.grpcServer = grpc.NewServer(grpcOptions...)

}

func (s *AdsService) grpcServerOptions() []grpc.ServerOption {
	interceptors := []grpc.UnaryServerInterceptor{
		// setup server prometheus monitoring (as final interceptor in chain)
		grpcprometheus.UnaryServerInterceptor,
	}

	grpcprometheus.EnableHandlingTimeHistogram()

	// Temp setting, default should be enough for most supported environments. Can be used for testing
	// envoy with lower values.
	var maxStreams int
	maxStreamsEnv := pilot.MaxConcurrentStreams
	if len(maxStreamsEnv) > 0 {
		maxStreams, _ = strconv.Atoi(maxStreamsEnv)
	}
	if maxStreams == 0 {
		maxStreams = 100000
	}

	grpcOptions := []grpc.ServerOption{
		grpc.UnaryInterceptor(middleware.ChainUnaryServer(interceptors...)),
		grpc.MaxConcurrentStreams(uint32(maxStreams)),
	}

	return grpcOptions
}

func (fx *AdsService) SendAll(r *v1alpha1.Resources) {
	for _, con:= range fx.clients {
		// TODO: only if watching our resource type

		r.Nonce = fmt.Sprintf("%v", time.Now())
		con.NonceSent[r.Collection] = r.Nonce;
		con.Stream.Send(r)
	}

}


func (fx *AdsService) Send(con *Connection, rtype string, r *v1alpha1.Resources) error {
	r.Nonce = fmt.Sprintf("%v", time.Now())
	con.NonceSent[rtype] = r.Nonce;
	return con.Stream.Send(r)
}

