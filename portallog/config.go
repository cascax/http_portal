package portallog

import "path"

type LogConfig struct {
	Path      string
	Name      string
	MaxSize   int `yaml:"max_size"`
	MaxBackup int `yaml:"max_backup"`
}

func (c *LogConfig) Filename() string {
	return path.Join(c.Path, c.Name)
}
