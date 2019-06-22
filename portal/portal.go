package portal

import (
	"bytes"
	"fmt"
	"github.com/golang/protobuf/proto"
	"github.com/mcxr4299/http_portal/portalcore"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

var logger Logger = defaultLogger(0)

type Logger interface {
	Printf(format string, args ...interface{})
	Errorf(format string, args ...interface{})
}

type LocalPortal struct {
	name       string
	handler    http.Handler
	remoteHost string
	conn       net.Conn
	sendLock   sync.Mutex
	receiver   portalcore.MessageReceiver
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
	conn, err := net.Dial("tcp", p.remoteHost)
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
	header := &portalcore.RpcHeader{
		Method: portalcore.MethodLogin,
		Seq:    seq,
	}
	req := &portalcore.LoginRequest{
		Name: p.name,
	}
	ctx, _ := context.WithTimeout(context.Background(), 5*time.Second)
	ctx = context.WithValue(ctx, "lock", &p.sendLock)
	err := portalcore.Send(ctx, p.conn, header, req)
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
		resp := respPkg.Msg.(*portalcore.AckResponse)
		if resp.Code != portalcore.AckCode_Success {
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
	respHeader := &portalcore.RpcHeader{
		Method: portalcore.MethodHeartbeat,
		Seq:    seq,
	}
	req := &portalcore.HeartbeatPkg{
		Timestamp: time.Now().Unix(),
	}
	tc := context.WithValue(ctx, "lock", p.sendLock)
	tc, _ = context.WithTimeout(ctx, time.Second*5)
	err := portalcore.Send(tc, p.conn, respHeader, req)
	if err != nil {
		if !portalcore.IsTemporary(err) {
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
		resp := respPkg.Msg.(*portalcore.AckResponse)
		if resp.Code != portalcore.AckCode_Success {
			logger.Errorf("heartbeat error, code:%d", resp.Code)
			if resp.Code == portalcore.AckCode_NotLogin {
				// 重新登录
				p.isLogin = false
				err = p.login()
				if err != nil {
					logger.Printf("re-login error: %s", err)
					return
				}
			}
		} else {
			logger.Printf("heartbeat ok")
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
		header, msg, err := portalcore.Receive(ctx, p.conn, p.getMsg, true)
		if err != nil {
			if portalcore.IsClose(err) {
				logger.Errorf("receive error, close conn, remote:%s err:%s", p.conn.RemoteAddr(), err)
				return
			}
			tempDelay = portalcore.CalcDelay(tempDelay)
			logger.Errorf("receive error, retry in:%s, remote:%s error:%s", tempDelay, p.conn.RemoteAddr(), err)
			portalcore.Sleep(tempDelay, p.quit)
			continue
		}

		respHeader := &portalcore.RpcHeader{}
		tc := context.WithValue(ctx, "lock", p.sendLock)
		tc, _ = context.WithTimeout(tc, time.Second*10)
		err = p.processMsg(header, msg)
		if err != nil {
			logger.Errorf("service deal msg error, %s", err)
			respHeader.Error = err.Error()
		}
	}
}

func (p *LocalPortal) getMsg(header *portalcore.RpcHeader) (proto.Message, error) {
	switch header.Method {
	case portalcore.MethodHttpDo:
		return &portalcore.HttpRequest{}, nil
	case "resp_" + portalcore.MethodLogin:
		return p.loginGetMsg(header)
	case "resp_" + portalcore.MethodHeartbeat:
		return &portalcore.AckResponse{}, nil
	default:
		return nil, errors.New("method not support")
	}
}

func (p *LocalPortal) processMsg(header *portalcore.RpcHeader, msg proto.Message) error {
	switch header.Method {
	case portalcore.MethodHttpDo:
		go p.httpRequest(header, msg.(*portalcore.HttpRequest))
		return nil
	case "resp_" + portalcore.MethodLogin:
		return p.receiver.SendResponse(header.Seq, &portalcore.RpcMessage{Header: header, Msg: msg})
	case "resp_" + portalcore.MethodHeartbeat:
		return p.receiver.SendResponse(header.Seq, &portalcore.RpcMessage{Header: header, Msg: msg})
	default:
		return errors.New("method not support")
	}
}

func (p *LocalPortal) loginGetMsg(header *portalcore.RpcHeader) (proto.Message, error) {
	if len(header.Error) > 0 {
		return nil, errors.New("login failed, " + header.Error)
	}
	if header.Method != "resp_"+portalcore.MethodLogin {
		return nil, errors.New("login resp error")
	}
	return &portalcore.AckResponse{}, nil
}

func (p *LocalPortal) httpRequest(header *portalcore.RpcHeader, req *portalcore.HttpRequest) {
	errResp := &portalcore.HttpResponse{Status: 500}
	respHeader := portalcore.NewResponseHeader(header)
	r, err := parseHttpRequest(req)
	if err != nil {
		logger.Errorf("parse http request error, %s", err.Error())
		respHeader.Error = err.Error()
		p.sendHttpResponse(respHeader, errResp)
		return
	}
	w := newHttpResponseWriter(p.bytesPool.Get().(*bytes.Buffer))
	defer p.bytesPool.Put(w.Buf)
	p.handler.ServeHTTP(w, r)
	w.mix()
	p.sendHttpResponse(respHeader, &w.resp)
}

func (p *LocalPortal) sendHttpResponse(header *portalcore.RpcHeader, resp *portalcore.HttpResponse) {
	ctx := context.Background()
	err := portalcore.Send(ctx, p.conn, header, resp)
	if err != nil {
		logger.Errorf("send http response error, %s", err.Error())
	}
}

func parseHttpRequest(req *portalcore.HttpRequest) (*http.Request, error) {
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
	resp   portalcore.HttpResponse
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
		w.resp.Header = append(w.resp.Header, &portalcore.HttpRequest_Header{
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
	fmt.Printf(format+"\n", args...)
}

func SetLogger(l Logger) {
	if l != nil {
		logger = l
	}
}
