package main

import (
	"flag"
	"fmt"
	"github.com/cascax/http_portal/core"
	"github.com/cascax/http_portal/portal"
	"github.com/cascax/http_portal/ptlog"
)

var log *ptlog.ZapLogger

type startFlag struct {
	Config  string
	Verbose bool
}

func main() {
	f := &startFlag{}
	flag.StringVar(&f.Config, "c", defaultConfigPath(), "set configuration `file`")
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

	handler := &RequestHandler{
		hostRewrite: make(map[string]string),
		httpTimeout: config.Agent.Timeout.HTTPRead,
	}
	for k, v := range config.Agent.HostRewrite {
		handler.hostRewrite[k] = v
	}
	socketHandler := &WebsocketHandler{
		hostRewrite: handler.hostRewrite,
		httpTimeout: handler.httpTimeout,
	}
	s := portal.NewLocalPortal(config.Agent.Name, handler, config.Agent.RemoteAddr)
	s.SetTimeout(config.Agent.Timeout)
	s.SetWebsocketHandler(socketHandler)

	// catch exit signal
	core.CatchExitSignal(s)

	s.Run()
	log.Info("portal agent exit")
}
