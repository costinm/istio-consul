package memory

import (
	"time"

	"istio.io/api/networking/v1alpha3"
)

// FakeXdsUpdater is used to test the registry.
type FakeXdsUpdater struct {
	// Events tracks notifications received by the updater
	Events chan XdsEvent


}

// XdsEvent is used to watch XdsEvents
type XdsEvent struct {
	// Type of the event
	Type string

	// The id of the event
	ID string
}

// NewFakeXDS creates a XdsUpdater reporting events via a channel.
func NewFakeXDS() *FakeXdsUpdater {
	return &FakeXdsUpdater{
		Events: make(chan XdsEvent, 100),
	}
}

func (*FakeXdsUpdater) ConfigUpdate(bool) {

}


func (fx *FakeXdsUpdater) ServiceEntriesUpdate(shard, hostname string, entry []*v1alpha3.ServiceEntry) error {
	select {
	case fx.Events <- XdsEvent{Type: "eds", ID: hostname}:
	default:
	}
	return nil

}

// SvcUpdate is called when a service port mapping definition is updated.
// This interface is WIP - labels, annotations and other changes to service may be
// updated to force a EDS and CDS recomputation and incremental push, as it doesn't affect
// LDS/RDS.
func (fx *FakeXdsUpdater) SvcUpdate(shard, hostname string, ports map[string]uint32, rports map[uint32]string) {
	select {
	case fx.Events <- XdsEvent{Type: "service", ID: hostname}:
	default:
	}
}

func (fx *FakeXdsUpdater) WorkloadUpdate(id string, labels map[string]string, annotations map[string]string) {
	select {
	case fx.Events <- XdsEvent{Type: "workload", ID: id}:
	default:
	}
}

func (fx *FakeXdsUpdater) Wait(et string) *XdsEvent {
	t := time.NewTimer(5 * time.Second)

	for {
		select {
		case e := <-fx.Events:
			if e.Type == et {
				return &e
			}
			continue
		case <-t.C:
			return nil
		}
	}
}

// Clear any pending event
func (fx *FakeXdsUpdater) Clear() {
	wait := true
	for wait {
		select {
		case <-fx.Events:
		default:
			wait = false
			break
		}
	}
}

