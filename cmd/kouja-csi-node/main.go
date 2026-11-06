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
		endpoint     = flag.String("endpoint", "unix:///csi/csi.sock", "CSI Unix socket endpoint")
		nodeID       = flag.String("node-id", os.Getenv("NODE_ID"), "Kubernetes node identity")
		initiator    = flag.String("initiator", os.Getenv("KOUJA_ISCSI_INITIATOR"), "iSCSI initiator IQN")
		fsType       = flag.String("fs-type", "ext4", "default filesystem type")
		driverName   = flag.String("driver-name", csi.DriverName, "CSI driver name")
		agentAddress = flag.String("agent-endpoint", "", "optional kouja agent endpoint for credential grants")
		tlsCert      = flag.String("tls-cert", "/etc/kouja/tls/tls.crt", "node client certificate")
		tlsKey       = flag.String("tls-key", "/etc/kouja/tls/tls.key", "node client private key")
		tlsCA        = flag.String("tls-ca", "/etc/kouja/tls/ca.crt", "agent CA bundle")
	)
	flag.Parse()
	if *nodeID == "" {
		hostname, err := os.Hostname()
		if err != nil {
			log.Fatal(err)
		}
		*nodeID = hostname
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
	node := csi.NewNode(*nodeID, *initiator, *fsType)
	if *agentAddress != "" {
		tlsConfig, tlsErr := csi.LoadTLSConfig(*tlsCert, *tlsKey, *tlsCA)
		if tlsErr != nil {
			log.Fatal(tlsErr)
		}
		if setErr := node.SetAgent(*agentAddress, tlsConfig); setErr != nil {
			log.Fatal(setErr)
		}
	}
	server := grpc.NewServer()
	csipb.RegisterNodeServer(server, node)
	csipb.RegisterIdentityServer(server, &csi.Identity{Name: *driverName, Version: csi.Version})
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		server.GracefulStop()
	}()
	log.Printf("kouja CSI node listening on %s", socket)
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
