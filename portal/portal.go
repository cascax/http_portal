package portal

import (
	"bytes"
	"fmt"
	"github.com/cascax/http_portal/core"
	"github.com/golang/protobuf/proto"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

var logger completeLogger = defaultLogger(0)

type Logger interface {
	Printf(format string, args ...interface{})
	Errorf(format string, args ...interface{})
}

type DebugLogger interface {
	Debugf(format string, args ...interface{})
}

type completeLogger interface {
	Logger
	DebugLogger
}

type LocalPortal struct {
	name       string
	handler    http.Handler
	remoteHost string
	conn       net.Conn
	sendLock   sync.Mutex
	receiver   core.MessageReceiver
	isLogin    bool
	bytesPool  sync.Pool
	quit       chan struct{}
}

func NewLocalPortal(name string, handler http.Handler, remoteHost string) *LocalPortal {
	lp := &LocalPortal{
		name:       name,
		handler:    handler,
		remoteHost: remoteHost,
		quit:       make(chan struct{}),
	}
	lp.bytesPool.New = func() interface{} {
		return bytes.NewBuffer(make([]byte, 0, 2048))
	}
	return lp
}

func (p *LocalPortal) Start() {
	go p.Run()
}

func (p *LocalPortal) Run() {
	err := p.connect()
	if err != nil {
		logger.Errorf("dial %s error: %s", p.remoteHost, err)
		return
	}
	err = p.login()
	if err != nil {
		logger.Errorf("login error: %s", err)
		return
	}
	p.heartbeatLoop()
}

func (p *LocalPortal) connect() error {
	conn, err := net.DialTimeout("tcp", p.remoteHost, 5*time.Second)
	if err != nil {
		return errors.WithMessage(err, "dial error")
	}
	p.conn = conn
	logger.Printf("connect server %s", p.remoteHost)
	go p.receive()
	return nil
}

func (p *LocalPortal) login() error {
	seq, respCh := p.receiver.PrepareRequest()
	defer p.receiver.DeleteSeq(seq)
	header := &core.RpcHeader{
		Method: core.MethodLogin,
		Seq:    seq,
	}
	req := &core.LoginRequest{
		Name: p.name,
	}
	ctx, _ := context.WithTimeout(context.Background(), 5*time.Second)
	ctx = context.WithValue(ctx, "lock", &p.sendLock)
	err := core.Send(ctx, p.conn, header, req)
	if err != nil {
		return errors.WithMessagef(err, "send error, seq(%d)", seq)
	}

	// receive
	select {
	case <-ctx.Done():
		// 登录超时
		return errors.WithMessage(ctx.Err(), "login end")
	case respPkg := <-respCh:
		if respPkg.Header.Error != "" {
			return errors.WithMessage(errors.New(respPkg.Header.Error), "login error")
		}
		resp := respPkg.Msg.(*core.AckResponse)
		if resp.Code != core.AckCode_Success {
			return errors.Errorf("login failed, code:%d", resp.Code)
		} else {
			logger.Printf("login success")
		}
	}
	p.isLogin = true
	return nil
}

func (p *LocalPortal) heartbeatLoop() {
	t := time.NewTimer(time.Second * 5)
	for range t.C {
		select {
		case <-p.quit:
			t.Stop()
			break
		default:
		}
		if p.heartbeat() {
			t.Reset(time.Second * 5)
		} else {
			t.Reset(time.Second * 1)
		}
	}
}

func (p *LocalPortal) heartbeat() (succ bool) {
	succ = true
	if !p.isLogin {
		err := p.login()
		if err != nil {
			logger.Errorf("re-login error: %s", err)
		}
		return
	}
	ctx := context.Background()
	seq, respCh := p.receiver.PrepareRequest()
	respHeader := &core.RpcHeader{
		Method: core.MethodHeartbeat,
		Seq:    seq,
	}
	req := &core.HeartbeatPkg{
		Timestamp: time.Now().Unix(),
	}
	tc := context.WithValue(ctx, "lock", p.sendLock)
	tc, _ = context.WithTimeout(ctx, time.Second*5)
	err := core.Send(tc, p.conn, respHeader, req)
	if err != nil {
		if !core.IsTemporary(err) {
			logger.Errorf("send heartbeat error, close conn, %s", err.Error())
			err = p.connect()
			if err != nil {
				logger.Errorf("reconnect error, %s", err.Error())
			}
			_ = p.login()
			return false
		}
		logger.Errorf("send heartbeat error, %s", err.Error())
	}
	// receive
	select {
	case <-tc.Done():
		logger.Errorf("heartbeat canceled, %s", tc.Err())
	case respPkg := <-respCh:
		resp := respPkg.Msg.(*core.AckResponse)
		if resp.Code != core.AckCode_Success {
			logger.Errorf("heartbeat error, code:%d", resp.Code)
			if resp.Code == core.AckCode_NotLogin {
				// 重新登录
				p.isLogin = false
				err = p.login()
				if err != nil {
					logger.Printf("re-login error: %s", err)
					return
				}
			}
		} else {
			logger.Debugf("heartbeat ok")
		}
	}
	return true
}

func (p *LocalPortal) receive() {
	ctx := context.Background()
	var tempDelay time.Duration
	for {
		select {
		case <-p.quit:
			break
		default:
		}
		header, msg, err := core.Receive(ctx, p.conn, p.getMsg, true)
		if err != nil {
			if core.IsClose(err) {
				logger.Errorf("receive error, close conn, remote:%s err:%s", p.conn.RemoteAddr(), err)
				return
			}
			tempDelay = core.CalcDelay(tempDelay)
			logger.Errorf("receive error, retry in:%s, remote:%s error:%s", tempDelay, p.conn.RemoteAddr(), err)
			core.Sleep(tempDelay, p.quit)
			continue
		}

		respHeader := &core.RpcHeader{}
		tc := context.WithValue(ctx, "lock", p.sendLock)
		tc, _ = context.WithTimeout(tc, time.Second*10)
		err = p.processMsg(header, msg)
		if err != nil {
			logger.Errorf("service deal msg error, %s", err)
			respHeader.Error = err.Error()
		}
	}
}

func (p *LocalPortal) getMsg(header *core.RpcHeader) (proto.Message, error) {
	switch header.Method {
	case core.MethodHttpDo:
		return &core.HttpRequest{}, nil
	case core.RespMethodLogin:
		return p.loginGetMsg(header)
	case core.RespMethodHeartbeat:
		return &core.AckResponse{}, nil
	default:
		return nil, errors.New("method not support")
	}
}

func (p *LocalPortal) processMsg(header *core.RpcHeader, msg proto.Message) error {
	switch header.Method {
	case core.MethodHttpDo:
		go p.httpRequest(header, msg.(*core.HttpRequest))
		return nil
	case core.RespMethodLogin:
		return p.receiver.SendResponse(header.Seq, &core.RpcMessage{Header: header, Msg: msg})
	case core.RespMethodHeartbeat:
		return p.receiver.SendResponse(header.Seq, &core.RpcMessage{Header: header, Msg: msg})
	default:
		return errors.New("method not support")
	}
}

func (p *LocalPortal) loginGetMsg(header *core.RpcHeader) (proto.Message, error) {
	if len(header.Error) > 0 {
		return nil, errors.New("login failed, " + header.Error)
	}
	if header.Method != core.RespMethodLogin {
		return nil, errors.New("login resp error")
	}
	return &core.AckResponse{}, nil
}

func (p *LocalPortal) httpRequest(header *core.RpcHeader, req *core.HttpRequest) {
	errResp := &core.HttpResponse{Status: 500}
	respHeader := core.NewResponseHeader(header)
	r, err := parseHttpRequest(req)
	if err != nil {
		logger.Errorf("parse http request error, %s", err.Error())
		respHeader.Error = err.Error()
		p.sendHttpResponse(respHeader, errResp)
		return
	}
	buf := p.bytesPool.Get().(*bytes.Buffer)
	buf.Reset()
	w := newHttpResponseWriter(buf)
	defer p.bytesPool.Put(buf)
	p.handler.ServeHTTP(w, r)
	w.mix()
	p.sendHttpResponse(respHeader, &w.resp)
}

func (p *LocalPortal) sendHttpResponse(header *core.RpcHeader, resp *core.HttpResponse) {
	ctx := context.Background()
	err := core.Send(ctx, p.conn, header, resp)
	if err != nil {
		logger.Errorf("send http response error, %s", err.Error())
	}
}

func parseHttpRequest(req *core.HttpRequest) (*http.Request, error) {
	r := &http.Request{
		Method:     req.Method,
		Proto:      req.ReqProto,
		Header:     make(http.Header, len(req.Header)),
		RequestURI: req.Url,
		Host:       req.Host,
		RemoteAddr: req.RemoteAddr,
	}
	for _, h := range req.Header {
		r.Header[h.Key] = h.Value
	}
	r.Body = newBodyReader(req.Body)
	var ok bool
	if r.ProtoMajor, r.ProtoMinor, ok = http.ParseHTTPVersion(req.ReqProto); !ok {
		return nil, errors.Errorf("%s malformed HTTP version", req.ReqProto)
	}
	var err error
	// TODO: 只支持http
	if req.Url[0] != '/' {
		req.Url = "/" + req.Url
	}
	rawurl := fmt.Sprintf("http://%s%s", req.Host, req.Url)
	if r.URL, err = url.ParseRequestURI(rawurl); err != nil {
		return nil, errors.WithMessage(err, "parse uri error")
	}
	return r, nil
}

type HttpResponseWriter struct {
	resp   core.HttpResponse
	header http.Header
	Buf    *bytes.Buffer
}

func (w *HttpResponseWriter) Header() http.Header {
	return w.header
}

func (w *HttpResponseWriter) Write(data []byte) (int, error) {
	return w.Buf.Write(data)
}

func (w *HttpResponseWriter) WriteHeader(statusCode int) {
	w.resp.Status = int32(statusCode)
}

func (w *HttpResponseWriter) mix() {
	for k, arr := range w.header {
		w.resp.Header = append(w.resp.Header, &core.HttpRequest_Header{
			Key: k, Value: arr,
		})
	}
	w.resp.Body = w.Buf.Bytes()
}

func newHttpResponseWriter(buf *bytes.Buffer) *HttpResponseWriter {
	w := &HttpResponseWriter{
		header: make(http.Header),
		Buf:    buf,
	}
	w.resp.Status = 200
	return w
}

type BodyReader struct {
	Buf *bytes.Buffer
}

func (r *BodyReader) Read(p []byte) (n int, err error) {
	return r.Buf.Read(p)
}

func (r *BodyReader) Close() error {
	return nil
}

func newBodyReader(data []byte) *BodyReader {
	return &BodyReader{
		Buf: bytes.NewBuffer(data),
	}
}

type defaultLogger int

func (l defaultLogger) Printf(format string, args ...interface{}) {
	fmt.Printf(format+"\n", args...)
}

func (l defaultLogger) Errorf(format string, args ...interface{}) {
	fmt.Printf("error: "+format+"\n", args...)
}

func (l defaultLogger) Debugf(format string, args ...interface{}) {
	fmt.Printf(format+"\n", args...)
}

type noDebugLogger struct {
	Logger
}

func (l noDebugLogger) Debugf(format string, args ...interface{}) {}

func SetLogger(l Logger) {
	if l != nil {
		if cl, ok := l.(completeLogger); ok {
			logger = cl
		} else {
			logger = noDebugLogger{l}
		}
	}
}
