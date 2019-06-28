package main

import (
	"fmt"
	"github.com/cascax/http_portal/ptlog"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v2"
	"io/ioutil"
)

const (
	DefaultConfigName = "portal_server.yml"
)

type ServerConfig struct {
	HttpServer  ProxyConfig `yaml:"http_server"`
	ProxyServer HttpConfig  `yaml:"proxy_server"`
	Log         ptlog.LogConfig
}

type ProxyConfig struct {
	Listen string
	Port   int
}

type HttpConfig struct {
	Listen string
	Port   int
	Portal map[string][]string // name => hosts
}

func (c *ProxyConfig) GetHost() string {
	return fmt.Sprintf("%s:%d", c.Listen, c.Port)
}

func (c *HttpConfig) GetHost() string {
	return fmt.Sprintf("%s:%d", c.Listen, c.Port)
}

func ReadConfig(filename string) (*ServerConfig, error) {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, errors.WithMessage(err, "read config error")
	}
	config := &ServerConfig{
		HttpServer: ProxyConfig{
			Listen: "127.0.0.1",
			Port:   80,
		},
		ProxyServer: HttpConfig{
			Listen: "127.0.0.1",
			Port:   10625,
			Portal: make(map[string][]string),
		},
		Log: ptlog.LogConfig{
			Path:      ".",
			Name:      "server.log",
			MaxSize:   20,
			MaxBackup: 10,
		},
	}
	err = yaml.Unmarshal(data, config)
	if err != nil {
		return nil, errors.WithMessage(err, "unmarshal yaml error")
	}
	return config, nil
}
