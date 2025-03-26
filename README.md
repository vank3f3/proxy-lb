# Dify 负载均衡器

这是一个为 `dify-plugin-daemon` 服务设计的负载均衡器，使用 Golang 实现，支持主动健康检查和多种负载均衡策略。

## 功能特性

- **负载均衡**：支持轮询、最少连接和IP哈希三种策略
- **主动健康检查**：定期检查后端服务健康状态
- **自动故障转移**：自动剔除不健康的后端，并在恢复后重新加入
- **配置热重载**：支持动态更新配置，无需重启服务
- **Docker 部署**：提供 Docker 镜像，方便部署

## 快速开始

### 配置文件

创建配置文件 `config.yaml`：

```yaml
server:
  port: 8080
  read_timeout: 5s
  write_timeout: 10s

health_check:
  path: "/health/check"
  interval: 10s
  timeout: 2s
  unhealthy_threshold: 3
  healthy_threshold: 2

load_balancing:
  strategy: "round_robin"  # 可选: round_robin, least_conn, ip_hash

backends:
  - name: "dify-plugin-daemon-1"
    url: "http://dify-plugin-daemon:5002"
    weight: 1
  - name: "dify-plugin-daemon-2"
    url: "http://dify-plugin-daemon-2:5002"
    weight: 1
  - name: "dify-plugin-daemon-3"
    url: "http://dify-plugin-daemon-3:5002"
    weight: 1
```

### 使用 Docker 运行

```bash
docker run -d \
  --name dify-lb \
  -p 8080:8080 \
  -v $(pwd)/config.yaml:/etc/lb/config.yaml \
  vank3f3/proxy-lb:latest
```

### 从源码构建

```bash
# 克隆仓库
git clone https://github.com/vank3f3/proxy-lb.git
cd proxy-lb

# 构建
go build -o lb-server ./cmd/server

# 运行
./lb-server -config configs/config.yaml
```

## 负载均衡策略

- **轮询 (round_robin)**：按顺序将请求分配给后端服务
- **最少连接 (least_conn)**：将请求发送到当前连接数最少的后端
- **IP 哈希 (ip_hash)**：基于客户端 IP 的哈希值选择后端，保证同一客户端请求总是发送到同一后端

## 健康检查

负载均衡器会定期向每个后端的 `/health/check` 路径发送 GET 请求，期望收到 HTTP 200 状态码。

- 当连续失败次数达到 `unhealthy_threshold` 时，后端会被标记为不健康并从负载均衡中移除
- 当连续成功次数达到 `healthy_threshold` 时，后端会被重新标记为健康并加入负载均衡

## 许可证

MIT 