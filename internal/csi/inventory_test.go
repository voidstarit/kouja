package csi

import (
	"context"
	"errors"
	"net"
	"testing"
)

func TestResolveSRV(t *testing.T) {
	previous := lookupSRV
	t.Cleanup(func() { lookupSRV = previous })
	lookupSRV = func(service, proto, name string) (string, []*net.SRV, error) {
		if service != "" || proto != "" || name != "_kouja._tcp.example.test" {
			t.Fatalf("lookup args = %q %q %q", service, proto, name)
		}
		return "", []*net.SRV{
			{Target: "b.example.test.", Port: 9443},
			{Target: "a.example.test.", Port: 9443},
			{Target: ".", Port: 9443},
		}, nil
	}
	endpoints, err := ResolveSRV(context.Background(), "_kouja._tcp.example.test")
	if err != nil {
		t.Fatalf("ResolveSRV: %v", err)
	}
	if len(endpoints) != 2 || endpoints[0] != "a.example.test:9443" || endpoints[1] != "b.example.test:9443" {
		t.Fatalf("endpoints = %#v", endpoints)
	}

	lookupSRV = func(string, string, string) (string, []*net.SRV, error) {
		return "", nil, errors.New("no record")
	}
	if _, err := ResolveSRV(context.Background(), "_missing._tcp.example.test"); err == nil {
		t.Fatal("missing SRV record did not fail")
	}
}
