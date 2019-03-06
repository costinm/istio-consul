# Intro

This is a refactoring of the consul registry adapter from Istio 1.x.

Changes:
- use the streaming interface instead of periodic polling, which doesn't scale and results in delay
- implements MCP and XDS EDS protocols - using minimal common code/deps from a separate repository ( istio.io/istio has too many
deps). It can be used directly from envoy, or via Pilot.
- simplified/optimized

# Limitations

K8S consul syncer is flattening the services. To workaround we could use namespace labels (or fix consul syncer).

# Run

By default will look for the local consul agent, on port 8500, and expose an MCP/ADS on 15098. A http debug interface
is available on 15099. Command line flags allow setting different values.

It is expected that an Envoy sidecar will add MTLS.


# Testing with k8s

```bash

consul agent -dev -config-dir=$TOP/src/github.com/costinm/istio-consul/pkg/consul/testdata/config-dir -ui

or

kubectl --namespace consul port-forward $(kubectl -n consul get -l app=consul pod -l component=server \
  -o=jsonpath='{.items[0].metadata.name}') 8500:8500

go run ../../../istio.io/istio/galley/tools/mcpc/main.go \
    --server 127.0.0.1:15098 \
    -sortedCollections istio/networking/v1alpha3/vserviceentries \
    -id testmcp -labels=a=b,c=d

```
