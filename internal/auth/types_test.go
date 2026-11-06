package auth

import (
	"crypto/x509"
	"net/url"
	"testing"
)

func uri(t testing.TB, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func bddTestParseRolesAndErrors(t testing.TB) {
	for _, test := range []struct {
		san  string
		role Role
	}{
		{"spiffe://kouja/caller/one", RoleCaller},
		{"spiffe://kouja/controller/one", RoleController},
		{"spiffe://kouja/node/one", RoleNode},
		{"spiffe://kouja/admin/one", RoleAdmin},
	} {
		identity, err := Parse(&x509.Certificate{URIs: []*url.URL{uri(t, test.san)}})
		if err != nil || identity.Role != test.role || identity.Owner != test.san {
			t.Fatalf("Parse(%s) = %#v, %v", test.san, identity, err)
		}
	}
	if _, err := Parse(nil); err == nil {
		t.Fatal("nil certificate did not fail")
	}
	if _, err := Parse(&x509.Certificate{URIs: []*url.URL{uri(t, "spiffe://other/role/one")}}); err == nil {
		t.Fatal("foreign SPIFFE domain did not fail")
	}
	if _, err := Parse(&x509.Certificate{URIs: []*url.URL{uri(t, "spiffe://kouja/unknown/one")}}); err == nil {
		t.Fatal("unknown role did not fail")
	}
}
