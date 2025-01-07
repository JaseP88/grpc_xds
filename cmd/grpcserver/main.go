package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"net"
	"os"

	"github.com/JaseP88/grpc_xds/api/echo"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	xdscreds "google.golang.org/grpc/credentials/xds"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/xds"
)

var (
	port     = flag.Int("port", 50051, "the port to serve Greeter service requests on. Health service will be served on `port+1`")
	xdsCreds = flag.Bool("xds_creds", false, "whether the server should use xDS APIs to receive security configuration")
)

type server struct {
	echo.UnimplementedEchoServerServer
	serverName string
}

func determineHostname() string {
	hostname, err := os.Hostname()
	if err != nil {
		log.Printf("Failed to get hostname: %v, will generate one", err)
		return fmt.Sprintf("generated-%03d", rand.Int()%100)
	}
	return hostname
}

func (s *server) SayHello(ctx context.Context, in *echo.EchoRequest) (*echo.EchoReply, error) {
	log.Println("Got rpc: --> ", in.Name)
	return &echo.EchoReply{Message: "Hello " + in.Name + "  from " + s.serverName}, nil
}

func (s *server) SayHelloStream(in *echo.EchoRequest, stream echo.EchoServer_SayHelloStreamServer) error {

	log.Println("Got stream:  -->  ")
	stream.Send(&echo.EchoReply{Message: "Hello " + in.Name})
	stream.Send(&echo.EchoReply{Message: "Hello " + in.Name})

	return nil
}

func main() {
	flag.Parse()

	greeterPort := fmt.Sprintf("127.0.0.1:%d", *port)
	greeterLis, err := net.Listen("tcp4", greeterPort)
	if err != nil {
		log.Fatalf("net.Listen(tcp4, %q) failed: %v", greeterPort, err)
	}

	creds := insecure.NewCredentials()
	if *xdsCreds {
		log.Println("Using xDS credentials...")
		var err error
		if creds, err = xdscreds.NewServerCredentials(xdscreds.ServerOptions{FallbackCreds: insecure.NewCredentials()}); err != nil {
			log.Fatalf("failed to create server-side xDS credentials: %v", err)
		}
	}

	greeterServer, err := xds.NewGRPCServer(grpc.Creds(creds))
	if err != nil {
		log.Fatalf("Failed to create an xDS enabled gRPC server: %v", err)
	}
	echo.RegisterEchoServerServer(greeterServer, &server{serverName: determineHostname()})

	healthPort := fmt.Sprintf("127.0.0.1:%d", *port+1)
	healthLis, err := net.Listen("tcp4", healthPort)
	if err != nil {
		log.Fatalf("net.Listen(tcp4, %q) failed: %v", healthPort, err)
	}
	grpcServer := grpc.NewServer()
	healthServer := health.NewServer()
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(grpcServer, healthServer)

	log.Printf("Serving GreeterService on %s and HealthService on %s", greeterLis.Addr().String(), healthLis.Addr().String())
	go func() {
		greeterServer.Serve(greeterLis)
	}()
	grpcServer.Serve(healthLis)
}
