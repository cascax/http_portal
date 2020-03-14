package portal

import (
	"bufio"
	"context"
	"fmt"
	"github.com/cascax/http_portal/core"
	"github.com/pkg/errors"
	"golang.org/x/net/websocket"
	"net"
	"net/http"
	"net/url"
	"strings"
)

type WebsocketHandler interface {
	http.Handler
	Handshake(*websocket.Config, *http.Request) error
}

type websocketProxy struct {
	ctx        context.Context
	portalConn net.Conn
	readWriter *bufio.ReadWriter
	isLogin    bool
	timeout    TimeoutConfig

	isClose  bool
	hijacked bool
}

func newWebsocketProxy(ctx context.Context, timeout TimeoutConfig) *websocketProxy {
	return &websocketProxy{
		ctx:     ctx,
		isLogin: false,
		timeout: timeout,
	}
}

func (p *websocketProxy) Connect(remoteHost string, loginReq *core.LoginRequest) error {
	conn, err := net.DialTimeout("tcp", remoteHost, p.timeout.ServerConnect)
	if err != nil {
		return errors.WithMessage(err, "dial error")
	}
	p.portalConn = conn
	p.readWriter = bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	logger.Infof("connect server %s for websocket", remoteHost)

	err = p.login(loginReq)
	if err != nil {
		return err
	}
	return nil
}

func (p *websocketProxy) login(loginReq *core.LoginRequest) error {
	header := core.RpcHeader{
		Method: core.MethodLogin,
		Seq:    0,
	}
	ctx, _ := context.WithTimeout(p.ctx, p.timeout.ServerWrite)
	err := core.Send(ctx, p.portalConn, header, loginReq)
	if err != nil {
		return errors.WithMessagef(err, "send websocket login error")
	}

	// receive login response
	_, respMsg, err := core.Receive(p.ctx, p.portalConn, loginGetMsg, true)
	if err != nil {
		return errors.WithMessage(err, "login receive response error")
	}
	resp := respMsg.(*core.AckResponse)
	if resp.Code != core.AckCode_Success {
		return errors.Errorf("login failed, code:%d", resp.Code)
	} else {
		logger.Infof("login success")
	}
	p.isLogin = true
	return nil
}

func (p *websocketProxy) ServeWebSocket(handler http.Handler, req *http.Request) {
	handler.ServeHTTP(p, req)
	p.readWriter.Flush()
	p.Close()
}

func (p *websocketProxy) WriteErrorResponse(statusCode int, err error) {
	_, _ = fmt.Fprintf(p.readWriter, "HTTP/1.1 %03d %s\r\n", statusCode, err.Error())
	_, _ = p.readWriter.WriteString("\r\n")
	_ = p.readWriter.Flush()
	p.Close()
}

func (p *websocketProxy) Hijack() (rwc net.Conn, buf *bufio.ReadWriter, err error) {
	if p.hijacked {
		return nil, nil, http.ErrHijacked
	}
	p.hijacked = true
	return p.portalConn, p.readWriter, nil
}

func (p *websocketProxy) Header() http.Header {
	logger.Errorf("ResponseWriter.Header not work, websocketProxy only for websocket")
	return make(http.Header)
}

func (p *websocketProxy) Write(_ []byte) (int, error) {
	return 0, errors.New("websocketProxy only for websocket")
}

func (p *websocketProxy) WriteHeader(_ int) {
	logger.Errorf("ResponseWriter.WriteHeader not work, websocketProxy only for websocket")
}

func (p *websocketProxy) Close() {
	_ = p.portalConn.Close()
}

func GetWebsocketConfig(req *http.Request) (*websocket.Config, int32, error) {
	config := &websocket.Config{
		Header: req.Header,
	}

	key := req.Header.Get("Sec-Websocket-Key")
	if key == "" {
		return config, http.StatusBadRequest, websocket.ErrChallengeResponse
	}
	version := req.Header.Get("Sec-Websocket-Version")
	switch version {
	case "13":
		config.Version = websocket.ProtocolVersionHybi13
	default:
		return config, http.StatusBadRequest, websocket.ErrBadWebSocketVersion
	}
	var scheme string
	if req.TLS != nil {
		scheme = "wss"
	} else {
		scheme = "ws"
	}
	var err error
	config.Location, err = url.ParseRequestURI(scheme + "://" + req.Host + req.URL.RequestURI())
	if err != nil {
		return config, http.StatusBadRequest, errors.WithMessage(err, "bad url")
	}
	origin := req.Header.Get("Origin")
	if origin == "" {
		return config, http.StatusBadRequest, errors.New("null origin")
	}
	config.Origin, err = url.ParseRequestURI(origin)
	if err != nil {
		return config, http.StatusBadRequest, errors.WithMessage(err, "bad origin")
	}
	protocol := strings.TrimSpace(req.Header.Get("Sec-Websocket-Protocol"))
	if protocol != "" {
		protocols := strings.Split(protocol, ",")
		for i := 0; i < len(protocols); i++ {
			config.Protocol = append(config.Protocol, strings.TrimSpace(protocols[i]))
		}
	}
	return config, http.StatusSwitchingProtocols, nil
}

func CtxWithWebsocketConfig(ctx context.Context, config *websocket.Config) context.Context {
	return context.WithValue(ctx, "WS-CONFIG", config)
}

func CtxGetWebsocketConfig(ctx context.Context) *websocket.Config {
	return ctx.Value("WS-CONFIG").(*websocket.Config)
}
