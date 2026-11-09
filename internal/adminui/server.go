package adminui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/voidstarit/kouja/internal/agent"
	"github.com/voidstarit/kouja/internal/config"
	"github.com/voidstarit/kouja/internal/fsutil"
	"github.com/voidstarit/kouja/internal/lvm"
	"github.com/voidstarit/kouja/internal/observability"
	"github.com/voidstarit/kouja/internal/pki"
	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/target"
)

var (
	errBadRequest           = errors.New("adminui: bad request")
	errTooLarge             = errors.New("adminui: request body too large")
	errConflict             = errors.New("adminui: conflict")
	errPreconditionRequired = errors.New("adminui: precondition required")
	errInvalidConfig        = errors.New("adminui: invalid configuration")
	errUnsafePath           = errors.New("adminui: unsafe path")
	errInternal             = errors.New("adminui: internal error")
	errLocked               = errors.New("adminui: configuration lock busy")
	errStoreUnavailable     = errors.New("adminui: state store unavailable")
	errConfigMarshal        = errors.New("adminui: running configuration marshal failed")
)

type Store interface {
	Transact(context.Context, func(state.Tx) error) error
}

type CapacityProvider interface {
	Capacity(context.Context, string) (lvm.Capacity, error)
}

type TargetDisabler interface {
	DisableTarget(context.Context, string, string) error
}

type TargetEnabler interface {
	EnableTarget(context.Context, string, string) error
}

type Options struct {
	ConfigPath      string
	Config          *config.Config
	ConfigData      []byte
	Store           Store
	Capacity        CapacityProvider
	VGs             agent.VGService
	Classes         agent.ClassRegistry
	VGClass         *agent.VGClassManager
	Ready           func() bool
	HostUUID        string
	AgentVersion    string
	APIVersion      string
	StartedAt       time.Time
	Logger          *slog.Logger
	Now             func() time.Time
	Assets          fs.FS
	Listener        net.Listener
	Metrics         *observability.Metrics
	TargetMetrics   target.ISCSIMetricsProvider
	DisableTarget   TargetDisabler
	EnableTarget    TargetEnabler
	VGClassGate     *sync.Mutex
	Issuer          pki.Issuer
	Registry        pki.Registry
	ExternalRevoker pki.ExternalRevoker
	Files           pki.FileStore
	CAStatus        pki.CAStatus
}

type Server struct {
	options       Options
	httpServer    *http.Server
	listener      net.Listener
	allowedHosts  map[string]struct{}
	stateMu       sync.Mutex
	statusMu      sync.Mutex
	metricsMu     sync.Mutex
	lastMetrics   *metricsSample
	lastGoodError string
	vgClassGate   *sync.Mutex
	assets        *assetFS
}

func New(options Options) (*Server, error) {
	if options.ConfigPath == "" {
		return nil, fmt.Errorf("adminui: ConfigPath is required")
	}
	if options.Config == nil || options.ConfigData == nil || options.Store == nil ||
		options.Capacity == nil || options.VGs == nil || options.Classes == nil {
		return nil, fmt.Errorf("adminui: incomplete server dependencies")
	}
	if !options.Config.AdminUI.Enabled {
		return nil, fmt.Errorf("adminui: admin UI is disabled")
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.StartedAt.IsZero() {
		options.StartedAt = time.Now()
	}
	if options.Ready == nil {
		options.Ready = func() bool { return true }
	}
	if options.VGClassGate == nil {
		options.VGClassGate = &sync.Mutex{}
	}
	listener := options.Listener
	if listener == nil {
		var err error
		listener, err = net.Listen("tcp", options.Config.AdminUI.Address)
		if err != nil {
			return nil, fmt.Errorf("adminui: listen %s: %w", options.Config.AdminUI.Address, err)
		}
	}
	if !isLoopbackListener(listener) {
		_ = listener.Close()
		return nil, fmt.Errorf("adminui: listener address is not loopback")
	}
	server := &Server{
		options:      options,
		listener:     listener,
		allowedHosts: make(map[string]struct{}),
		vgClassGate:  options.VGClassGate,
	}
	port := listenerPort(listener.Addr())
	for _, host := range []string{"127.0.0.1:" + port, "[::1]:" + port, "localhost:" + port} {
		server.allowedHosts[host] = struct{}{}
	}
	if options.Assets != nil {
		server.assets = newAssetFS(options.Assets)
	} else {
		server.assets = defaultAssets()
	}
	server.httpServer = &http.Server{
		Handler:           server.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	return server, nil
}

func (server *Server) Handler() http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				server.options.Logger.Error("admin UI panic", "error", recovered)
				writeAPIError(response, http.StatusInternalServerError, "internal_error", "internal error")
			}
		}()
		server.securityHeaders(response)
		if !server.allowedHost(request.Host) {
			writeAPIError(response, http.StatusForbidden, "forbidden", "host is not allowed")
			return
		}
		server.serveHTTP(response, request)
	})
}

