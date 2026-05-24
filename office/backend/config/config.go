package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server  ServerConfig  `yaml:"server"`
	Auth    AuthConfig    `yaml:"auth"`
	Storage StorageConfig `yaml:"storage"`
}

type ServerConfig struct {
	Addr       string `yaml:"addr"`
	DataDir    string `yaml:"data_dir"`
	UploadsDir string `yaml:"uploads_dir"`
}

type AuthConfig struct {
	Enabled        bool   `yaml:"enabled"`
	Password       string `yaml:"password"`
	MaxAttempts    int    `yaml:"max_attempts"`
	LockoutMinutes int    `yaml:"lockout_minutes"`
	SessionHours   int    `yaml:"session_hours"`
}

type StorageConfig struct {
	Type     string         `yaml:"type"`
	Postgres PostgresConfig `yaml:"postgres"`
}

type PostgresConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	Database string `yaml:"database"`
	SSLMode  string `yaml:"sslmode"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Default(), nil
	}
	cfg := Default()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func Default() *Config {
	return &Config{
		Server: ServerConfig{
			Addr:       ":8080",
			DataDir:    "./data",
			UploadsDir: "./uploads",
		},
		Auth: AuthConfig{
			Enabled:        false,
			Password:       "",
			MaxAttempts:    5,
			LockoutMinutes: 15,
			SessionHours:   24,
		},
		Storage: StorageConfig{
			Type: "local",
			Postgres: PostgresConfig{
				Host:    "localhost",
				Port:    5432,
				SSLMode: "disable",
			},
		},
	}
}
