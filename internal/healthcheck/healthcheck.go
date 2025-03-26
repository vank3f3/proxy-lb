package healthcheck

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/vank3f3/proxy-lb/internal/config"
)

// BackendStatus 表示后端服务的健康状态
type BackendStatus struct {
	Backend           config.BackendConfig
	Healthy           bool
	LastCheck         time.Time
	ConsecutiveFails  int
	ConsecutivePasses int
	mu                sync.RWMutex
}

// HealthChecker 负责检查后端服务的健康状态
type HealthChecker struct {
	backends       []*BackendStatus
	config         config.HealthCheckConfig
	client         *http.Client
	stopCh         chan struct{}
	statusChangeCh chan BackendStatusChange
	mu             sync.RWMutex
}

// BackendStatusChange 表示后端状态变更事件
type BackendStatusChange struct {
	Backend config.BackendConfig
	Healthy bool
}

// BackendStatusInfo 包含后端状态信息
type BackendStatusInfo struct {
	Name         string    `json:"name"`
	URL          string    `json:"url"`
	Healthy      bool      `json:"healthy"`
	LastCheck    time.Time `json:"last_check"`
	FailCount    int       `json:"fail_count"`
	SuccessCount int       `json:"success_count"`
}

// NewHealthChecker 创建一个新的健康检查器
func NewHealthChecker(cfg config.HealthCheckConfig, backends []config.BackendConfig) *HealthChecker {
	backendStatuses := make([]*BackendStatus, len(backends))
	for i, backend := range backends {
		backendStatuses[i] = &BackendStatus{
			Backend: backend,
			Healthy: true, // 初始假设所有后端都是健康的
		}
	}

	client := &http.Client{
		Timeout: cfg.Timeout,
	}

	return &HealthChecker{
		backends:       backendStatuses,
		config:         cfg,
		client:         client,
		stopCh:         make(chan struct{}),
		statusChangeCh: make(chan BackendStatusChange, 10),
	}
}

// Start 开始健康检查
func (hc *HealthChecker) Start() {
	ticker := time.NewTicker(hc.config.Interval)
	defer ticker.Stop()

	// 立即进行一次健康检查
	hc.checkAll()

	for {
		select {
		case <-ticker.C:
			hc.checkAll()
		case <-hc.stopCh:
			return
		}
	}
}

// Stop 停止健康检查
func (hc *HealthChecker) Stop() {
	close(hc.stopCh)
}

// GetStatusChangeChan 返回状态变更通道
func (hc *HealthChecker) GetStatusChangeChan() <-chan BackendStatusChange {
	return hc.statusChangeCh
}

// GetHealthyBackends 返回所有健康的后端
func (hc *HealthChecker) GetHealthyBackends() []config.BackendConfig {
	hc.mu.RLock()
	defer hc.mu.RUnlock()

	var healthyBackends []config.BackendConfig
	for _, backend := range hc.backends {
		backend.mu.RLock()
		if backend.Healthy {
			healthyBackends = append(healthyBackends, backend.Backend)
		}
		backend.mu.RUnlock()
	}
	return healthyBackends
}

// GetBackendStatuses 返回所有后端的状态信息
func (hc *HealthChecker) GetBackendStatuses() []BackendStatusInfo {
	hc.mu.RLock()
	defer hc.mu.RUnlock()

	statuses := make([]BackendStatusInfo, len(hc.backends))
	for i, backend := range hc.backends {
		backend.mu.RLock()
		statuses[i] = BackendStatusInfo{
			Name:         backend.Backend.Name,
			URL:          backend.Backend.URL,
			Healthy:      backend.Healthy,
			LastCheck:    backend.LastCheck,
			FailCount:    backend.ConsecutiveFails,
			SuccessCount: backend.ConsecutivePasses,
		}
		backend.mu.RUnlock()
	}

	return statuses
}

// UpdateBackends 更新后端列表
func (hc *HealthChecker) UpdateBackends(backends []config.BackendConfig) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	// 创建一个映射以快速查找现有后端
	existingBackends := make(map[string]*BackendStatus)
	for _, backend := range hc.backends {
		existingBackends[backend.Backend.Name] = backend
	}

	// 创建新的后端列表
	newBackends := make([]*BackendStatus, len(backends))
	for i, backend := range backends {
		if existing, ok := existingBackends[backend.Name]; ok {
			// 保留现有后端的状态
			existing.Backend = backend // 更新配置
			newBackends[i] = existing
		} else {
			// 添加新后端
			newBackends[i] = &BackendStatus{
				Backend: backend,
				Healthy: true, // 初始假设新后端是健康的
			}
		}
	}

	hc.backends = newBackends
}

// checkAll 检查所有后端的健康状态
func (hc *HealthChecker) checkAll() {
	var wg sync.WaitGroup

	hc.mu.RLock()
	backends := hc.backends
	hc.mu.RUnlock()

	for _, backend := range backends {
		wg.Add(1)
		go func(b *BackendStatus) {
			defer wg.Done()
			hc.checkBackend(b)
		}(backend)
	}

	wg.Wait()
}

