package main

import (
	"flag"
	"fmt"
	"github.com/mcxr4299/http_portal/portallog"
	"go.uber.org/zap"
	"os"
	"path"
	"path/filepath"
)

var (
	log    *portallog.ZapLogger
	logger *zap.Logger
)

type startFlag struct {
	Config  string
	Verbose bool
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
	fmt.Printf("start on param %+v\n", f)

	config, err := ReadConfig(f.Config)
	if err != nil {
		panic(err)
	}

	if f.Verbose {
		log = portallog.NewConsoleLog()
	} else {
		log, err = portallog.NewLog(config.Log)
		if err != nil {
			panic(err)
		}
	}
	logger = log.Logger

	log.Infof("start on param %+v", f)
	log.Infof("config: %+v", config)
	proxyServer := NewProxyServer(config.ProxyServer.GetHost())
	proxyServer.SetHosts(config.ProxyServer.Portal)
	err = proxyServer.Start()
	if err != nil {
		log.Panic(err)
	}
	runHttpServer(proxyServer, config.HttpServer.GetHost())
}
