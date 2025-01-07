package main

import (
	"flag"
	"log"
	"strings"
	"time"

	"github.com/JaseP88/grpc_xds/api/echo"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	xdscreds "google.golang.org/grpc/credentials/xds"
	_ "google.golang.org/grpc/resolver" // use for "dns:///be.cluster.local:50051"
	_ "google.golang.org/grpc/xds"      // use for xds-experimental:///be-srv
)

var (
	target   = flag.String("target", "xds:///localhost:50051", "uri of the Greeter Server, e.g. 'xds:///helloworld-service:8080'")
	name     = flag.String("name", "world", "name you wished to be greeted by the server")
	xdsCreds = flag.Bool("xds_creds", false, "whether the server should use xDS APIs to receive security configuration")
)

func main() {
	// address := flag.String("host", "dns:///be.cluster.local:50051", "dns:///be.cluster.local:50051 or xds-experimental:///be-srv")
	flag.Parse()

	if !strings.HasPrefix(*target, "xds:///") {
		log.Fatalf("-target must use a URI with scheme set to 'xds'")
	}

	creds := insecure.NewCredentials()
	if *xdsCreds {
		log.Println("Using xDS credentials...")
		var err error
		if creds, err = xdscreds.NewClientCredentials(xdscreds.ClientOptions{FallbackCreds: insecure.NewCredentials()}); err != nil {
			log.Fatalf("failed to create client-side xDS credentials: %v", err)
		}
	}
	conn, err := grpc.NewClient(*target, grpc.WithTransportCredentials(creds))
	if err != nil {
		log.Fatalf("grpc.NewClient(%s) failed: %v", *target, err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := echo.NewEchoServerClient(conn)
	r, err := client.SayHello(ctx, &echo.EchoRequest{Name: *name})
	if err != nil {
		log.Fatalf("could not greet: %v", err)
	}
	log.Printf("Greeting: %s", r.GetMessage())

	// admin port
	// go func() {
	// 	lis, err := net.Listen("tcp", ":19000")
	// 	if err != nil {
	// 		log.Fatalf("failed to listen: %v", err)
	// 	}
	// 	defer lis.Close()
	// 	opts := []grpc.ServerOption{grpc.MaxConcurrentStreams(10)}
	// 	grpcServer := grpc.NewServer(opts...)
	// 	cleanup, err := admin.Register(grpcServer)
	// 	if err != nil {
	// 		log.Fatalf("failed to register admin services: %v", err)
	// 	}
	// 	defer cleanup()

	// 	log.Printf("Admin port listen on :%s", lis.Addr().String())
	// 	if err := grpcServer.Serve(lis); err != nil {
	// 		log.Fatalf("failed to serve: %v", err)
	// 	}
	// }()

	for i := 0; i < 30; i++ {
		r, err := client.SayHello(ctx, &echo.EchoRequest{Name: "unary RPC msg "})
		if err != nil {
			log.Fatalf("could not greet: %v", err)
		}
		log.Printf("RPC Response: %v %v", i, r)
		time.Sleep(2 * time.Second)
	}
}
