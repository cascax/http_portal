package ptlog

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"path"
	"strings"
)

type LogConfig struct {
	Path      string
	Name      string
	MaxSize   int `yaml:"max_size"`
	MaxBackup int `yaml:"max_backup"`
	Level     string
}

func (c *LogConfig) Filename() string {
	return path.Join(c.Path, c.Name)
}

func (c *LogConfig) ZapLevel() zapcore.Level {
	l, _ := ParseLevel(c.Level)
	return l
}

func ParseLevel(s string) (zapcore.Level, bool) {
	switch strings.ToLower(s) {
	case "debug":
		return zap.DebugLevel, true
	case "info":
		return zap.InfoLevel, true
	case "warn":
		return zap.WarnLevel, true
	case "error":
		return zap.ErrorLevel, true
	case "panic":
		return zap.PanicLevel, true
	case "fatal":
		return zap.FatalLevel, true
	}
	return zap.WarnLevel, false
}
