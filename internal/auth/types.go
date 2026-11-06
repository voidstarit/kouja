package auth

import (
	"crypto/x509"
	"errors"
	"strings"
)

type Role string

const (
	RoleCaller     Role = "caller"
	RoleController Role = "controller"
	RoleNode       Role = "node"
	RoleAdmin      Role = "admin"
)

type Identity struct {
	Owner string
	Role  Role
}

func Parse(peer *x509.Certificate) (Identity, error) {
	if peer == nil {
		return Identity{}, errors.New("missing peer certificate")
	}
	for _, uri := range peer.URIs {
		if uri.Scheme != "spiffe" || uri.Host != "kouja" {
			continue
		}
		parts := strings.Split(strings.Trim(uri.Path, "/"), "/")
		if len(parts) != 2 {
			continue
		}
		switch parts[0] {
		case "caller":
			return Identity{Owner: uri.String(), Role: RoleCaller}, nil
		case "controller":
			return Identity{Owner: uri.String(), Role: RoleController}, nil
		case "node":
			return Identity{Owner: uri.String(), Role: RoleNode}, nil
		case "admin":
			return Identity{Owner: uri.String(), Role: RoleAdmin}, nil
		}
	}
	return Identity{}, errors.New("no kouja URI SAN")
}