func (server *Server) Serve() error {
	if server.options.Metrics != nil {
		server.options.Metrics.Set("kouja_admin_ui_up", 1)
	}
	err := server.httpServer.Serve(server.listener)
	if server.options.Metrics != nil {
		server.options.Metrics.Set("kouja_admin_ui_up", 0)
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (server *Server) Shutdown(ctx context.Context) error {
	if server.options.Metrics != nil {
		server.options.Metrics.Set("kouja_admin_ui_up", 0)
	}
	return server.httpServer.Shutdown(ctx)
}

func (server *Server) Address() net.Addr {
	return server.listener.Addr()
}

func (server *Server) MarkRunning() error {
	err := fsutil.WriteFileAtomic(server.options.ConfigPath+".last-good", server.options.ConfigData, 0o600)
	server.stateMu.Lock()
	defer server.stateMu.Unlock()
	if err != nil {
		server.lastGoodError = err.Error()
		return err
	}
	server.lastGoodError = ""
	return nil
}

func (server *Server) serveHTTP(response http.ResponseWriter, request *http.Request) {
	switch {
	case request.URL.Path == "/":
		if request.Method != http.MethodGet {
			methodNotAllowed(response)
			return
		}
		server.serveIndex(response)
	case request.URL.Path == "/favicon.ico":
		if request.Method != http.MethodGet {
			methodNotAllowed(response)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	case strings.HasPrefix(request.URL.Path, "/assets/"):
		if request.Method != http.MethodGet {
			methodNotAllowed(response)
			return
		}
		server.serveAsset(response, request)
	case request.URL.Path == "/api/status":
		if request.Method != http.MethodGet {
			methodNotAllowed(response)
			return
		}
		server.handleStatus(response, request)
	case request.URL.Path == "/api/config":
		switch request.Method {
		case http.MethodGet:
			server.handleGetConfig(response, request)
		case http.MethodPut:
			if !server.authorizeMutation(response, request, "application/yaml") {
				return
			}
			server.handlePutConfig(response, request)
		default:
			methodNotAllowed(response)
		}
	case request.URL.Path == "/api/vgs":
		switch request.Method {
		case http.MethodGet:
			server.handleListVGs(response, request)
		case http.MethodPost:
			if !server.authorizeMutation(response, request, "application/json") {
				return
			}
			server.handleRegisterVG(response, request)
		default:
			methodNotAllowed(response)
		}
	case strings.HasPrefix(request.URL.Path, "/api/vgs/"):
		if request.Method != http.MethodDelete {
			methodNotAllowed(response)
			return
		}
		if !server.authorizeMutation(response, request, "application/json") {
			return
		}
		server.handleDeregisterVG(response, request)
	case request.URL.Path == "/api/targets/disable":
		if request.Method != http.MethodPost {
			methodNotAllowed(response)
			return
		}
		if !server.authorizeMutation(response, request, "application/json") {
			return
		}
		server.handleDisableTarget(response, request)
	case request.URL.Path == "/api/targets/enable":
		if request.Method != http.MethodPost {
			methodNotAllowed(response)
			return
		}
		if !server.authorizeMutation(response, request, "application/json") {
			return
		}
		server.handleEnableTarget(response, request)
	case request.URL.Path == "/api/classes":
		if request.Method != http.MethodGet {
			methodNotAllowed(response)
			return
		}
		server.handleListClasses(response, request)
	case request.URL.Path == "/api/ca":
		if request.Method != http.MethodGet {
			methodNotAllowed(response)
			return
		}
		server.handleDownloadCA(response, request)
	case request.URL.Path == "/api/certificates":
		switch request.Method {
		case http.MethodGet:
			server.handleListCertificates(response, request)
		case http.MethodPost:
			if !server.authorizeMutation(response, request, "application/json") {
				return
			}
			server.handleCreateCertificate(response, request)
		default:
			methodNotAllowed(response)
		}

	case request.URL.Path == "/api/certificates:revoke-external":
		if request.Method != http.MethodPost {
			methodNotAllowed(response)
			return
		}
		if !server.authorizeMutation(response, request, "application/json") {
			return
		}
		server.handleRevokeExternalCertificate(response, request)

	case strings.HasPrefix(request.URL.Path, "/api/certificates/"):
		switch request.Method {
		case http.MethodGet:
			server.handleDownloadCertificate(response, request)
		case http.MethodDelete:
			if !server.authorizeMutation(response, request, "application/json") {
				return
			}
			server.handleRevokeCertificate(response, request)
		default:
			methodNotAllowed(response)
		}
	case strings.HasPrefix(request.URL.Path, "/api/classes/"):
		if request.Method != http.MethodPut && request.Method != http.MethodDelete {
			methodNotAllowed(response)
			return
		}
		if !server.authorizeMutation(response, request, "application/json") {
			return
		}
		if request.Method == http.MethodPut {
			server.handleSetClass(response, request)
		} else {
			server.handleRemoveClass(response, request)
		}
	default:
		writeAPIError(response, http.StatusNotFound, "not_found", "route not found")
	}
}

func (server *Server) handleStatus(response http.ResponseWriter, request *http.Request) {
	server.statusMu.Lock()
	defer server.statusMu.Unlock()
	ctx, cancel := context.WithTimeout(request.Context(), 10*time.Second)
	defer cancel()
	status, err := server.buildStatus(ctx)
	if err != nil {
		if errors.Is(err, errConfigMarshal) {
			writeAPIError(response, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		writeAPIError(response, http.StatusServiceUnavailable, "unavailable", err.Error())
		return
	}
	writeJSON(response, http.StatusOK, status)
}

func (server *Server) handleGetConfig(response http.ResponseWriter, request *http.Request) {
	configState, err := server.configState()
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(response, http.StatusOK, configState)
}

func (server *Server) handlePutConfig(response http.ResponseWriter, request *http.Request) {
	body, err := readLimitedBody(response, request, 1<<20)
	if err != nil {
		writeMappedError(response, err)
		return
	}
	result, err := server.saveConfig(request.Context(), body, request.Header.Get("If-Match"), request.Header.Get("If-None-Match"), request.RemoteAddr)
	if err != nil {
		writeMappedError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (server *Server) handleListVGs(response http.ResponseWriter, request *http.Request) {
	vgs, err := server.listVGStatuses(request.Context())
	if err != nil {
		writeMappedError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, VGListResponse{VGs: vgs})
}

func (server *Server) handleListClasses(response http.ResponseWriter, _ *http.Request) {
	classes := server.options.Classes.Snapshot()
	if classes == nil {
		classes = map[string]string{}
	}
	writeJSON(response, http.StatusOK, ClassListResponse{Classes: classes})
}

func (server *Server) allowedHost(host string) bool {
	_, ok := server.allowedHosts[host]
	if ok {
		return true
	}
	// An IPv6 Host header may be formatted without brackets only in malformed
	// requests; reject it rather than guessing.
	return false
}

func (server *Server) authorizeMutation(response http.ResponseWriter, request *http.Request, contentType string) bool {
	if request.Header.Get("Sec-Fetch-Site") == "cross-site" {
		writeAPIError(response, http.StatusForbidden, "forbidden", "cross-site request rejected")
		return false
	}
	if origin := request.Header.Get("Origin"); origin != "" {
		if origin == "null" {
			writeAPIError(response, http.StatusForbidden, "forbidden", "null origin rejected")
			return false
		}
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme != "http" || parsed.Host == "" || !server.allowedHost(parsed.Host) {
			writeAPIError(response, http.StatusForbidden, "forbidden", "origin is not allowed")
			return false
		}
	}
	if !strings.HasPrefix(request.Header.Get("Content-Type"), contentType) {
		writeAPIError(response, http.StatusUnsupportedMediaType, "unsupported_media_type", "unexpected content type")
		return false
	}
	return true
}

func (server *Server) securityHeaders(response http.ResponseWriter) {
	response.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.Header().Set("X-Frame-Options", "DENY")
	response.Header().Set("Referrer-Policy", "no-referrer")
}

func (server *Server) serveIndex(response http.ResponseWriter) {
	if server.assets == nil {
		server.serveFallback(response)
		return
	}
	data, err := server.assets.read("index.html")
	if err != nil {
		server.serveFallback(response)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = response.Write(data) //nolint:gosec // embedded, build-time HTML
}

func (server *Server) serveFallback(response http.ResponseWriter) {
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.WriteHeader(http.StatusServiceUnavailable)
	if server.assets != nil {
		if data, err := server.assets.readFallback(); err == nil {
			_, _ = response.Write(data) //nolint:gosec // embedded, build-time HTML
			return
		}
	}
	_, _ = io.WriteString(response, "<!doctype html><title>kouja admin UI</title><p>UI assets are not built. Run <code>make build</code>.</p>")
}

func (server *Server) serveAsset(response http.ResponseWriter, request *http.Request) {
	relative := strings.TrimPrefix(request.URL.Path, "/assets/")
	if relative == "" || strings.Contains(relative, "..") || strings.HasPrefix(path.Base(relative), ".") || strings.HasSuffix(relative, "/") {
		writeAPIError(response, http.StatusNotFound, "not_found", "asset not found")
		return
	}
	if server.assets == nil {
		writeAPIError(response, http.StatusNotFound, "not_found", "asset not found")
		return
	}
	data, err := server.assets.readAsset(relative)
	if err != nil {
		writeAPIError(response, http.StatusNotFound, "not_found", "asset not found")
		return
	}
	contentType := mime.TypeByExtension(path.Ext(relative))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	response.Header().Set("Content-Type", contentType)
	response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = response.Write(data) //nolint:gosec // embedded, build-time HTML
}

func newJSONEncoder(writer io.Writer) *json.Encoder {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	encoder := newJSONEncoder(response)
	_ = encoder.Encode(value)
}

func writeAPIError(response http.ResponseWriter, status int, code, message string) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	encoder := newJSONEncoder(response)
	_ = encoder.Encode(APIError{Error: message, Code: code})
}

func methodNotAllowed(response http.ResponseWriter) {
	writeAPIError(response, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

func writeMappedError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errBadRequest):
		writeAPIError(response, http.StatusBadRequest, "bad_request", err.Error())
	case errors.Is(err, errTooLarge):
		writeAPIError(response, http.StatusRequestEntityTooLarge, "too_large", err.Error())
	case errors.Is(err, errInvalidConfig):
		writeAPIError(response, http.StatusUnprocessableEntity, "invalid_config", err.Error())
	case errors.Is(err, errUnsafePath):
		writeAPIError(response, http.StatusUnprocessableEntity, "invalid_config", err.Error())
	case errors.Is(err, errConflict):
		writeAPIError(response, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, errPreconditionRequired):
		writeAPIError(response, http.StatusPreconditionRequired, "precondition_required", err.Error())
	case errors.Is(err, errLocked):
		writeAPIError(response, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, errStoreUnavailable):
		writeAPIError(response, http.StatusServiceUnavailable, "unavailable", err.Error())
	case errors.Is(err, agent.ErrVGInUse):
		writeAPIError(response, http.StatusConflict, "vg_in_use", err.Error())
	case errors.Is(err, agent.ErrVGConflict):
		writeAPIError(response, http.StatusConflict, "vg_conflict", err.Error())
	case errors.Is(err, agent.ErrVGClassMapped):
		writeAPIError(response, http.StatusConflict, "class_conflict", err.Error())
	case errors.Is(err, agent.ErrVGUnavailable):
		writeAPIError(response, http.StatusUnprocessableEntity, "vg_unavailable", err.Error())
	case errors.Is(err, agent.ErrVGUUIDMismatch):
		writeAPIError(response, http.StatusUnprocessableEntity, "vg_unavailable", err.Error())
	case errors.Is(err, agent.ErrVGNotFound):
		writeAPIError(response, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, config.ErrInvalid):
		writeAPIError(response, http.StatusUnprocessableEntity, "invalid_config", err.Error())
	case errors.Is(err, config.ErrClassNotFound):
		writeAPIError(response, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, config.ErrClassConflict):
		writeAPIError(response, http.StatusConflict, "class_conflict", err.Error())
	case errors.Is(err, config.ErrLocked):
		writeAPIError(response, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, config.ErrUnsafePath):
		writeAPIError(response, http.StatusUnprocessableEntity, "invalid_config", err.Error())
	default:
		writeAPIError(response, http.StatusInternalServerError, "internal_error", "internal error")
	}
}

func readLimitedBody(response http.ResponseWriter, request *http.Request, limit int64) ([]byte, error) {
	request.Body = http.MaxBytesReader(response, request.Body, limit)
	data, err := io.ReadAll(request.Body)
	if err != nil {
		if strings.Contains(err.Error(), "request body too large") {
			return nil, errTooLarge
		}
		return nil, fmt.Errorf("%w: %v", errBadRequest, err)
	}
	return data, nil
}

func isLoopbackListener(listener net.Listener) bool {
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return false
	}
	return address.IP.Equal(net.ParseIP("127.0.0.1")) || address.IP.Equal(net.ParseIP("::1"))
}

func listenerPort(address net.Addr) string {
	if tcp, ok := address.(*net.TCPAddr); ok {
		return fmt.Sprintf("%d", tcp.Port)
	}
	_, port, err := net.SplitHostPort(address.String())
	if err != nil {
		return ""
	}
	return port
}
