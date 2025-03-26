package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/vank3f3/proxy-lb/internal/config"
	"github.com/vank3f3/proxy-lb/internal/healthcheck"
	"github.com/vank3f3/proxy-lb/internal/loadbalancer"
	"github.com/vank3f3/proxy-lb/internal/proxy"
)

func main() {
	// 解析命令行参数
	configPath := flag.String("config", "configs/config.yaml", "配置文件路径")
	flag.Parse()

	// 加载配置
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		fmt.Printf("加载配置失败: %v\n", err)
		os.Exit(1)
	}

	// 创建健康检查器
	healthChecker := healthcheck.NewHealthChecker(cfg.HealthCheck, cfg.Backends)

	// 创建负载均衡器
	loadBalancer := loadbalancer.NewLoadBalancer(cfg.LoadBalancing.Strategy, cfg.Backends)

	// 创建反向代理
	reverseProxy := proxy.NewReverseProxy(cfg, loadBalancer, healthChecker)

	// 处理信号
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// 启动反向代理服务器
	go func() {
		if err := reverseProxy.Start(); err != nil {
			fmt.Printf("启动反向代理服务器失败: %v\n", err)
			os.Exit(1)
		}
	}()

	// 等待信号
	sig := <-sigCh
	fmt.Printf("接收到信号 %v，正在关闭服务...\n", sig)

	// 停止健康检查
	healthChecker.Stop()

	fmt.Println("服务已关闭")
}
