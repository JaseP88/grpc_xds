package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	"github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	listenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	routev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	routerv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/router/v3"
	http_connection_managerv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	discoveryv3 "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	"github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"github.com/envoyproxy/go-control-plane/pkg/server/v3"
	"github.com/envoyproxy/go-control-plane/pkg/test/v3"
	"github.com/envoyproxy/go-control-plane/pkg/wellknown"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// UpstreamPorts is a type that implements flag.Value interface
type UpstreamPorts []int

// String is a method that implements the flag.Value interface
func (u *UpstreamPorts) String() string {
	// See: https://stackoverflow.com/a/37533144/609290
	return strings.Join(strings.Fields(fmt.Sprint(*u)), ",")
}

// Set is a method that implements the flag.Value interface
func (u *UpstreamPorts) Set(port string) error {
	log.Printf("[UpstreamPorts] %s", port)
	i, err := strconv.Atoi(port)
	if err != nil {
		return err
	}
	*u = append(*u, i)
	return nil
}

var (
	debug         bool
	port          uint
	version       int32
	upstreamPorts UpstreamPorts
)

const (
	localhost       = "127.0.0.1"
	backendHostName = "be.cluster.local"
	listenerName    = "be-srv"
	routeConfigName = "be-srv-route"
	clusterName     = "be-srv-cluster"
	virtualHostName = "be-srv-vs"
)

func init() {
	flag.BoolVar(&debug, "debug", true, "Use debug logging")
	flag.UintVar(&port, "port", 18000, "Management server port")
	flag.Var(&upstreamPorts, "upstream_port", "list of upstream gRPC servers")
}

const grpcMaxConcurrentStreams = 1000

// RunManagementServer starts an xDS server at the given port.
func RunManagementServer(ctx context.Context, server server.Server, port uint) {
	var grpcOptions []grpc.ServerOption
	grpcOptions = append(grpcOptions, grpc.MaxConcurrentStreams(grpcMaxConcurrentStreams))
	grpcServer := grpc.NewServer(grpcOptions...)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.WithError(err).Fatal("failed to listen")
	}

	// register services
	discoveryv3.RegisterAggregatedDiscoveryServiceServer(grpcServer, server)

	log.WithFields(log.Fields{"port": port}).Info("management server listening")
	go func() {
		if err = grpcServer.Serve(lis); err != nil {
			log.Error(err)
		}
	}()
	<-ctx.Done()

	grpcServer.GracefulStop()
}

