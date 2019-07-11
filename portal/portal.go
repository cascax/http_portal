package portal

import (
	"bytes"
	"fmt"
	"github.com/cascax/http_portal/core"
	"github.com/golang/protobuf/proto"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	ResponseSizeLimit        = 1024 * 1024 * 4
	ResponseReadSize         = 512
	DefaultHeartbeatInterval = 5 * time.Second
)

var logger completeLogger = defaultLogger(0)

type Logger interface {
	Infof(format string, args ...interface{})
	Errorf(format string, args ...interface{})
}

type DebugLogger interface {
	Debugf(format string, args ...interface{})
}

type completeLogger interface {
	Logger
	DebugLogger
}

type TimeoutConfig struct {
	ServerConnect time.Duration `yaml:"server_connect"`
	ServerWrite   time.Duration `yaml:"server_write"`
	HTTPRead      time.Duration `yaml:"http_read"`
}

type LocalPortal struct {
	name         string
	handler      http.Handler
	remoteHost   string
	conn         net.Conn
	sendLock     sync.Mutex
	receiver     core.MessageReceiver
	isLogin      bool
	quit         chan struct{}
	timeout      TimeoutConfig
	lastTransfer time.Time
}

func NewLocalPortal(name string, handler http.Handler, remoteHost string) *LocalPortal {
	lp := &LocalPortal{
		name:       name,
		handler:    handler,
		remoteHost: remoteHost,
		quit:       make(chan struct{}),
		timeout: TimeoutConfig{
			ServerConnect: 5 * time.Second,
			ServerWrite:   5 * time.Second,
		},
	}
	return lp
}

func (p *LocalPortal) SetTimeout(t TimeoutConfig) {
	p.timeout = t
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
	conn, err := net.DialTimeout("tcp", p.remoteHost, p.timeout.ServerConnect)
	if err != nil {
		return errors.WithMessage(err, "dial error")
	}
	p.conn = conn
	logger.Infof("connect server %s", p.remoteHost)
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
	ctx, _ := context.WithTimeout(context.Background(), p.timeout.ServerWrite)
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
			logger.Infof("login success")
		}
	}
	p.isLogin = true
	return nil
}

func (p *LocalPortal) heartbeatLoop() {
	t := time.NewTimer(time.Second * 5)
	failedTime := 0
	for range t.C {
		select {
		case <-p.quit:
			t.Stop()
			break
		default:
		}
		now := time.Now()
		if now.Sub(p.lastTransfer) < DefaultHeartbeatInterval {
			t.Reset(now.Sub(p.lastTransfer))
			continue
		}
		shortWait, succ := p.heartbeat()
		if succ {
			failedTime = 0
			p.lastTransfer = time.Now()
		} else {
			failedTime++
		}
		if failedTime >= 5 {
			_ = p.conn.Close()
			failedTime = 0
		}
		if shortWait {
			t.Reset(time.Second * 1)
		} else {
			t.Reset(DefaultHeartbeatInterval)
		}
	}
}

