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
	"reflect"
	"sort"
	"time"

	"github.com/hashicorp/consul/api"

	"github.com/costinm/istio-discovery/pilot/pkg/model"
	"github.com/costinm/istio-discovery/pkg/log"
)

// Controller communicates with Consul and monitors for changes
type Controller struct {
	client     *api.Client
	xdsUpdater model.XDSUpdater

	discovery            *api.Client
	instanceCachedRecord []*api.CatalogService

	serviceCachedRecord   map[string][]string

	period               time.Duration
}

// NewController creates a new Consul controller
func NewController(addr string, xdsUpdater model.XDSUpdater, interval time.Duration) (*Controller, error) {
	conf := api.DefaultConfig()
	conf.Address = addr

	client, err := api.NewClient(conf)
	return &Controller{
		discovery:            client,
		period:               interval,
		instanceCachedRecord: make([]*api.CatalogService, 0),
		serviceCachedRecord:  make(map[string][]string),
		client:  client,
		xdsUpdater: xdsUpdater,
	}, err
}

// Services list declarations of all services in the system
func (c *Controller) Services() ([]*model.Service, error) {
	data, err := c.getServices()
	if err != nil {
		return nil, err
	}

	services := make([]*model.Service, 0, len(data))
	for name := range data {
		endpoints, err := c.getCatalogService(name, nil)
		if err != nil {
			return nil, err
		}
		services = append(services, convertService(endpoints))
	}

	return services, nil
}

// GetService retrieves a service by host name if it exists
func (c *Controller) GetService(hostname model.Hostname) (*model.Service, error) {
	// Get actual service by name
	name, err := parseHostname(hostname)
	if err != nil {
		log.Infof("parseHostname(%s) => error %v", hostname, err)
		return nil, err
	}

	endpoints, err := c.getCatalogService(name, nil)
	if len(endpoints) == 0 || err != nil {
		return nil, err
	}

	return convertService(endpoints), nil
}

func (c *Controller) getServices() (map[string][]string, error) {
	// TODO: does not scale. Should have a cache, incremental.

	// With sync, services will be a map of name with value 'k8s' (the tag added by default)

	data, _, err := c.client.Catalog().Services(nil)
	if err != nil {
		log.Warnf("Could not retrieve services from consul: %v", err)
		return nil, err
	}

	return data, nil
}

func (c *Controller) getCatalogService(name string, q *api.QueryOptions) ([]*api.CatalogService, error) {
	endpoints, _, err := c.client.Catalog().Service(name, "", q)
	if err != nil {
		log.Warnf("Could not retrieve service catalogue from consul: %v", err)
		return nil, err
	}

	return endpoints, nil
}


// Run all controllers until a signal is received
func (c *Controller) Run(stop <-chan struct{}) {
	svcs, err := c.getServices()
	if err != nil {
		log.Warnf("Could not fetch services: %v", err)
		return
	}
	c.xdsUpdater.ConfigUpdate(true)
	c.serviceCachedRecord = svcs

	for _, tags := range svcs {
		sort.Strings(tags)
	}

	instances := make([]*api.CatalogService, 0)
	for name := range svcs {
		endpoints, _, err := c.discovery.Catalog().Service(name, "", nil)
		if err != nil {
			log.Warnf("Could not retrieve service catalogue from consul: %v", err)
			continue
		}

		log.Infof("Endpoints: %s %v", name, endpoints)

		go c.watchInstance(name)
		instances = append(instances, endpoints...)
	}

	newRecord := consulServiceInstances(instances)
	sort.Sort(newRecord)
	c.instanceCachedRecord = newRecord

	go c.watchSvc()
	go c.watchNodes("")
}

func (c *Controller) watchSvc() {
	idx := uint64(0)
	for {
		svcs, meta, err := c.client.Catalog().Services(&api.QueryOptions{
			WaitIndex: idx,
			WaitTime: 30 * time.Second,
		})
		if err != nil {
			log.Infof("Consul error %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		if idx != meta.LastIndex {
			idx = meta.LastIndex
			// When an instance shows up, we get a svc update as well. This needs to be filtered out.
			if !reflect.DeepEqual(svcs, c.serviceCachedRecord) {
				log.Infof("SVC watch %d %v %v %v", idx, svcs, meta, err)
				// find added services, add watchInstance for them
				for k, _ := range svcs {
					if _, f := c.serviceCachedRecord[k]; !f {
						c.watchInstance(k)
					}
				}
				// TODO: find removed instances, stop watches on them

				c.xdsUpdater.ConfigUpdate(true)
				c.serviceCachedRecord = svcs
			}
		}
	}
}

func (c *Controller) watchInstance(servicename string) {
	idx := uint64(0)
	for {
		svcs, meta, err := c.client.Catalog().Service(servicename, "", &api.QueryOptions{
			WaitIndex: idx,
			WaitTime: 30 * time.Second,
		})
		if err != nil {
			log.Infof("Consul error %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		if idx != meta.LastIndex {

			idx = meta.LastIndex
			log.Infof("INS watch %d %s %v %v", idx, servicename, svcs, meta)
			eps := []*model.IstioEndpoint{}
			for _, si := range svcs {
				msi := convertInstance(si)
				eps = append(eps, msi)
			}

			c.xdsUpdater.EDSUpdate("consul", servicename, eps)
		}
	}
}

func (c *Controller) watchNodes(svc string) {
	idx := uint64(0)
	for {
		svcs, meta, err := c.client.Catalog().Nodes(&api.QueryOptions{
			WaitIndex: idx,
			WaitTime: 30 * time.Second,
		})
		if err != nil {
			log.Infof("Consul error %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		if idx != meta.LastIndex {
			idx = meta.LastIndex
			log.Infof("NODE watch %v %v %v", svcs, meta, err)
		}
	}
}


// GetIstioServiceAccounts implements model.ServiceAccounts operation TODO
func (c *Controller) GetIstioServiceAccounts(hostname model.Hostname, ports []string) []string {
	// Need to get service account of service registered with consul
	// Currently Consul does not have service account or equivalent concept
	// As a step-1, to enabling istio security in Consul, We assume all the services run in default service account
	// This will allow all the consul services to do mTLS
	// Follow - https://goo.gl/Dt11Ct

	return []string{
		"spiffe://cluster.local/ns/default/sa/default",
	}
}

type consulServiceInstances []*api.CatalogService

// Len of the array
func (a consulServiceInstances) Len() int {
	return len(a)
}

// Swap i and j
func (a consulServiceInstances) Swap(i, j int) {
	a[i], a[j] = a[j], a[i]
}

// Less i and j
func (a consulServiceInstances) Less(i, j int) bool {
	return a[i].ID < a[j].ID
}
