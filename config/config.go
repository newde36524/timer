package config

import (
	"fmt"

	"github.com/spf13/viper"
)

// Config 应用配置
type Config struct {
	Server   ServerConfig   `mapstructure:"server"`
	Database DatabaseConfig `mapstructure:"database"`
	Redis    RedisConfig    `mapstructure:"redis"`
}

// ServerConfig 服务器配置
type ServerConfig struct {
	Port     int    `mapstructure:"port"`
	Mode     string `mapstructure:"mode"`     // debug, release, test
	Timezone string `mapstructure:"timezone"` // 时区设置
}

// DatabaseConfig 数据库配置
type DatabaseConfig struct {
	Path     string `mapstructure:"path"`
	LogLevel string `mapstructure:"log_level"`
}

// RedisConfig Redis 配置
type RedisConfig struct {
	Enabled  bool   `mapstructure:"enabled"`
	Addr     string `mapstructure:"addr"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

// Load 加载配置
func Load(configPath string) (*Config, error) {
	v := viper.New()

	// 设置默认值
	setDefaults(v)

	// 读取配置文件
	if configPath != "" {
		v.SetConfigFile(configPath)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
		v.AddConfigPath("./config")
	}

	// 环境变量
	v.AutomaticEnv()

	// 读取配置文件
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
		// 配置文件不存在，使用默认值
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	return &cfg, nil
}

// setDefaults 设置默认值
func setDefaults(v *viper.Viper) {
	// 服务器配置
	v.SetDefault("server.port", 8080)
	v.SetDefault("server.mode", "release")
	v.SetDefault("server.timezone", "Asia/Shanghai")

	// 数据库配置
	v.SetDefault("database.path", "/data/timer.db")
	v.SetDefault("database.log_level", "warn")

	// Redis 配置
	v.SetDefault("redis.enabled", true)
	v.SetDefault("redis.addr", "127.0.0.1:6379")
	v.SetDefault("redis.password", "")
	v.SetDefault("redis.db", 0)
}
