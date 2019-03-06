// Copyright 2017 Istio Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package consul

import (
	"testing"
	"time"

	"github.com/costinm/istio-discovery/pkg/memory"
	"github.com/hashicorp/consul/api"

)

var (
	services = map[string][]string{
		"productpage": {"version|v1"},
		"reviews":     {"version|v1", "version|v2", "version|v3"},
	}
	productpage = []*api.CatalogService{
		{
			Node:           "istio",
			Address:        "172.19.0.5",
			ID:             "111-111-111",
			ServiceName:    "productpage",
			ServiceTags:    []string{"version|v1"},
			ServiceAddress: "172.19.0.11",
			ServicePort:    9080,
		},
	}
	reviews = []*api.CatalogService{
		{
			Node:           "istio",
			Address:        "172.19.0.5",
			ID:             "222-222-222",
			ServiceName:    "reviews",
			ServiceTags:    []string{"version|v1"},
			ServiceAddress: "172.19.0.6",
			ServicePort:    9081,
		},
		{
			Node:           "istio",
			Address:        "172.19.0.5",
			ID:             "333-333-333",
			ServiceName:    "reviews",
			ServiceTags:    []string{"version|v2"},
			ServiceAddress: "172.19.0.7",
			ServicePort:    9081,
		},
		{
			Node:           "istio",
			Address:        "172.19.0.5",
			ID:             "444-444-444",
			ServiceName:    "reviews",
			ServiceTags:    []string{"version|v3"},
			ServiceAddress: "172.19.0.8",
			ServicePort:    9080,
			NodeMeta:       map[string]string{protocolTagName: "tcp"},
		},
	}
)

func TestInstances(t *testing.T) {
	conf := api.DefaultConfig()
	conf.Address = "127.0.0.1:8500"

	client, err := api.NewClient(conf)

	xds := &memory.FakeXdsUpdater{}
	controller, err := NewController(conf.Address, xds, 3*time.Second)
	if err != nil {
		t.Errorf("could not create Consul Controller: %v", err)
	}

	controller.Run(nil)


	w, err := client.Catalog().Register(&api.CatalogRegistration{
		Datacenter: "dc1",
		Node:       "local1",
		Address:    "127.0.0.2",
		TaggedAddresses: map[string]string{
			"lan": "192.168.10.10",
			"wan": "10.0.10.10",
		},
		NodeMeta: map[string]string{
			"somekey": "somevalue",
		},
		Service: &api.AgentService{
			ID: "myecho",
			Service: "echo123",
			Port: 1234,
			Tags: []string{"t1|v2"},
			Address: "127.0.0.3",
			Meta: map[string]string{"version":"1"},
		},
	}, nil)
	if err != nil {
		t.Error(err)
	}
	t.Log(w)

	ev := xds.Wait("eds")
	t.Log("Event", ev)

}