func (p *LocalPortal) heartbeat() (shortWait bool, succ bool) {
	if !p.isLogin {
		err := p.login()
		if err != nil {
			logger.Errorf("re-login error: %s", err)
		}
		return true, false
	}
	ctx := context.Background()
	seq, respCh := p.receiver.PrepareRequest()
	defer p.receiver.DeleteSeq(seq)
	respHeader := &core.RpcHeader{
		Method: core.MethodHeartbeat,
		Seq:    seq,
	}
	req := &core.HeartbeatPkg{
		Timestamp: time.Now().Unix(),
	}
	tc := context.WithValue(ctx, "lock", &p.sendLock)
	tc, _ = context.WithTimeout(ctx, p.timeout.ServerWrite)
	err := core.Send(tc, p.conn, respHeader, req)
	if err != nil {
		if !core.IsTemporary(err) {
			logger.Errorf("send heartbeat error, close conn, %s", err.Error())
			err = p.connect()
			if err != nil {
				logger.Errorf("reconnect error, %s", err.Error())
			}
			_ = p.login()
			return true, false
		}
		logger.Errorf("send heartbeat error, %s", err.Error())
		return false, false
	}
	// receive
	select {
	case <-tc.Done():
		logger.Errorf("heartbeat canceled, %s", tc.Err())
		return false, false
	case respPkg := <-respCh:
		resp := respPkg.Msg.(*core.AckResponse)
		if resp.Code != core.AckCode_Success {
			logger.Errorf("heartbeat error, code:%d", resp.Code)
			if resp.Code == core.AckCode_NotLogin {
				// 重新登录
				p.isLogin = false
				err = p.login()
				if err != nil {
					logger.Infof("re-login error: %s", err)
					return true, false
				}
			}
		} else {
			logger.Debugf("heartbeat ok")
		}
	}
	return false, true
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
		return nil, errors.Errorf("method(%s) not support", header.Method)
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

// 处理server发来的http请求
// 解析req后交给http.Handler处理，然后返回结果
func (p *LocalPortal) httpRequest(header *core.RpcHeader, req *core.HttpRequest) {
	errResp := &core.HttpResponse{Status: 500}
	respHeader := core.NewResponseHeader(header)
	r, err := parseHttpRequest(context.Background(), req)
	if err != nil {
		logger.Errorf("parse http request error, %s", err.Error())
		respHeader.Error = err.Error()
		_ = p.sendHttpResponse(respHeader, errResp)
		return
	}
	buf := core.GetBytesBuf()
	w := newHttpResponseWriter(buf.Bytes(), func(resp *core.HttpResponse) error {
		return p.sendHttpResponse(respHeader, resp)
	})
	defer core.PutBytesBuf(bytes.NewBuffer(w.buf))
	p.handler.ServeHTTP(w, r)
	err = w.Send(true)
	if err != nil {
		logger.Errorf("send the end resp error, %s", err.Error())
	} else {
		p.lastTransfer = time.Now()
	}
}

func (p *LocalPortal) sendHttpResponse(header *core.RpcHeader, resp *core.HttpResponse) error {
	ctx := context.WithValue(context.Background(), "lock", &p.sendLock)
	err := core.Send(ctx, p.conn, header, resp)
	if err != nil {
		logger.Errorf("send http response error, %s", err.Error())
		return err
	}
	logger.Debugf("send http response, seq:%d len:%d", header.Seq, len(resp.Body))
	return nil
}

func parseHttpRequest(ctx context.Context, req *core.HttpRequest) (*http.Request, error) {
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
	r.WithContext(ctx)
	return r, nil
}

type HttpResponseWriter struct {
	firstResp core.HttpResponse
	header    http.Header
	buf       []byte
	bufLen    int
	firstSend bool
	sendFunc  func(*core.HttpResponse) error
}

func (w *HttpResponseWriter) Header() http.Header {
	return w.header
}

func (w *HttpResponseWriter) Write(data []byte) (int, error) {
	l := len(data)
	e := w.growBuf(l)
	if e != nil {
		return 0, e
	}
	copy(w.buf[w.bufLen:], data)
	w.bufLen += l
	if w.bufLen >= ResponseSizeLimit {
		e := w.Send(false)
		if e != nil {
			return l, e
		}
	}
	return l, nil
}

func (w *HttpResponseWriter) WriteHeader(statusCode int) {
	w.firstResp.Status = int32(statusCode)
}

func (w *HttpResponseWriter) ReadFrom(reader io.Reader) (int, error) {
	hasRead := 0
	hasSend := w.bufLen
	for {
		e := w.growBuf(ResponseReadSize)
		if e != nil {
			return hasRead, e
		}
		end := cap(w.buf)
		if end > ResponseSizeLimit {
			end = ResponseSizeLimit
		}
		n, e := reader.Read(w.buf[w.bufLen:end])
		if n < 0 {
			panic(errors.New("bytes.Buffer: reader returned negative count from Read"))
		}
		w.bufLen += n
		hasRead += n
		hasSend += n
		if e != nil {
			if e == io.EOF {
				return hasRead, nil
			}
			return hasRead, e
		}
		if hasSend >= ResponseSizeLimit {
			e = w.Send(false)
			if e != nil {
				return hasRead, e
			}
			hasSend = 0
		}
	}
}

func (w *HttpResponseWriter) Send(finish bool) error {
	var resp *core.HttpResponse
	if w.firstSend {
		w.mix()
		w.firstSend = false
		resp = &w.firstResp
	} else {
		resp = &core.HttpResponse{
			Body: w.buf[:w.bufLen],
		}
	}
	resp.NotFinish = !finish
	err := w.sendFunc(resp)
	if err != nil {
		return errors.WithMessage(err, "send resp error")
	}
	w.bufLen = 0
	return nil
}

func (w *HttpResponseWriter) mix() {
	for k, arr := range w.header {
		w.firstResp.Header = append(w.firstResp.Header, &core.HttpRequest_Header{
			Key: k, Value: arr,
		})
	}
	w.firstResp.Body = w.buf[:w.bufLen]
}

func (w *HttpResponseWriter) growBuf(n int) error {
	c := cap(w.buf)
	if w.bufLen+n < c {
		return nil
	}
	if c+n > core.MaxReceiveSize {
		return bytes.ErrTooLarge
	}
	buf := make([]byte, 2*c+n)
	copy(buf, w.buf[:w.bufLen])
	w.buf = buf
	return nil
}

func newHttpResponseWriter(buf []byte, sendFunc func(*core.HttpResponse) error) *HttpResponseWriter {
	w := &HttpResponseWriter{
		header:    make(http.Header),
		buf:       buf,
		firstSend: true,
		sendFunc:  sendFunc,
	}
	w.firstResp.Status = 200
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

func (l defaultLogger) Infof(format string, args ...interface{}) {
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