// checkBackend 检查单个后端的健康状态
func (hc *HealthChecker) checkBackend(backend *BackendStatus) {
	url := backend.Backend.URL + hc.config.Path

	ctx, cancel := context.WithTimeout(context.Background(), hc.config.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		hc.markUnhealthy(backend, fmt.Sprintf("创建请求失败: %v", err))
		return
	}

	resp, err := hc.client.Do(req)
	if err != nil {
		hc.markUnhealthy(backend, fmt.Sprintf("请求失败: %v", err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		hc.markUnhealthy(backend, fmt.Sprintf("非健康状态码: %d", resp.StatusCode))
		return
	}

	hc.markHealthy(backend)
}

// markUnhealthy 标记后端为不健康
func (hc *HealthChecker) markUnhealthy(backend *BackendStatus, reason string) {
	backend.mu.Lock()
	defer backend.mu.Unlock()

	backend.LastCheck = time.Now()
	backend.ConsecutiveFails++
	backend.ConsecutivePasses = 0

	// 如果连续失败次数达到阈值，且当前状态为健康，则标记为不健康
	if backend.ConsecutiveFails >= hc.config.UnhealthyThreshold && backend.Healthy {
		backend.Healthy = false
		fmt.Printf("后端 %s 标记为不健康: %s\n", backend.Backend.Name, reason)

		// 通知状态变更
		select {
		case hc.statusChangeCh <- BackendStatusChange{Backend: backend.Backend, Healthy: false}:
		default:
			fmt.Println("状态变更通道已满，无法发送不健康通知")
		}
	}
}

// markHealthy 标记后端为健康
func (hc *HealthChecker) markHealthy(backend *BackendStatus) {
	backend.mu.Lock()
	defer backend.mu.Unlock()

	backend.LastCheck = time.Now()
	backend.ConsecutivePasses++
	backend.ConsecutiveFails = 0 // 重置失败计数

	// 如果连续成功次数达到阈值，且当前状态为不健康，则标记为健康
	if backend.ConsecutivePasses >= hc.config.HealthyThreshold && !backend.Healthy {
		backend.Healthy = true
		fmt.Printf("后端 %s 恢复健康\n", backend.Backend.Name)

		// 通知状态变更
		select {
		case hc.statusChangeCh <- BackendStatusChange{Backend: backend.Backend, Healthy: true}:
		default:
			fmt.Println("状态变更通道已满，无法发送健康恢复通知")
		}
	}
}

// MarkBackendUnhealthy 直接标记后端为不健康（由请求失败触发）
func (hc *HealthChecker) MarkBackendUnhealthy(backendName string, reason string) bool {
	hc.mu.RLock()
	defer hc.mu.RUnlock()

	for _, backend := range hc.backends {
		if backend.Backend.Name == backendName {
			backend.mu.Lock()
			wasHealthy := backend.Healthy
			backend.Healthy = false
			backend.ConsecutiveFails++
			backend.ConsecutivePasses = 0
			backend.LastCheck = time.Now()
			backend.mu.Unlock()

			if wasHealthy {
				fmt.Printf("后端 %s 因请求失败直接标记为不健康: %s\n", backendName, reason)

				// 通知状态变更
				select {
				case hc.statusChangeCh <- BackendStatusChange{Backend: backend.Backend, Healthy: false}:
				default:
					fmt.Println("状态变更通道已满，无法发送不健康通知")
				}
				return true
			}
			return false
		}
	}
	return false
}

// ResetFailureCount 重置后端的失败计数
func (hc *HealthChecker) ResetFailureCount(backendName string) {
	hc.mu.RLock()
	defer hc.mu.RUnlock()

	for _, backend := range hc.backends {
		if backend.Backend.Name == backendName {
			backend.mu.Lock()
			// 只有当失败计数大于0时才输出日志
			if backend.ConsecutiveFails > 0 {
				backend.ConsecutiveFails = 0
				fmt.Printf("后端 %s 的失败计数已重置\n", backendName)
			} else {
				backend.ConsecutiveFails = 0
			}
			backend.mu.Unlock()
			return
		}
	}
}

// IncrementFailureCount 增加后端的失败计数并检查是否需要标记为不健康
func (hc *HealthChecker) IncrementFailureCount(backendName string, reason string) bool {
	hc.mu.RLock()
	defer hc.mu.RUnlock()

	for _, backend := range hc.backends {
		if backend.Backend.Name == backendName {
			backend.mu.Lock()
			backend.ConsecutiveFails++
			failCount := backend.ConsecutiveFails
			wasHealthy := backend.Healthy

			// 如果失败计数达到阈值，标记为不健康
			if failCount >= 3 && wasHealthy {
				backend.Healthy = false
				backend.mu.Unlock()

				fmt.Printf("后端 %s 因连续失败次数(%d)达到阈值直接标记为不健康: %s\n",
					backendName, failCount, reason)

				// 通知状态变更
				select {
				case hc.statusChangeCh <- BackendStatusChange{Backend: backend.Backend, Healthy: false}:
				default:
					fmt.Println("状态变更通道已满，无法发送不健康通知")
				}
				return true
			} else {
				backend.mu.Unlock()
				return false
			}
		}
	}
	return false
}
