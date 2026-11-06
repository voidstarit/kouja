package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	csipb "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/voidstarit/kouja/internal/csi"
	"google.golang.org/grpc"
)

func main() {
	var (
		endpoint   = flag.String("endpoint", "unix:///csi/csi.sock", "CSI Unix socket endpoint")
		hosts      = flag.String("hosts", "127.0.0.1:9443", "comma-separated kouja agent gRPC endpoints")
		srvRecord  = flag.String("srv-record", "", "optional SRV record that replaces --hosts with discovered agent endpoints")
		certFile   = flag.String("tls-cert", "/etc/kouja/tls/tls.crt", "client certificate")
		keyFile    = flag.String("tls-key", "/etc/kouja/tls/tls.key", "client private key")
		caFile     = flag.String("tls-ca", "/etc/kouja/tls/ca.crt", "server CA bundle")
		driverName = flag.String("driver-name", csi.DriverName, "CSI driver name")
		kubeconfig = flag.String("kubeconfig", "", "optional kubeconfig; in-cluster config is used when empty")
	)
	flag.Parse()
	tlsConfig, err := csi.LoadTLSConfig(*certFile, *keyFile, *caFile)
	if err != nil {
		log.Fatal(err)
	}
	endpoints := strings.Split(*hosts, ",")
	if *srvRecord != "" {
		discovered, resolveErr := csi.ResolveSRV(context.Background(), *srvRecord)
		if resolveErr != nil {
			log.Fatal(resolveErr)
		}
		endpoints = discovered
	}
	controller, err := csi.NewController(endpoints, tlsConfig)
	if err != nil {
		log.Fatal(err)
	}
	defer controller.Close()
	if lookup, lookupErr := csi.NewKubernetesAttachmentLookup(*kubeconfig); lookupErr == nil {
		controller.SetAttachmentLookup(lookup)
		controller.SetPolicyLookup(lookup)
	} else {
		log.Printf("CSI attachment generation lookup unavailable: %v", lookupErr)
	}
	if placement, placementErr := csi.NewKubernetesPlacement(*kubeconfig); placementErr == nil {
		controller.SetPlacement(placement)
	} else {
		log.Printf("CSI placement records unavailable: %v", placementErr)
	}
	socket, err := socketPath(*endpoint)
	if err != nil {
		log.Fatal(err)
	}
	_ = os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		log.Fatalf("listen %s: %v", socket, err)
	}
	server := grpc.NewServer()
	csipb.RegisterControllerServer(server, controller)
	csipb.RegisterIdentityServer(server, &csi.Identity{
		Name: *driverName, Version: csi.Version, Controller: true, Ready: controller.Ready,
	})
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		server.GracefulStop()
	}()
	log.Printf("kouja CSI controller listening on %s", socket)
	if err := server.Serve(listener); err != nil {
		log.Fatal(err)
	}
}

func socketPath(endpoint string) (string, error) {
	path := strings.TrimPrefix(endpoint, "unix://")
	if path == endpoint || path == "" {
		return "", fmt.Errorf("unsupported CSI endpoint %q", endpoint)
	}
	return path, nil
}
