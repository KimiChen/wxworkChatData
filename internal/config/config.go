package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	MySQL            MySQLConfig  `yaml:"mysql"`
	Media            MediaConfig  `yaml:"media"`
	Proxy            ProxyConfig  `yaml:"proxy"`
	Log              LogConfig    `yaml:"log"`
	StorageFilewords string       `yaml:"storage_filewords"` // 兼容旧配置，当 storage_database/storage_file 未设置时作为回退
	StorageDatabase  string       `yaml:"storage_database"`  // 数据库表名前缀的时间格式，如 YYYY-MM-DD
	StorageFile      string       `yaml:"storage_file"`      // 媒体文件目录的时间格式，如 YYYY-MM-DD-hh
	DBPrefix         string       `yaml:"-"`                 // 由 StorageDatabase 生成的数据库表名前缀
	FilePrefix       string       `yaml:"-"`                 // 由 StorageFile 生成的媒体文件目录前缀
	Corps            []CorpConfig `yaml:"corps"`
}

type MySQLConfig struct {
	DSN          string `yaml:"dsn"`
	MaxOpenConns int    `yaml:"max_open_conns"`
	MaxIdleConns int    `yaml:"max_idle_conns"`
}

type MediaConfig struct {
	BasePath string `yaml:"base_path"`
}

type ProxyConfig struct {
	URL      string `yaml:"url"`
	Password string `yaml:"password"`
}

type LogConfig struct {
	Level string `yaml:"level"`
	File  string `yaml:"file"`
}

type CorpConfig struct {
	Name              string `yaml:"name"`
	CorpID            string `yaml:"corp_id"`
	CorpSecret        string `yaml:"corp_secret"`
	RSAPrivateKey     string `yaml:"rsa_private_key"`
	RSAPrivateKeyFile string `yaml:"rsa_private_key_file"`
	PollInterval      int    `yaml:"poll_interval"` // seconds
	BatchSize         int    `yaml:"batch_size"`
	SDKTimeout        int    `yaml:"sdk_timeout"` // seconds
}

// FormatStoragePrefix 将 YYYY-MM-DD-hh-mm-ss 格式转为 Go time 格式并用当前时间生成前缀
func FormatStoragePrefix(format string) string {
	// YYYY-MM-DD-hh-mm-ss → Go time layout
	r := strings.NewReplacer(
		"YYYY", "2006",
		"MM", "01",
		"DD", "02",
		"hh", "15",
		"mm", "04",
		"ss", "05",
	)
	goLayout := r.Replace(format)
	return time.Now().Format(goLayout)
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if len(cfg.Corps) == 0 {
		return nil, fmt.Errorf("no corps configured")
	}

	for i := range cfg.Corps {
		c := &cfg.Corps[i]

		// Load RSA private key from file if inline key is empty
		if c.RSAPrivateKey == "" && c.RSAPrivateKeyFile != "" {
			keyData, err := os.ReadFile(c.RSAPrivateKeyFile)
			if err != nil {
				return nil, fmt.Errorf("read rsa key file for corp %s: %w", c.Name, err)
			}
			c.RSAPrivateKey = string(keyData)
		}

		if c.Name == "" {
			return nil, fmt.Errorf("corps[%d]: name is required", i)
		}
		if c.CorpID == "" {
			return nil, fmt.Errorf("corps[%d] (%s): corp_id is required", i, c.Name)
		}
		if c.CorpSecret == "" {
			return nil, fmt.Errorf("corps[%d] (%s): corp_secret is required", i, c.Name)
		}
		if c.RSAPrivateKey == "" {
			return nil, fmt.Errorf("corps[%d] (%s): rsa_private_key or rsa_private_key_file is required", i, c.Name)
		}

		// Apply defaults
		if c.PollInterval <= 0 {
			c.PollInterval = 30
		}
		if c.BatchSize <= 0 || c.BatchSize > 1000 {
			c.BatchSize = 500
		}
		if c.SDKTimeout <= 0 {
			c.SDKTimeout = 10
		}
	}

	// 兼容旧配置：storage_database / storage_file 未设置时回退到 storage_filewords
	if cfg.StorageDatabase == "" {
		cfg.StorageDatabase = cfg.StorageFilewords
	}
	if cfg.StorageFile == "" {
		cfg.StorageFile = cfg.StorageFilewords
	}
	if cfg.StorageDatabase == "" {
		return nil, fmt.Errorf("storage_database is required (e.g. YYYY-MM-DD)")
	}
	if cfg.StorageFile == "" {
		return nil, fmt.Errorf("storage_file is required (e.g. YYYY-MM-DD-hh)")
	}
	cfg.DBPrefix = FormatStoragePrefix(cfg.StorageDatabase)
	cfg.FilePrefix = FormatStoragePrefix(cfg.StorageFile)

	// Apply MySQL defaults
	if cfg.MySQL.MaxOpenConns <= 0 {
		cfg.MySQL.MaxOpenConns = 20
	}
	if cfg.MySQL.MaxIdleConns <= 0 {
		cfg.MySQL.MaxIdleConns = 5
	}

	// Apply log default
	if cfg.Log.Level == "" {
		cfg.Log.Level = "info"
	}

	return &cfg, nil
}
