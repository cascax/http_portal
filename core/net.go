package core

import (
	"context"
	"encoding/binary"
	"github.com/golang/protobuf/proto"
	"github.com/pkg/errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	RpcBeginFlag   uint32 = 0xFFF62556
	DefaultMsgSize        = 2048
)

var beginFlag = make([]byte, 4)
var bytesPool = sync.Pool{New: func() interface{} {
	return proto.NewBuffer(make([]byte, 0, DefaultMsgSize))
}}

func Send(ctx context.Context, conn net.Conn, header *RpcHeader, msg proto.Message) error {
	if len(header.Method) == 0 {
		return errors.New("no method set")
	}
	if d, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(d)
		defer conn.SetDeadline(time.Time{})
	}
	// send lock
	var lock *sync.Mutex
	if v := ctx.Value("lock"); v != nil {
		if mu, ok := v.(*sync.Mutex); ok {
			lock = mu
			lock.Lock()
			defer lock.Unlock()
		}
	}
	// send begin flag
	_, err := conn.Write(beginFlag)
	if err != nil {
		return newError(err, "write flag error")
	}

	// make message data
	buf := bytesPool.Get().(*proto.Buffer)
	buf.Reset()
	defer bytesPool.Put(buf)
	err = buf.Marshal(header)
	if err != nil {
		return newError(err, "marshal header error")
	}
	if msg != nil {
		err = buf.Marshal(msg)
		if err != nil {
			return newError(err, "marshal msg error")
		}
	}

	// send data length, header length and data
	lenData := make([]byte, 8)
	binary.BigEndian.PutUint32(lenData, uint32(len(buf.Bytes())))
	binary.BigEndian.PutUint32(lenData[4:], uint32(header.XXX_Size()))
	_, err = conn.Write(lenData)
	if err != nil {
		return newError(err, "write len error")
	}
	_, err = conn.Write(buf.Bytes())
	if err != nil {
		return newError(err, "write error")
	}

	return nil
}

func Receive(ctx context.Context, conn net.Conn, newMsg func(*RpcHeader) (proto.Message, error), wait bool) (header *RpcHeader, msg proto.Message, err error) {
	if d, ok := ctx.Deadline(); !wait && ok {
		_ = conn.SetDeadline(d)
		defer conn.SetDeadline(time.Time{})
	} else {
		_ = conn.SetDeadline(time.Time{})
	}
	// read
	headerData := make([]byte, 12)
	_, err = read(conn, headerData, len(headerData))
	if err != nil {
		return nil, nil, newError(err, "read header error")
	}
	if binary.BigEndian.Uint32(headerData[0:]) != RpcBeginFlag {
		return nil, nil, errors.New("begin flag wrong")
	}
	bodyLen := binary.BigEndian.Uint32(headerData[4:])
	headerLen := binary.BigEndian.Uint32(headerData[8:])
	if headerLen == 0 {
		return nil, nil, errors.New("pkg has no header")
	}
	buf := bytesPool.Get().(*proto.Buffer)
	buf.Reset()
	defer bytesPool.Put(buf)
	if d, ok := ctx.Deadline(); wait && ok {
		_ = conn.SetDeadline(d)
		defer conn.SetDeadline(time.Time{})
	}
	data := buf.Bytes()
	if cap(data) < int(bodyLen) {
		data = make([]byte, 0, bodyLen)
		buf.SetBuf(data)
	}
	data = data[:bodyLen]
	_, err = read(conn, data, int(bodyLen))
	if err != nil {
		return nil, nil, newError(err, "read body error")
	}

	// unmarshal
	header = &RpcHeader{}
	err = proto.Unmarshal(data[:headerLen], header)
	if err != nil {
		return nil, nil, newError(err, "unmarshal header error")
	}
	msg, err = newMsg(header)
	if err != nil {
		return nil, nil, err
	}
	data = data[headerLen:]
	if len(data) == 0 {
		return header, msg, nil
	}
	err = proto.Unmarshal(data, msg)
	if err != nil {
		return header, nil, newError(err, "unmarshal msg error")
	}
	return header, msg, nil
}

func read(conn net.Conn, data []byte, size int) (int, error) {
	total := 0
	for {
		n, err := conn.Read(data[total:size])
		if err != nil {
			return total + n, err
		}
		total += n
		if total >= size {
			break
		}
	}
	return total, nil
}

type RpcMessage struct {
	Header *RpcHeader
	Msg    proto.Message
}

type MessageReceiver struct {
	Seq      int32
	requests sync.Map // int32 => proto.Message
}

func (r *MessageReceiver) PrepareRequest() (int32, <-chan *RpcMessage) {
	s := atomic.AddInt32(&r.Seq, 1)
	ch := make(chan *RpcMessage)
	r.requests.Store(s, ch)
	return s, ch
}

func (r *MessageReceiver) SendResponse(seq int32, msg *RpcMessage) error {
	if ch, ok := r.requests.Load(seq); ok {
		select {
		case ch.(chan *RpcMessage) <- msg:
			return nil
		default:
			return errors.New("resp receiver don't receiving")
		}
	}
	return errors.New("resp has no receiver")
}

func (r *MessageReceiver) DeleteSeq(seq int32) {
	r.requests.Delete(seq)
}

type NetError struct {
	cause     error
	msg       string
	temporary bool
}

func (w *NetError) Error() string { return w.msg + ": " + w.cause.Error() }
func (w *NetError) Cause() error  { return w.cause }
func (w *NetError) Temporary() bool {
	return w.temporary
}

func newError(err error, message string) error {
	if err == nil {
		return nil
	}
	e := &NetError{
		cause:     err,
		msg:       message,
		temporary: true,
	}
	if err, ok := err.(Temporary); ok {
		e.temporary = err.Temporary()
	}
	return e
}

func IsTemporary(err error) bool {
	if e, ok := err.(Temporary); ok {
		return e.Temporary()
	}
	return true
}

func IsClose(err error) bool {
	if !IsTemporary(err) {
		return true
	}
	if e, ok := err.(cause); ok {
		if e.Cause() == io.EOF {
			return true
		}
	}
	return false
}

func NewResponseHeader(header *RpcHeader) *RpcHeader {
	return &RpcHeader{
		Method: "resp_" + header.Method,
		Seq:    header.Seq,
	}
}

func CalcDelay(tempDelay time.Duration) time.Duration {
	if tempDelay == 0 {
		tempDelay = 20 * time.Millisecond
	} else {
		tempDelay *= 2
	}
	if max := 1 * time.Second; tempDelay > max {
		tempDelay = max
	}
	return tempDelay
}

func Sleep(tempDelay time.Duration, cancel <-chan struct{}) {
	timer := time.NewTimer(tempDelay)
	select {
	case <-timer.C:
	case <-cancel:
		timer.Stop()
	}
}

func init() {
	binary.BigEndian.PutUint32(beginFlag, RpcBeginFlag)
}
