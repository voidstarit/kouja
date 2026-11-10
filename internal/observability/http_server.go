package observability

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

// HTTPServer is a small plaintext listener for the dedicated metrics scrape
// endpoint. It deliberately has no authentication; callers should bind it to
// loopback or a restricted network address.
type HTTPServer struct {
	server   *http.Server
	listener net.Listener
}

func NewHTTPServer(address string, handler http.Handler) (*HTTPServer, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	return &HTTPServer{
		server: &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
		listener: listener,
	}, nil
}

func (server *HTTPServer) Addr() net.Addr {
	return server.listener.Addr()
}

func (server *HTTPServer) Serve() error {
	err := server.server.Serve(server.listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (server *HTTPServer) Shutdown(ctx context.Context) error {
	return server.server.Shutdown(ctx)
}
