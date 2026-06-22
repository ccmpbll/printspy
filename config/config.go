package config

import (
	"fmt"
	"os"

	"github.com/ccmpbll/printspy/models"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Server struct {
		Port    int    `yaml:"port"`
		DataDir string `yaml:"data_dir"`
	} `yaml:"server"`
	Printers []models.PrinterConfig `yaml:"printers"`
}

func Load(path string) (*Config, error) {
	cfg := &Config{}
	cfg.Server.Port = 8080
	cfg.Server.DataDir = "/data"

	if path == "" {
		path = os.Getenv("PRINTSPY_CONFIG")
	}
	if path == "" {
		for _, p := range []string{"config.yaml", "/etc/printspy/config.yaml"} {
			if _, err := os.Stat(p); err == nil {
				path = p
				break
			}
		}
	}

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return cfg, err
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return cfg, err
		}
	}

	if envPort := os.Getenv("PRINTSPY_PORT"); envPort != "" {
		var port int
		if _, err := fmt.Sscanf(envPort, "%d", &port); err == nil && port > 0 {
			cfg.Server.Port = port
		}
	}

	if envDataDir := os.Getenv("PRINTSPY_DATA_DIR"); envDataDir != "" {
		cfg.Server.DataDir = envDataDir
	}

	return cfg, nil
}
