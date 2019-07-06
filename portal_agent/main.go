package main

import (
	"flag"
	"fmt"
	"github.com/cascax/http_portal/portal"
	"github.com/cascax/http_portal/ptlog"
	"go.uber.org/zap"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"time"
)

var log *ptlog.ZapLogger

type RequestHandler struct {
	hostRewrite map[string]string
}

func (h *RequestHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Infof("%s %s", r.Method, r.URL.String())
	newUrl := h.getUrl(*r.URL)
	req, err := http.NewRequest(r.Method, newUrl, r.Body)
	if err != nil {
		log.Error("make request error, ", err)
		w.WriteHeader(500)
		w.Write([]byte(err.Error()))
		return
	}
	defer req.Body.Close()
	for k, arr := range r.Header {
		for _, v := range arr {
			req.Header.Add(k, v)
		}
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Error("do request error, ", err)
		w.WriteHeader(500)
		w.Write([]byte(err.Error()))
		return
	}
	w.WriteHeader(resp.StatusCode)
	for k, arr := range resp.Header {
		for _, v := range arr {
			w.Header().Add(k, v)
		}
	}
	writer := w.(*portal.HttpResponseWriter)
	_, err = writer.Buf.ReadFrom(resp.Body)
	if err != nil {
		log.Error("write response error, ", err)
	}
	log.Debugf("write resp body len:%d", writer.Buf.Len())
}

func (h *RequestHandler) getUrl(url url.URL) string {
	if v, ok := h.hostRewrite[url.Host]; ok {
		url.Host = v
	}
	return url.String()
}

func defaultConfig() string {
	configPath, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		panic("can't get config path, " + err.Error())
	}
	return path.Join(configPath, DefaultConfigName)
}

func init() {
	log = ptlog.NewConsoleLog(zap.AddCallerSkip(1))
	portal.SetLogger(&logger{log.Logger.Sugar()})
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
	}
	for k, v := range config.Agent.HostRewrite {
		handler.hostRewrite[k] = v
	}
	portal.NewLocalPortal(config.Agent.Name, handler, config.Agent.RemoteAddr).Run()
}

type logger struct {
	sugar *zap.SugaredLogger
}

func (l *logger) Printf(template string, args ...interface{}) {
	l.sugar.Infof(template, args...)
}

func (l *logger) Errorf(template string, args ...interface{}) {
	l.sugar.Errorf(template, args...)
}

func (l *logger) Debugf(template string, args ...interface{}) {
	l.sugar.Debugf(template, args...)
}

type startFlag struct {
	Config  string
	Verbose bool
}
