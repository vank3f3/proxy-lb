package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"

	"github.com/vank3f3/proxy-lb/internal/config"
	"github.com/vank3f3/proxy-lb/internal/healthcheck"
	"github.com/vank3f3/proxy-lb/internal/loadbalancer"
)

// BackendStats 存储后端的请求统计信息
type BackendStats struct {
	Name           string    `json:"name"`
	URL            string    `json:"url"`
	Healthy        bool      `json:"healthy"`
	LastCheck      time.Time `json:"last_check"`
	SuccessCount   int64     `json:"success_count"`
	FailureCount   int64     `json:"failure_count"`
	LastStatusCode int       `json:"last_status_code"`
	LastError      string    `json:"last_error,omitempty"`
}

// ReverseProxy 实现反向代理功能
type ReverseProxy struct {
	loadBalancer  *loadbalancer.LoadBalancer
	healthChecker *healthcheck.HealthChecker
	config        *config.Config
	stats         map[string]*BackendStats
	statsMutex    sync.RWMutex
}

// NewReverseProxy 创建一个新的反向代理
func NewReverseProxy(cfg *config.Config, lb *loadbalancer.LoadBalancer, hc *healthcheck.HealthChecker) *ReverseProxy {
	// 初始化统计信息
	stats := make(map[string]*BackendStats)
	for _, backend := range cfg.Backends {
		stats[backend.Name] = &BackendStats{
			Name: backend.Name,
			URL:  backend.URL,
		}
	}

	return &ReverseProxy{
		loadBalancer:  lb,
		healthChecker: hc,
		config:        cfg,
		stats:         stats,
	}
}

// ServeHTTP 处理HTTP请求
func (p *ReverseProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 处理状态监控路由
	if r.URL.Path == "/proxy/status" {
		p.handleStatusRequest(w, r)
		return
	}

	// 尝试处理请求，支持失败重试
	p.handleRequestWithRetry(w, r, 0)
}

// handleRequestWithRetry 处理请求并支持失败重试
func (p *ReverseProxy) handleRequestWithRetry(w http.ResponseWriter, r *http.Request, retryCount int) {
	// 最大重试次数，避免无限循环
	const maxRetries = 3
	if retryCount >= maxRetries {
		http.Error(w, "所有后端服务均不可用", http.StatusServiceUnavailable)
		return
	}

	// 获取下一个后端
	backend := p.loadBalancer.NextBackend(r)
	if backend == nil {
		http.Error(w, "无可用的后端服务", http.StatusServiceUnavailable)
		return
	}

	// 解析后端URL
	targetURL, err := url.Parse(backend.URL)
	if err != nil {
		http.Error(w, fmt.Sprintf("无效的后端URL: %v", err), http.StatusInternalServerError)
		return
	}

	// 创建请求体的副本，因为请求体只能读取一次
	var requestBodyCopy []byte
	if r.Body != nil {
		requestBodyCopy, err = io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("读取请求体失败: %v", err), http.StatusInternalServerError)
			return
		}
		r.Body.Close()
		// 重新设置请求体
		r.Body = io.NopCloser(bytes.NewBuffer(requestBodyCopy))
	}

	// 创建反向代理
	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	// 自定义错误处理
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		fmt.Printf("代理请求错误: %v\n", err)

		// 更新统计信息
		p.statsMutex.Lock()
		if stats, ok := p.stats[backend.Name]; ok {
			stats.FailureCount++
			stats.LastError = err.Error()
		}
		p.statsMutex.Unlock()

		// 增加失败计数并检查是否需要标记为不健康
		statusChanged := p.healthChecker.IncrementFailureCount(backend.Name, err.Error())
		if statusChanged {
			// 如果状态变更，更新负载均衡器的后端列表
			healthyBackends := p.healthChecker.GetHealthyBackends()
			p.loadBalancer.UpdateBackends(healthyBackends)
		}

		// 如果有请求体，需要重新设置
		if requestBodyCopy != nil {
			r.Body = io.NopCloser(bytes.NewBuffer(requestBodyCopy))
		}

		// 重试请求到下一个后端
		p.handleRequestWithRetry(w, r, retryCount+1)
	}

	// 自定义Director函数
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = targetURL.Host
		req.Header.Set("X-Forwarded-Host", req.Host)
		req.Header.Set("X-Forwarded-Proto", "http")
		req.Header.Set("X-Forwarded-For", req.RemoteAddr)
	}

	// 自定义ModifyResponse函数
	proxy.ModifyResponse = func(resp *http.Response) error {
		// 添加自定义响应头
		resp.Header.Set("X-Proxy", "Dify-LB")

		// 更新统计信息
		p.statsMutex.Lock()
		if stats, ok := p.stats[backend.Name]; ok {
			// 始终更新状态码
			stats.LastStatusCode = resp.StatusCode

			if resp.StatusCode >= 200 && resp.StatusCode < 400 {
				stats.SuccessCount++

				// 只有当失败计数大于0时才重置
				if stats.FailureCount > 0 {
					stats.FailureCount = 0
					stats.LastError = ""
					p.statsMutex.Unlock()
					p.healthChecker.ResetFailureCount(backend.Name)
				} else {
					p.statsMutex.Unlock()
				}
			} else {
				stats.FailureCount++
				p.statsMutex.Unlock()

				// 对于非成功的状态码，增加失败计数
				reason := fmt.Sprintf("HTTP状态码: %d", resp.StatusCode)
				statusChanged := p.healthChecker.IncrementFailureCount(backend.Name, reason)
				if statusChanged {
					// 如果状态变更，更新负载均衡器的后端列表
					healthyBackends := p.healthChecker.GetHealthyBackends()
					p.loadBalancer.UpdateBackends(healthyBackends)

					// 如果状态码表示服务器错误，重试请求
					if resp.StatusCode >= 500 && retryCount < 3 {
						// 关闭当前响应
						resp.Body.Close()

						// 重新设置请求体
						if requestBodyCopy != nil {
							r.Body = io.NopCloser(bytes.NewBuffer(requestBodyCopy))
						}

						// 重试请求到下一个后端
						p.handleRequestWithRetry(w, r, retryCount+1)
						return fmt.Errorf("服务器错误，重试请求")
					}
				}
			}
		} else {
			p.statsMutex.Unlock()
		}

		return nil
	}

	// 记录请求开始时间
	startTime := time.Now()

	// 处理请求
	proxy.ServeHTTP(w, r)

	// 记录请求结束时间和处理时间
	duration := time.Since(startTime)
	fmt.Printf("请求 %s 被转发到 %s，处理时间: %v\n", r.URL.Path, backend.Name, duration)

	// 如果使用最少连接策略，减少连接计数
	p.loadBalancer.DecrementConn(backend.Name)
}

