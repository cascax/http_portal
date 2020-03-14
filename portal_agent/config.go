package main

import (
	"github.com/cascax/http_portal/portal"
	"github.com/cascax/http_portal/ptlog"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"time"
)

const (
	DefaultConfigName = "portal_server.yml"
)

type ServerConfig struct {
	Agent AgentConfig `yaml:"portal_agent"`
	Log   ptlog.LogConfig
}

type AgentConfig struct {
	RemoteAddr  string               `yaml:"remote_addr"`
	Name        string               `yaml:"name"`
	HostRewrite map[string]string    `yaml:"host_rewrite"`
	Timeout     portal.TimeoutConfig `yaml:"timeout"`
}

func ReadConfig(filename string) (*ServerConfig, error) {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, errors.WithMessage(err, "read config error")
	}
	config := &ServerConfig{
		Agent: AgentConfig{
			HostRewrite: make(map[string]string),
			Timeout: portal.TimeoutConfig{
				ServerConnect: 5 * time.Second,
				ServerWrite:   5 * time.Second,
			},
		},
		Log: ptlog.LogConfig{
			Path:      ".",
			Name:      "agent.log",
			MaxSize:   20,
			MaxBackup: 10,
		},
	}
	err = yaml.Unmarshal(data, config)
	if err != nil {
		return nil, errors.WithMessage(err, "unmarshal yaml error")
	}
	if len(config.Agent.Name) == 0 {
		return nil, errors.New("agent.name not configured")
	}
	if len(config.Agent.RemoteAddr) == 0 {
		return nil, errors.New("agent.remote_addr not configured")
	}
	return config, nil
}

func defaultConfigPath() string {
	configPath, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		panic("can't get config path, " + err.Error())
	}
	return path.Join(configPath, DefaultConfigName)
}
