package csi

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
)

var lookupSRV = net.LookupSRV

// ResolveSRV resolves an SRV record into kouja agent endpoints. The caller can
// pass a full record name such as _kouja._tcp.example.internal, or a name
// relative to the cluster search domain.
func ResolveSRV(ctx context.Context, record string) ([]string, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	_, values, err := lookupSRV("", "", strings.TrimSpace(record))
	if err != nil {
		return nil, fmt.Errorf("csi: resolve SRV %s: %w", record, err)
	}
	endpoints := make([]string, 0, len(values))
	for _, value := range values {
		host := strings.TrimSuffix(value.Target, ".")
		if host == "" {
			continue
		}
		endpoints = append(endpoints, net.JoinHostPort(host, fmt.Sprintf("%d", value.Port)))
	}
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("csi: SRV %s returned no endpoints", record)
	}
	sort.Strings(endpoints)
	return endpoints, nil
}
