package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/costinm/istio-discovery/pkg/ads"
	"github.com/costinm/istio-consul/pkg/consul"
	"github.com/costinm/istio-discovery/pilot/pkg/model"
)

var (
	server = flag.String("consul", "localhost:8500", "Address of consul server")

	addr = flag.String("httpAddr", ":15099", "Address of the HTTP debug server")

	grpcAddr = flag.String("grpcAddr", ":15098", "Address of the ADS/MCP server")
)

// Minimal MCP server exposing k8s and consul synthetic entries
// Currently both are returned to test the timing of the k8s-to-consul sync
// Will use KUBECONFIG or in-cluster config for k8s
func main() {
	flag.Parse()

	a := ads.NewService(*grpcAddr)

	c := initController(a)

	log.Println("Starting", c, a)

	http.ListenAndServe(*addr, nil)
}

func initController(a model.XDSUpdater) *consul.Controller {

	c, err := consul.NewController(*server, a, 10 * time.Second)
	if err != nil {
		log.Fatal(err)
	}
	go c.Run(make(chan struct{}))
		return c
}
