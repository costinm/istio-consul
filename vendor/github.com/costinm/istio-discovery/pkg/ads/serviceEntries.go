package ads

import (
	"log"
	"strings"
	"sync"

	"github.com/costinm/istio-discovery/pilot/pkg/model"
	"github.com/gogo/protobuf/types"
	"istio.io/api/mcp/v1alpha1"
	"istio.io/api/networking/v1alpha3"
)

type Endpoints struct {
	mutex sync.RWMutex
	epShards map[string]map[string][]*model.IstioEndpoint
}

var (
	ep = &Endpoints{
		epShards: map[string]map[string][]*model.IstioEndpoint{},
	}
)

const ServiceEntriesType = "istio/networking/v1alpha3/vserviceentries"

func init() {
	resourceHandler["ServiceEntry"] = sePush
	resourceHandler[ServiceEntriesType] = sePush
	resourceHandler["type.googleapis.com/envoy.api.v2.ClusterLoadAssignment"] = edsPush
}


// Called to request push of endpoints in ServiceEntry format
func sePush(s *AdsService, con *Connection, rtype string, res []string) error {
	log.Print("SE request ", rtype, res)

	r := &v1alpha1.Resources{}
	r.Collection = ServiceEntriesType // must match
	for hostname, sh := range ep.epShards {
		res, err := convertShardToResource(hostname, sh)
		if err != nil {
			return err
		}
		r.Resources = append(r.Resources, *res)

	}

	return s.Send(con, rtype, r)
}

// Called to request push of ClusterLoadAssignments (EDS) - same information, but in Envoy format
func edsPush(s *AdsService, con *Connection, rtype string, res []string) error {
	// TODO.
	return nil
}


// Called when a new endpoint is added to a shard.
func (fx *AdsService) EDSUpdate(shard, hostname string, entry []*model.IstioEndpoint) error {
	ep.mutex.Lock()
	defer ep.mutex.Unlock()

	sh, f := ep.epShards[hostname]
	if !f {
		sh = map[string][]*model.IstioEndpoint{}
		ep.epShards[hostname] = sh
	}

	sh[shard] = entry

	log.Println("EDSUpdate ", shard, hostname, entry)

	// Typically this is deployed for a single cluster - but may still group in shards.

	// See sink.go - handleResponse.
	r := &v1alpha1.Resources{}

	r.Collection = ServiceEntriesType // must match

	res, err := convertShardToResource(hostname, sh)
	if err != nil {
		return err
	}

	r.Resources = []v1alpha1.Resource{*res}
	// The object created by client has resource.Body.TypeUrl, resource.Metadata and Body==Message.

	// TODO: remove the extra caching in coremodel

	fx.SendAll(r)

	return nil
}

func convertShardToResource(hostname string, sh map[string][]*model.IstioEndpoint) (*v1alpha1.Resource, error ){
	// See serviceregistry/external/conversion for the opposite side
	// See galley/pkg/runtime/state
	hostParts := strings.Split(hostname, ".")
	name := hostParts[0]
	var namespace string
	if len(hostParts) == 1 {
		namespace = "consul"
	} else {
		namespace = hostParts[1]
	}
	se := &v1alpha3.ServiceEntry{

	}

	for shardName, hmap := range sh {
		for _, ep := range hmap {
			se.Endpoints = append(se.Endpoints, &v1alpha3.ServiceEntry_Endpoint{
				Address: ep.Address,
				Labels: map[string]string{"shard":shardName},
			})
		}
	}

	seAny, err := types.MarshalAny(se)
	if err != nil {
		return nil, err
	}
	res := v1alpha1.Resource{
		Body: seAny,
		Metadata:&v1alpha1.Metadata{
			Name: namespace + "/" + name, // goes to model.Config.Name and Namespace - of course different syntax
		},
	}

	res.Metadata.Version = "1" // model.Config.ResourceVersion
	// Labels and Annotations - for the top service, not used here

	return &res, nil
}

// Called on pod events.
func (fx *AdsService) WorkloadUpdate(id string, labels map[string]string, annotations map[string]string) {
	// update-Running seems to be readiness check ?
	log.Println("PodUpdate ", id, labels, annotations)
}


func (*AdsService) ConfigUpdate(bool) {
	//log.Println("ConfigUpdate")
}

// Updating the internal data structures

// SvcUpdate is called when a service port mapping definition is updated.
// This interface is WIP - labels, annotations and other changes to service may be
// updated to force a EDS and CDS recomputation and incremental push, as it doesn't affect
// LDS/RDS.
func (fx *AdsService) SvcUpdate(shard, hostname string, ports map[string]uint32, rports map[uint32]string) {
	log.Println("ConfigUpdate")
}