// handleStatusRequest 处理状态监控请求
func (p *ReverseProxy) handleStatusRequest(w http.ResponseWriter, r *http.Request) {
	// 获取健康检查状态
	healthStatus := p.healthChecker.GetBackendStatuses()

	// 更新统计信息中的健康状态
	p.statsMutex.Lock()
	for _, status := range healthStatus {
		if stats, ok := p.stats[status.Name]; ok {
			stats.Healthy = status.Healthy
			stats.LastCheck = status.LastCheck

			// 如果节点恢复健康，重置失败计数
			if status.Healthy && stats.FailureCount > 0 {
				stats.FailureCount = 0
				stats.LastError = ""
			}
		}
	}

	// 准备响应数据
	var statsSlice []*BackendStats
	for _, stat := range p.stats {
		statsSlice = append(statsSlice, stat)
	}
	p.statsMutex.Unlock()

	// 设置响应头
	w.Header().Set("Content-Type", "application/json")

	// 输出JSON
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"timestamp": time.Now(),
		"backends":  statsSlice,
	}); err != nil {
		http.Error(w, fmt.Sprintf("生成状态信息失败: %v", err), http.StatusInternalServerError)
	}
}

// Start 启动反向代理服务器
func (p *ReverseProxy) Start() error {
	// 设置HTTP服务器
	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", p.config.Server.Port),
		Handler:      p,
		ReadTimeout:  p.config.Server.ReadTimeout,
		WriteTimeout: p.config.Server.WriteTimeout,
	}

	// 启动健康检查
	go p.healthChecker.Start()

	// 监听健康状态变更
	go p.handleHealthStatusChanges()

	fmt.Printf("反向代理服务器启动在端口 %d\n", p.config.Server.Port)
	fmt.Println("状态监控页面可访问: http://localhost:" + fmt.Sprintf("%d", p.config.Server.Port) + "/proxy/status")
	return server.ListenAndServe()
}

// handleHealthStatusChanges 处理健康状态变更
func (p *ReverseProxy) handleHealthStatusChanges() {
	for change := range p.healthChecker.GetStatusChangeChan() {
		// 获取当前健康的后端列表
		healthyBackends := p.healthChecker.GetHealthyBackends()
		// 更新负载均衡器的后端列表
		p.loadBalancer.UpdateBackends(healthyBackends)

		if change.Healthy {
			fmt.Printf("后端 %s 已恢复健康，当前健康后端数量: %d\n", change.Backend.Name, len(healthyBackends))

			// 当节点恢复健康时，重置其失败计数
			p.statsMutex.Lock()
			if stats, ok := p.stats[change.Backend.Name]; ok {
				stats.FailureCount = 0
				stats.LastError = ""
			}
			p.statsMutex.Unlock()
		} else {
			fmt.Printf("后端 %s 已标记为不健康，当前健康后端数量: %d\n", change.Backend.Name, len(healthyBackends))
		}
	}
}
