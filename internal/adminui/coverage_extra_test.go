package adminui

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/voidstarit/kouja/internal/pki"
	"github.com/voidstarit/kouja/internal/warnings"
)

func TestWritePKIErrorBranches(t *testing.T) {
	cases := []struct {
		err    error
		status int
	}{
		{pki.ErrDisabled, http.StatusServiceUnavailable},
		{pki.ErrCAUnavailable, http.StatusServiceUnavailable},
		{pki.ErrInvalidRole, http.StatusBadRequest},
		{pki.ErrInvalidName, http.StatusBadRequest},
		{pki.ErrInvalidTTL, http.StatusBadRequest},
		{pki.ErrInvalidSerial, http.StatusBadRequest},
		{pki.ErrExternalInvalid, http.StatusBadRequest},
		{pki.ErrQuotaExceeded, http.StatusTooManyRequests},
		{pki.ErrNotFound, http.StatusNotFound},
		{pki.ErrJournalUnhealthy, http.StatusServiceUnavailable},
		{errors.New("other"), http.StatusInternalServerError},
	}
	for _, test := range cases {
		recorder := httptest.NewRecorder()
		writePKIError(recorder, test.err)
		if recorder.Code != test.status {
			t.Errorf("writePKIError(%v) = %d, want %d", test.err, recorder.Code, test.status)
		}
	}
}

type testAddress string

func (a testAddress) Network() string { return "test" }
func (a testAddress) String() string  { return string(a) }

func TestListenerPortAndViewHelpers(t *testing.T) {
	if got := listenerPort(&net.TCPAddr{Port: 1234}); got != "1234" {
		t.Fatalf("TCP listener port = %q", got)
	}
	if got := listenerPort(testAddress("127.0.0.1:4321")); got != "4321" {
		t.Fatalf("fallback listener port = %q", got)
	}
	if got := listenerPort(testAddress("not-a-port")); got != "" {
		t.Fatalf("invalid listener port = %q", got)
	}
	view := warningsView([]warnings.Warning{{Code: "CODE", Message: "message"}})
	if len(view) != 1 || view[0].Code != "CODE" || view[0].Message != "message" {
		t.Fatalf("warningsView = %#v", view)
	}
	file := issuedFile("admin", "name", "serial", "private_key", time.Now())
	if file.Name != "admin-name-serial.key" || file.Kind != "private_key" || file.CertificateName != "name" {
		t.Fatalf("issuedFile = %#v", file)
	}
	certificate := issuedFile("admin", "name", "serial", "certificate", time.Now())
	if certificate.Name != "admin-name-serial.crt" || certificate.Kind != "certificate" {
		t.Fatalf("issuedFile certificate = %#v", certificate)
	}
}