func main() {
	flag.Parse()
	if debug {
		log.SetLevel(log.DebugLevel)
	}
	ctx := context.Background()

	log.Printf("Starting control plane")

	signal := make(chan struct{})
	cb := &test.Callbacks{Debug: true}

	xdsCache := cache.NewSnapshotCache(true, cache.IDHash{}, &log.Logger{})

	srv := server.NewServer(ctx, xdsCache, cb)

	go RunManagementServer(ctx, srv, port)

	<-signal

	cb.Report()

	var lbendpoints []*endpointv3.LbEndpoint
	currentHost := 0

	for {
		if currentHost+1 <= len(upstreamPorts) {
			v := upstreamPorts[currentHost]
			currentHost++
			// ENDPOINT
			log.Infof(">>>>>>>>>>>>>>>>>>> creating ENDPOINT for remoteHost:port %s:%d", backendHostName, v)

			hst := &corev3.Address{Address: &corev3.Address_SocketAddress{
				SocketAddress: &corev3.SocketAddress{
					Address:  backendHostName,
					Protocol: corev3.SocketAddress_TCP,
					PortSpecifier: &corev3.SocketAddress_PortValue{
						PortValue: uint32(v),
					},
				},
			}}

			epp := &endpointv3.LbEndpoint{
				HostIdentifier: &endpointv3.LbEndpoint_Endpoint{
					Endpoint: &endpointv3.Endpoint{
						Address: hst,
					}},
				HealthStatus: corev3.HealthStatus_HEALTHY,
			}
			lbendpoints = append(lbendpoints, epp)

			eds := []types.Resource{
				&endpointv3.ClusterLoadAssignment{
					ClusterName: clusterName,
					Endpoints: []*endpointv3.LocalityLbEndpoints{{
						Locality: &corev3.Locality{
							Region: "us-central1",
							Zone:   "us-central1-a",
						},
						Priority:            0,
						LoadBalancingWeight: &wrapperspb.UInt32Value{Value: uint32(1000)},
						LbEndpoints:         lbendpoints,
					}},
				},
			}

			// CLUSTER
			log.Infof(">>>>>>>>>>>>>>>>>>> creating CLUSTER " + clusterName)
			cls := []types.Resource{
				&clusterv3.Cluster{
					Name:                 clusterName,
					LbPolicy:             clusterv3.Cluster_ROUND_ROBIN,
					ClusterDiscoveryType: &clusterv3.Cluster_Type{Type: clusterv3.Cluster_EDS},
					EdsClusterConfig: &clusterv3.Cluster_EdsClusterConfig{
						EdsConfig: &corev3.ConfigSource{
							ConfigSourceSpecifier: &corev3.ConfigSource_Ads{},
						},
					},
				},
			}

			// RDS
			log.Infof(">>>>>>>>>>>>>>>>>>> creating RDS " + virtualHostName)

			rds := []types.Resource{
				&routev3.RouteConfiguration{
					Name:             routeConfigName,
					ValidateClusters: &wrapperspb.BoolValue{Value: true},
					VirtualHosts: []*routev3.VirtualHost{{
						Name:    virtualHostName,
						Domains: []string{listenerName}, //******************* >> must match what is specified at xds:/// //
						Routes: []*routev3.Route{{
							Match: &routev3.RouteMatch{
								PathSpecifier: &routev3.RouteMatch_Prefix{
									Prefix: "",
								},
							},
							Action: &routev3.Route_Route{
								Route: &routev3.RouteAction{
									ClusterSpecifier: &routev3.RouteAction_Cluster{
										Cluster: clusterName,
									},
								},
							},
						},
						},
					}},
				},
			}

			// LISTENER
			log.Infof(">>>>>>>>>>>>>>>>>>> creating LISTENER " + listenerName)
			hcRds := &http_connection_managerv3.HttpConnectionManager_Rds{
				Rds: &http_connection_managerv3.Rds{
					RouteConfigName: routeConfigName,
					ConfigSource: &corev3.ConfigSource{
						ResourceApiVersion: corev3.ApiVersion_V3,
						ConfigSourceSpecifier: &corev3.ConfigSource_Ads{
							Ads: &corev3.AggregatedConfigSource{},
						},
					},
				},
			}

			hff := &routerv3.Router{}
			tctx, err := anypb.New(hff)
			if err != nil {
				log.Errorf("could not unmarshall router: %v\n", err)
				os.Exit(1)
			}

			manager := &http_connection_managerv3.HttpConnectionManager{
				CodecType:      http_connection_managerv3.HttpConnectionManager_AUTO,
				RouteSpecifier: hcRds,
				HttpFilters: []*http_connection_managerv3.HttpFilter{{
					Name: wellknown.Router,
					ConfigType: &http_connection_managerv3.HttpFilter_TypedConfig{
						TypedConfig: tctx,
					},
				}},
			}

			pbst, err := anypb.New(manager)
			if err != nil {
				panic(err)
			}

			l := []types.Resource{
				&listenerv3.Listener{
					Name: listenerName,
					ApiListener: &listenerv3.ApiListener{
						ApiListener: pbst,
					},
				}}

			atomic.AddInt32(&version, 1)
			log.Infof(">>>>>>>>>>>>>>>>>>> creating snapshot Version %d", version)
			resources := make(map[resource.Type][]types.Resource, 4)
			resources[resource.ClusterType] = cls
			resources[resource.ListenerType] = l
			resources[resource.RouteType] = rds
			resources[resource.EndpointType] = eds

			snap, err := cache.NewSnapshot(fmt.Sprint(version), resources)
			if err != nil {
				log.Fatalf("Could not set snapshot %v", err)
			}

			err = xdsCache.SetSnapshot(ctx, "node123", snap)
			if err != nil {
				log.Fatalf("Could not set snapshot %v", err)
			}

			time.Sleep(10 * time.Second)
		}
	}
}
