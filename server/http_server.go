package main

import (
	"github.com/mcxr4299/http_portal/portalcore"
	"go.uber.org/zap"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"
)

const (
	MaxPortalDeep = 5
)

type httpHandler struct {
	proxyServer *ProxyServer
}

func (f httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	//w.Write([]byte(fmt.Sprintf("%+v", r)))
	//w.Write([]byte("\n\n"))
	//w.Write([]byte(fmt.Sprintf("%+v", *r.URL)))
	////w.Write([]byte(fmt.Sprintf("%+v", *r.URL.User)))
	//w.Write([]byte("\n\n"))
	//w.Write([]byte(fmt.Sprintf("%+v", r.RequestURI)))
	log.Infof("http %s %s", r.Method, r.RequestURI)

	req := &portalcore.HttpRequest{
		Method:     r.Method,
		Url:        r.URL.String(),
		ReqProto:   r.Proto,
		Host:       r.Host,
		RemoteAddr: r.RemoteAddr,
	}
	if r.Body != nil {
		// TODO: 处理不了太大的请求
		data, err := ioutil.ReadAll(r.Body)
		if err != nil {
			log.Error("get body error", zap.Error(err), zap.String("remote", r.RemoteAddr))
			_, _ = w.Write([]byte("body error"))
			return
		}
		req.Body = data
	}
	withDeep := false
	// check recursive
	if v := r.Header.Get(portalcore.PortalHeaderDeep); len(v) > 0 {
		// 处理递归调用 在HTTP头中带个独有的深度信息
		if deep, err := strconv.Atoi(v); err == nil {
			if deep > MaxPortalDeep || r.Header.Get(portalcore.PortalHeaderHost) == r.Host {
				// portal中次数超过深度 or 上一次也是从当前host传送的，也就是说下一次解析这个host还会再从这走
				w.WriteHeader(500)
				log.Errorf("http %s %s, over max portal deep", r.Method, r.RequestURI)
				w.Write([]byte("over max portal deep"))
				return
			}
			req.Header = append(req.Header, &portalcore.HttpRequest_Header{
				Key:   portalcore.PortalHeaderDeep,
				Value: []string{strconv.Itoa(deep + 1)},
			})
			withDeep = true
		}
	}
	for k, arr := range r.Header {
		if strings.HasPrefix(k, portalcore.PortalHeaderPrefix) {
			continue
		}
		h := &portalcore.HttpRequest_Header{Key: k}
		for _, v := range arr {
			h.Value = append(h.Value, v)
		}
		req.Header = append(req.Header, h)
	}
	if !withDeep {
		req.Header = append(req.Header, &portalcore.HttpRequest_Header{
			Key:   portalcore.PortalHeaderDeep,
			Value: []string{"1"},
		})
	}
	req.Header = append(req.Header, &portalcore.HttpRequest_Header{
		Key:   portalcore.PortalHeaderHost,
		Value: []string{r.Host},
	})

	resp, err := f.proxyServer.DoRequest(req)
	if err != nil {
		if resp != nil {
			w.WriteHeader(int(resp.Status))
		}
		log.Errorf("%d for http %s %s, %s", resp.Status, r.Method, r.RequestURI, err)
		w.Write([]byte(err.Error()))
		return
	}
	header := w.Header()
	for _, h := range resp.Header {
		for _, v := range h.Value {
			header.Add(h.Key, v)
		}
	}
	w.WriteHeader(int(resp.Status))
	w.Write(resp.Body)
}

func runHttpServer(server *ProxyServer, host string) {
	handler := httpHandler{proxyServer: server}
	log.Info("start http server ", host)
	err := http.ListenAndServe(host, handler)
	if err != nil {
		if err != http.ErrServerClosed {
			log.Error("http server closed, ", err)
		} else {
			log.Info("http server closed")
		}
	}
}
