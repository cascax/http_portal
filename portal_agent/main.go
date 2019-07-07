package main

import (
	"flag"
	"fmt"
	"github.com/cascax/http_portal/core"
	"github.com/cascax/http_portal/portal"
	"github.com/cascax/http_portal/ptlog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

var log *ptlog.ZapLogger

type RequestHandler struct {
	hostRewrite map[string]string
	httpTimeout time.Duration
}

func (h *RequestHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	err := h.serveHTTP(w, r)
	if err != nil {
		log.Error("write response error, ", err)
	}
}

func (h *RequestHandler) serveHTTP(w http.ResponseWriter, r *http.Request) error {
	log.Infof("%s %s", r.Method, r.URL.String())
	newUrl := h.getUrl(*r.URL)
	req, err := http.NewRequest(r.Method, newUrl.String(), r.Body)
	if err != nil {
		log.Error("make request error, ", err)
		w.WriteHeader(500)
		_, err = w.Write([]byte(err.Error()))
		return err
	}
	defer req.Body.Close()
	for k, arr := range r.Header {
		for _, v := range arr {
			req.Header.Add(k, v)
		}
	}
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: h.httpTimeout,
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Error("do request error, ", err)
		w.WriteHeader(500)
		_, err = w.Write([]byte(err.Error()))
		return err
	}
	// replace redirect header
	if v := resp.Header.Get("Location"); len(v) > 0 {
		resp.Header.Set("Location", strings.Replace(v, newUrl.Host, r.Header.Get(core.PortalHeaderHost), 1))
	}

	for k, arr := range resp.Header {
		for _, v := range arr {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	log.Debugf("http response status:%d length:%d", resp.StatusCode, resp.ContentLength)

	writer := w.(*portal.HttpResponseWriter)
	_, err = writer.ReadFrom(resp.Body)
	return err
}

func (h *RequestHandler) getUrl(url url.URL) url.URL {
	if v, ok := h.hostRewrite[url.Host]; ok {
		url.Host = v
	}
	return url
}

func defaultConfig() string {
	configPath, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		panic("can't get config path, " + err.Error())
	}
	return path.Join(configPath, DefaultConfigName)
}

func main() {
	f := &startFlag{}
	flag.StringVar(&f.Config, "c", defaultConfig(), "set configuration `file`")
	flag.BoolVar(&f.Verbose, "v", false, "print log to console")
	flag.Parse()
	fmt.Printf("start on param %+v\n", f)

	config, err := ReadConfig(f.Config)
	if err != nil {
		panic(err)
	}

	if f.Verbose {
		log = ptlog.NewConsoleLog()
	} else {
		fmt.Println("log file:", config.Log.Filename())
		log, err = ptlog.NewLog(config.Log)
		if err != nil {
			panic(err)
		}
	}

	portal.SetLogger(log)
	log.Infof("start on param %+v", f)
	log.Infof("config: %+v", config)
	if len(config.Agent.Name) == 0 {
		panic("agent.name not configured")
	}
	if len(config.Agent.RemoteAddr) == 0 {
		panic("agent.remote_addr not configured")
	}

	handler := &RequestHandler{
		hostRewrite: make(map[string]string),
		httpTimeout: config.Agent.Timeout.HTTPRead,
	}
	for k, v := range config.Agent.HostRewrite {
		handler.hostRewrite[k] = v
	}
	s := portal.NewLocalPortal(config.Agent.Name, handler, config.Agent.RemoteAddr)
	s.SetTimeout(config.Agent.Timeout)
	s.Run()
}

type startFlag struct {
	Config  string
	Verbose bool
}
