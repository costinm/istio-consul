package service

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gogo/protobuf/types"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/costinm/istio-discovery/pkg/log"
)

var (
	xdsClients = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "pilot_xds",
		Help: "Number of endpoints connected to this pilot using XDS",
	})
)

// ParseMetadata parses the opaque Metadata from an Envoy Node into string key-value pairs.
// Any non-string values are ignored.
func parseMetadata(metadata *types.Struct) map[string]string {
	if metadata == nil {
		return nil
	}
	fields := metadata.GetFields()
	res := make(map[string]string, len(fields))
	for k, v := range fields {
		if s, ok := v.GetKind().(*types.Value_StringValue); ok {
			res[k] = s.StringValue
		}
	}
	if len(res) == 0 {
		res = nil
	}
	return res
}

func (s *AdsService) connectionID(node string) string {
	s.mutex.Lock()
	s.connectionNumber++
	c := s.connectionNumber
	s.mutex.Unlock()
	return node + "-" + strconv.Itoa(int(c))
}

func (s *AdsService) addCon(conID string, con *Connection) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.clients[conID] = con
	xdsClients.Set(float64(len(s.clients)))

	//
}

func (s *AdsService) removeCon(conID string, con *Connection) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.clients[conID] == nil {
		log.Errorf("ADS: Removing connection for non-existing node %v.", s)
	}
	delete(s.clients, conID)
	xdsClients.Set(float64(len(s.clients)))
}

type NodeID struct {
	// Type can be sidecar, ingress, router. Other names are possible.
	Type string

	// IPAddress is the declared IP. If missing, the peer address can be used
	IPAddress string

	// WorkloadID identifies the workload. In K8S it is podID.namespace
	WorkloadID string

	// Domain suffix for short hostnames. In k8s it is the namespace.svc.cluster.local
	Domain string
}

// parseServiceNode extracts info from the node
func parseServiceNode(s string) (NodeID, error) {
	parts := strings.Split(s, "~")
	out := NodeID{}

	if len(parts) != 4 {
		return out, fmt.Errorf("missing parts in the service node %q", s)
	}

	out.Type = parts[0]

	out.IPAddress = parts[1]

	out.WorkloadID = parts[2]

	out.Domain = parts[3]
	return out, nil
}
