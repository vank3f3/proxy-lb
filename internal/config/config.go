package config

import (
	"fmt"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
)

// Config 包含应用程序的所有配置
type Config struct {
	Server        ServerConfig        `mapstructure:"server"`
	HealthCheck   HealthCheckConfig   `mapstructure:"health_check"`
	LoadBalancing LoadBalancingConfig `mapstructure:"load_balancing"`
	Backends      []BackendConfig     `mapstructure:"backends"`
}

// ServerConfig 包含服务器相关配置
type ServerConfig struct {
	Port         int           `mapstructure:"port"`
	ReadTimeout  time.Duration `mapstructure:"read_timeout"`
	WriteTimeout time.Duration `mapstructure:"write_timeout"`
	IdleTimeout  time.Duration `mapstructure:"idle_timeout"`
}

// HealthCheckConfig 包含健康检查相关配置
type HealthCheckConfig struct {
	Path               string        `mapstructure:"path"`
	Interval           time.Duration `mapstructure:"interval"`
	Timeout            time.Duration `mapstructure:"timeout"`
	UnhealthyThreshold int           `mapstructure:"unhealthy_threshold"`
	HealthyThreshold   int           `mapstructure:"healthy_threshold"`
}

// LoadBalancingConfig 包含负载均衡策略配置
type LoadBalancingConfig struct {
	Strategy string `mapstructure:"strategy"`
}

// BackendConfig 包含后端服务配置
type BackendConfig struct {
	Name   string `mapstructure:"name"`
	URL    string `mapstructure:"url"`
	Weight int    `mapstructure:"weight"`
}

// LoadConfig 从指定路径加载配置文件
func LoadConfig(configPath string) (*Config, error) {
	v := viper.New()
	v.SetConfigFile(configPath)

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	var config Config
	if err := v.Unmarshal(&config); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	// 设置配置文件变更监听
	v.WatchConfig()
	v.OnConfigChange(func(e fsnotify.Event) {
		fmt.Printf("配置文件已更改: %s\n", e.Name)
		if err := v.Unmarshal(&config); err != nil {
			fmt.Printf("重新加载配置失败: %v\n", err)
		}
	})

	return &config, nil
}
