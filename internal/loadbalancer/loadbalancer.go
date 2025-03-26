package loadbalancer

import (
	"hash/fnv"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/vank3f3/proxy-lb/internal/config"
)

// Strategy 定义负载均衡策略接口
type Strategy interface {
	NextBackend(req *http.Request) *config.BackendConfig
	UpdateBackends(backends []config.BackendConfig)
}

// RoundRobinStrategy 实现轮询策略
type RoundRobinStrategy struct {
	backends []config.BackendConfig
	current  uint64
	mu       sync.RWMutex
}

// NewRoundRobinStrategy 创建一个新的轮询策略
func NewRoundRobinStrategy(backends []config.BackendConfig) *RoundRobinStrategy {
	return &RoundRobinStrategy{
		backends: backends,
	}
}

// NextBackend 获取下一个后端
func (r *RoundRobinStrategy) NextBackend(req *http.Request) *config.BackendConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.backends) == 0 {
		return nil
	}

	// 原子操作递增计数器
	next := atomic.AddUint64(&r.current, 1) - 1
	idx := next % uint64(len(r.backends))
	return &r.backends[idx]
}

// UpdateBackends 更新后端列表
func (r *RoundRobinStrategy) UpdateBackends(backends []config.BackendConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.backends = backends
}

// LeastConnStrategy 实现最少连接策略
type LeastConnStrategy struct {
	backends []config.BackendConfig
	counts   []int64
	mu       sync.RWMutex
}

// NewLeastConnStrategy 创建一个新的最少连接策略
func NewLeastConnStrategy(backends []config.BackendConfig) *LeastConnStrategy {
	return &LeastConnStrategy{
		backends: backends,
		counts:   make([]int64, len(backends)),
	}
}

// NextBackend 获取当前连接数最少的后端
func (l *LeastConnStrategy) NextBackend(req *http.Request) *config.BackendConfig {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if len(l.backends) == 0 {
		return nil
	}

	// 找到连接数最少的后端
	minIdx := 0
	minCount := atomic.LoadInt64(&l.counts[0])

	for i := 1; i < len(l.counts); i++ {
		count := atomic.LoadInt64(&l.counts[i])
		if count < minCount {
			minCount = count
			minIdx = i
		}
	}

	// 增加选中后端的连接计数
	atomic.AddInt64(&l.counts[minIdx], 1)
	return &l.backends[minIdx]
}

// DecrementConn 减少后端连接计数
func (l *LeastConnStrategy) DecrementConn(backendName string) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	for i, backend := range l.backends {
		if backend.Name == backendName {
			atomic.AddInt64(&l.counts[i], -1)
			break
		}
	}
}

// UpdateBackends 更新后端列表
func (l *LeastConnStrategy) UpdateBackends(backends []config.BackendConfig) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// 创建新的计数数组
	newCounts := make([]int64, len(backends))

	// 尝试保留现有后端的连接计数
	for i, newBackend := range backends {
		for j, oldBackend := range l.backends {
			if newBackend.Name == oldBackend.Name {
				newCounts[i] = l.counts[j]
				break
			}
		}
	}

	l.backends = backends
	l.counts = newCounts
}

// IPHashStrategy 实现基于客户端IP的哈希策略
type IPHashStrategy struct {
	backends []config.BackendConfig
	mu       sync.RWMutex
}

// NewIPHashStrategy 创建一个新的IP哈希策略
func NewIPHashStrategy(backends []config.BackendConfig) *IPHashStrategy {
	return &IPHashStrategy{
		backends: backends,
	}
}

// NextBackend 基于客户端IP选择后端
func (i *IPHashStrategy) NextBackend(req *http.Request) *config.BackendConfig {
	i.mu.RLock()
	defer i.mu.RUnlock()

	if len(i.backends) == 0 {
		return nil
	}

	// 获取客户端IP
	clientIP := req.RemoteAddr
	// 如果有X-Forwarded-For头，使用它
	if forwardedFor := req.Header.Get("X-Forwarded-For"); forwardedFor != "" {
		clientIP = forwardedFor
	}

	// 计算哈希值
	h := fnv.New32a()
	h.Write([]byte(clientIP))
	idx := h.Sum32() % uint32(len(i.backends))

	return &i.backends[idx]
}

// UpdateBackends 更新后端列表
func (i *IPHashStrategy) UpdateBackends(backends []config.BackendConfig) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.backends = backends
}

// LoadBalancer 负载均衡器
type LoadBalancer struct {
	strategy Strategy
	mu       sync.RWMutex
}

// NewLoadBalancer 创建一个新的负载均衡器
func NewLoadBalancer(strategy string, backends []config.BackendConfig) *LoadBalancer {
	var s Strategy

	switch strategy {
	case "least_conn":
		s = NewLeastConnStrategy(backends)
	case "ip_hash":
		s = NewIPHashStrategy(backends)
	default: // 默认使用轮询策略
		s = NewRoundRobinStrategy(backends)
	}

	return &LoadBalancer{
		strategy: s,
	}
}

// NextBackend 获取下一个后端
func (lb *LoadBalancer) NextBackend(req *http.Request) *config.BackendConfig {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	return lb.strategy.NextBackend(req)
}

// UpdateBackends 更新后端列表
func (lb *LoadBalancer) UpdateBackends(backends []config.BackendConfig) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	lb.strategy.UpdateBackends(backends)
}

// DecrementConn 减少后端连接计数（仅适用于最少连接策略）
func (lb *LoadBalancer) DecrementConn(backendName string) {
	if lc, ok := lb.strategy.(*LeastConnStrategy); ok {
		lc.DecrementConn(backendName)
	}
}
