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
	Owner        string
	Role         Role
	SerialNumber string
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
		serial := ""
		if peer.SerialNumber != nil {
			serial = strings.ToLower(peer.SerialNumber.Text(16))
		}
		switch parts[0] {
		case "caller":
			return Identity{Owner: uri.String(), Role: RoleCaller, SerialNumber: serial}, nil
		case "controller":
			return Identity{Owner: uri.String(), Role: RoleController, SerialNumber: serial}, nil
		case "node":
			return Identity{Owner: uri.String(), Role: RoleNode, SerialNumber: serial}, nil
		case "admin":
			return Identity{Owner: uri.String(), Role: RoleAdmin, SerialNumber: serial}, nil
		}
	}
	return Identity{}, errors.New("no kouja URI SAN")
}
