# Koordinator CPU 绑核

> 更新时间：2025-01-20

---

## 目录

1. [CPU 绑核概述](#1-cpu-绑核概述)
2. [绑核原理](#2-绑核原理)
3. [Koordinator CPU 管理机制](#3-koordinator-cpu-管理机制)
4. [配置方式](#4-配置方式)
5. [实战案例](#5-实战案例)
6. [最佳实践](#6-最佳实践)

---

## 1. CPU 绑核概述

### 1.1 什么是 CPU 绑核

CPU 绑核（CPU Pinning / CPU Affinity）是指将进程或线程绑定到特定的 CPU 核心上运行，避免进程在不同核心间迁移，从而：

- **减少上下文切换开销**：进程始终在固定核心运行，避免缓存失效
- **提高缓存命中率**：L1/L2/L3 缓存数据得以保留
- **降低延迟**：消除 CPU 迁移带来的延迟抖动
- **提升性能**：对于计算密集型和延迟敏感应用尤其重要

### 1.2 应用场景

| 场景 | 说明 | 绑核价值 |
|------|------|----------|
| **数据库** | MySQL、PostgreSQL | 减少 context switch，提升查询性能 |
| **缓存** | Redis、Memcached | 降低延迟，提升吞吐 |
| **网络服务** | Nginx、Envoy | 减少 packet processing 延迟 |
| **计算密集** | 科学计算、AI 推理 | 避免 NUMA 跨节点访问 |
| **实时系统** | 金融交易、游戏 | 保证确定性的低延迟 |

### 1.3 Linux CPU 亲和性基础

Linux 提供了多种 CPU 亲和性控制机制：

```bash
# 查看当前 CPU 亲和性
taskset -cp <pid>

# 设置进程 CPU 亲和性（绑定到 CPU 0-3）
taskset -c 0-3 <command>

# 使用 cgroup 限制 CPU
echo "0-3" > /sys/fs/cgroup/cpuset/myapp/cpuset.cpus
```

```
┌─────────────────────────────────────────────────────────────────┐
│                    CPU 亲和性工作原理                            │
└─────────────────────────────────────────────────────────────────┘

  无绑核情况：
  ┌────────────────────────────────────────────────────────────┐
  │                                                            │
  │  进程 A                                                    │
  │    ┌─────┐    ┌─────┐    ┌─────┐    ┌─────┐             │
  │    │CPU0 │───▶│CPU1 │───▶│CPU2 │───▶│CPU3 │  (频繁迁移)  │
  │    └─────┘    └─────┘    └─────┘    └─────┘             │
  │                                                            │
  │  问题：缓存失效、上下文切换开销、延迟抖动                   │
  └────────────────────────────────────────────────────────────┘

  绑核情况：
  ┌────────────────────────────────────────────────────────────┐
  │                                                            │
  │  进程 A  ──▶  ┌─────┐  ┌─────┐                            │
  │              │CPU0 │  │CPU1 │  (固定运行)                 │
  │              └─────┘  └─────┘                            │
  │                                                            │
  │  优势：缓存复用、无迁移开销、稳定延迟                       │
  └────────────────────────────────────────────────────────────┘
```

---

## 2. 绑核原理

### 2.1 NUMA 架构

现代服务器通常采用 NUMA（Non-Uniform Memory Access）架构：

```
┌─────────────────────────────────────────────────────────────────┐
│                      NUMA 架构示意图                             │
└─────────────────────────────────────────────────────────────────┘

  Node 0                          Node 1
  ┌─────────────────────┐      ┌─────────────────────┐
  │                     │      │                     │
  │  CPU0   CPU1   CPU2 │      │  CPU3   CPU4   CPU5 │
  │    │      │      │   │      │    │      │      │   │
  │    └──────┴──────┘   │      │    └──────┴──────┘   │
  │           │           │      │           │          │
  │      ┌────▼────┐     │      │      ┌────▼────┐     │
  │      │ L3 Cache│     │      │      │ L3 Cache│     │
  │      └────┬────┘     │      │      └────┬────┘     │
  │           │           │      │           │          │
  │      ┌────▼────┐     │      │      ┌────▼────┐     │
  │      │ Memory  │     │      │      │ Memory  │     │
  │      │ (本地)  │     │      │      │ (本地)  │     │
  │      └─────────┘     │      │      └─────────┘     │
  │                     │      │                     │
  └─────────────────────┘      └─────────────────────┘
          │                              │
          └────────── QPI ────────────────┘
              (跨节点访问延迟更高)
```

**绑核在 NUMA 架构中的重要性**：
- 进程绑定到 Node 0 的 CPU，优先使用 Node 0 的本地内存
- 避免 NUMA 跨节点访问，降低内存访问延迟

### 2.2 Cgroup CPU 控制

Linux cgroup 提供两种 CPU 控制机制：

| 机制 | subsystem | 用途 |
|------|-----------|------|
| **cpu** | cpu, cpuacct | 时间片配额、使用统计 |
| **cpuset** | cpuset | CPU 核心绑定、NUMA 节点绑定 |

```bash
# cpuset 配置示例
mkdir /sys/fs/cgroup/cpuset/myapp
echo "0-3" > /sys/fs/cgroup/cpuset/myapp/cpuset.cpus       # 绑定 CPU 0-3
echo "0" > /sys/fs/cgroup/cpuset/myapp/cpuset.mems          # 绑定 NUMA Node 0

# 将进程放入 cgroup
echo <pid> > /sys/fs/cgroup/cpuset/myapp/tasks
```

### 2.3 容器 CPU 亲和性

Kubernetes 通过 `cpu.cfs_quota_us` 和 `cpu.cfs_period_us` 实现 CPU 限制：

```yaml
resources:
  requests:
    cpu: "2"      # requests = 2 cores
  limits:
    cpu: "4"      # limits = 4 cores
```

**对应 cgroup 配置**：
```bash
# cpu.cfs_period_us = 100000 (100ms)
# cpu.cfs_quota_us = 200000 (2 cores * 100ms) = requests

# limits 下的 cpu.shares = 2048 (2 * 1024)
```

---

## 3. Koordinator CPU 管理机制

### 3.1 QoS 与 CPU 策略

Koordinator 通过 QoS 类实现不同的 CPU 管理策略：

| QoS 类 | CPU 策略 | 绑核方式 | 隔离级别 |
|--------|----------|----------|----------|
| **SYSTEM** | cpuPolicy: none | cpuset | 完全隔离 |
| **LSE** | cpuPolicy: none | cpuset | 完全隔离 |
| **LSR** | cpuPolicy: none | cpuset | 完全隔离 |
| **LS** | cpuPolicy: none | cpu shares | 优先级调度 |
| **BE** | cpuPolicy: none | cpu shares | 尽力而为 |

### 3.2 CPU 绑定配置

Koordinator 通过多种方式实现 CPU 绑定：

#### 方式一：通过 LSE QoS 实现独占

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: lse-cpu-pinned
  labels:
    koordinator.sh/qosClass: "LSE"
spec:
  containers:
  - name: app
    image: nginx
    resources:
      requests:
        cpu: "4"
      limits:
        cpu: "4"
```

**效果**：
- Koordinator 为 Pod 分配独占的 CPU 核心
- 使用 `cpuset cgroup` 绑定到固定核心
- 其他 Pod 无法使用这些核心

#### 方式二：通过 CPU 亲和性 Annotation

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: cpu-affinity-pod
  annotations:
    # 指定 CPU 亲和性
    scheduling.koordinator.sh/cpu-agnostic: "false"
    scheduling.koordinator.sh/cpu-exclusive: "true"
spec:
  containers:
  - name: app
    image: nginx
    resources:
      requests:
        cpu: "2"
      limits:
        cpu: "2"
```

#### 方式三：通过 Node Label 和 Pod Affinity

```yaml
# 标记节点 CPU 拓扑
apiVersion: v1
kind: Node
metadata:
  labels:
    # 物理核数
    node.koordinator.sh/cpu-cores: "32"
    # CPU 型号
    node.koordinator.sh/cpu-model: "Intel_Xeon_E5-2680"
    # NUMA 节点数
    node.koordinator.sh/numa-nodes: "2"
```

### 3.3 Koordinator 调度器配置

```yaml
apiVersion: config.koordinator.sh/v1alpha1
kind: ClusterColocationProfile
metadata:
  name: cpu-pinning-profile
spec:
  selector:
    matchLabels:
      koordinator.sh/qosClass: "LSE"
  namespaceSelector:
    matchNames:
      - production
  schedulerName: koord-scheduler
  priority: 100
  # CPU 绑定策略
  cpuPolicy:
    policy: "none"  # none 表示使用 cpuset 独占
  # 内存 QoS
  memoryQOS:
    enable: true
    minLimitPercent: 50
```

---

## 4. 配置方式

### 4.1 基础 CPU 绑定

#### 单容器 Pod 绑核

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: cpu-pinned-simple
  labels:
    koordinator.sh/qosClass: "LSE"
spec:
  schedulerName: koord-scheduler
  containers:
  - name: app
    image: redis:7-alpine
    resources:
      requests:
        cpu: "4"      # 请求 4 个独占核心
        memory: "8Gi"
      limits:
        cpu: "4"
        memory: "8Gi"
    # 验证绑核的命令
    command:
      - /bin/sh
      - -c
      - |
        echo "CPU Affinity:"
        taskset -cp $$
        redis-server --save ""
```

**验证绑核**：
```bash
# 查看 Pod 运行的 CPU
kubectl exec cpu-pinned-simple -- taskset -cp 1

# 查看节点 cgroup 配置
kubectl get pod cpu-pinned-simple -o jsonpath='{.status.nodeName}'
# SSH 到节点后：
cat /sys/fs/cgroup/cpuset/kubepods.slice/.../cpuset.cpus
```

#### 多容器 Pod 绑核

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: multi-container-pinned
  labels:
    koordinator.sh/qosClass: "LSE"
spec:
  schedulerName: koord-scheduler
  containers:
  # 主应用容器
  - name: app
    image: nginx:alpine
    resources:
      requests:
        cpu: "2"
      limits:
        cpu: "2"
  # 日志收集容器（共享核心）
  - name: log-collector
    image: fluent/fluent-bit
    resources:
      requests:
        cpu: "500m"
      limits:
        cpu: "500m"
```

### 4.2 NUMA 感知绑核

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: numa-aware-pinned
  labels:
    koordinator.sh/qosClass: "LSE"
  annotations:
    # 指定 NUMA 节点
    scheduling.koordinator.sh/numa-node: "0"
spec:
  schedulerName: koord-scheduler
  containers:
  - name: app
    image: postgres:15-alpine
    resources:
      requests:
        cpu: "8"
        memory: "32Gi"
      limits:
        cpu: "8"
        memory: "32Gi"
    env:
    - name: POSTGRES_SHARED_BUFFERS
      value: "8GB"
    - name: POSTGRES_EFFECTIVE_CACHE_SIZE
      value: "24GB"
```

### 4.3 物理核 vs 逻辑核

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: physical-core-pinned
  labels:
    koordinator.sh/qosClass: "LSE"
  annotations:
    # 绑定物理核（避免超线程干扰）
    scheduling.koordinator.sh/cpu-bind-policy: "PhysicalCore"
spec:
  schedulerName: koord-scheduler
  containers:
  - name: app
    image: mysql:8
    resources:
      requests:
        # 请求 4 个物理核（实际对应 8 个逻辑核）
        cpu: "4"
        memory: "16Gi"
      limits:
        cpu: "4"
        memory: "16Gi"
    env:
    - name: MYSQL_INNODB_BUFFER_POOL_SIZE
      value: "12G"
```

### 4.4 混合 QoS 场景

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: mixed-qos-example
  labels:
    koordinator.sh/qosClass: "LS"  # 延迟敏感
spec:
  schedulerName: koord-scheduler
  containers:
  # 关键业务容器（LS QoS，高优先级）
  - name: critical-app
    image: nginx:alpine
    resources:
      requests:
        cpu: "2"
        memory: "4Gi"
      limits:
        cpu: "2"
        memory: "4Gi"
  # 旁路容器（BE QoS，低优先级）
  - name: sidecar
    image: busybox
    resources:
      requests:
        cpu: "500m"
        memory: "512Mi"
      limits:
        cpu: "1"
        memory: "1Gi"
```

---

## 5. 实战案例

### 5.1 案例1：MySQL 数据库绑核

#### 场景描述
生产环境 MySQL 数据库，需要稳定性能，要求 CPU 绑核避免上下文切换。

#### 完整配置

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: mysql-config
  namespace: database
data:
  my.cnf: |
    [mysqld]
    # 基础配置
    port = 3306
    datadir = /var/lib/mysql
    socket = /var/run/mysqld/mysqld.sock

    # InnoDB 配置（适配绑核场景）
    innodb_buffer_pool_size = 24G
    innodb_buffer_pool_instances = 8
    innodb_log_file_size = 2G
    innodb_flush_log_at_trx_commit = 1
    innodb_flush_method = O_DIRECT

    # 并发配置
    innodb_thread_concurrency = 0
    innodb_read_io_threads = 8
    innodb_write_io_threads = 8

    # CPU 相关（绑核后可以适当降低）
    thread_handling = pool-of-threads
    thread_pool_size = 16

    # 连接配置
    max_connections = 2000
    max_connect_errors = 10000

    [client]
    port = 3306
    socket = /var/run/mysqld/mysqld.sock
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: mysql-pvc
  namespace: database
spec:
  accessModes:
    - ReadWriteOnce
  storageClassName: fast-ssd
  resources:
    requests:
      storage: 500Gi
---
apiVersion: v1
kind: Service
metadata:
  name: mysql
  namespace: database
spec:
  selector:
    app: mysql
  ports:
  - port: 3306
    targetPort: 3306
  type: ClusterIP
---
apiVersion: v1
kind: Pod
metadata:
  name: mysql-cpu-pinned
  namespace: database
  labels:
    app: mysql
    koordinator.sh/qosClass: "LSE"  # 独占资源
  annotations:
    # CPU 绑定策略
    scheduling.koordinator.sh/cpu-exclusive: "true"
    scheduling.koordinator.sh/cpu-bind-policy: "FullPCPUs"
    # NUMA 优化
    scheduling.koordinator.sh/numa-aware: "true"
spec:
  schedulerName: koord-scheduler
  priorityClassName: high-priority
  containers:
  - name: mysql
    image: mysql:8.0
    resources:
      requests:
        # 绑定 8 个物理核（16 线程）
        cpu: "8"
        memory: "32Gi"
      limits:
        cpu: "8"
        memory: "32Gi"
    ports:
    - containerPort: 3306
      name: mysql
    env:
    - name: MYSQL_ROOT_PASSWORD
      valueFrom:
        secretKeyRef:
          name: mysql-secret
          key: password
    - name: MYSQL_DATABASE
      value: "production"
    - name: MYSQL_CONFIG_FILE
      value: "/etc/mysql/conf.d/my.cnf"
    volumeMounts:
    - name: data
      mountPath: /var/lib/mysql
    - name: config
      mountPath: /etc/mysql/conf.d
    # 启动后验证 CPU 绑定
    command:
      - /bin/bash
      - -c
      - |
        echo "=== MySQL CPU Pinning Verification ==="
        echo "Process ID: $$"
        echo ""
        echo "CPU Affinity:"
        taskset -cp $$
        echo ""
        echo "NUMA Policy:"
        numactl -p $$
        echo ""
        echo "Starting MySQL..."
        exec docker-entrypoint.sh mysqld
    livenessProbe:
      exec:
        command:
        - mysqladmin
        - ping
        - -h
        - localhost
      initialDelaySeconds: 30
      periodSeconds: 10
    readinessProbe:
      exec:
        command:
        - mysqladmin
        - ping
        - -h
        - localhost
      initialDelaySeconds: 10
      periodSeconds: 5
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: mysql-pvc
  - name: config
    configMap:
      name: mysql-config
  # 节点选择：选择有足够 CPU 核心的节点
  nodeSelector:
    node.koordinator.sh/cpu-cores: "32"
  affinity:
    # Pod 反亲和：确保此 Pod 独占节点资源
    podAntiAffinity:
      requiredDuringSchedulingIgnoredDuringExecution:
      - labelSelector:
          matchLabels:
            app: mysql
        topologyKey: kubernetes.io/hostname
```

#### 验证与测试

```bash
# 1. 查看 Pod 调度状态
kubectl -n database get pod mysql-cpu-pinned -o wide

# 2. 进入 Pod 验证 CPU 绑定
kubectl -n database exec mysql-cpu-pinned -- taskset -cp 1

# 预期输出：
# pid 1's current affinity list: 0-7  (绑定到 CPU 0-7)

# 3. 查看实际运行的 CPU 核心
kubectl -n database exec mysql-cpu-pinned -- cat /proc/$$/status | grep Cpus_allowed

# 4. 性能测试
kubectl -n database exec mysql-cpu-pinned -- sysbench \
  --mysql-host=localhost \
  --mysql-port=3306 \
  --mysql-user=root \
  --mysql-password=xxx \
  --mysql-db=production \
  --tables=10 \
  --table-size=1000000 \
  oltp_read_write \
  prepare

kubectl -n database exec mysql-cpu-pinned -- sysbench \
  --mysql-host=localhost \
  --mysql-port=3306 \
  --mysql-user=root \
  --mysql-password=xxx \
  --mysql-db=production \
  --tables=10 \
  --table-size=1000000 \
  --threads=16 \
  --time=300 \
  oltp_read_write \
  run

# 5. 监控 CPU 使用
kubectl -n database exec mysql-cpu-pinned -- mpstat -P ALL 1
```

### 5.2 案例2：Redis 集群绑核

#### 场景描述
Redis 集群用于高并发缓存场景，对延迟非常敏感，需要 CPU 绑核并确保 NUMA 本地内存访问。

#### 完整配置

```yaml
# Redis ConfigMap
apiVersion: v1
kind: ConfigMap
metadata:
  name: redis-config
  namespace: cache
data:
  redis.conf: |
    # 网络配置
    port 6379
    bind 0.0.0.0
    protected-mode yes
    tcp-backlog 511
    timeout 0
    tcp-keepalive 300

    # 内存配置（绑核后使用大内存页）
    maxmemory 24gb
    maxmemory-policy allkeys-lru
    maxmemory-samples 5

    # 持久化配置
    save ""
    appendonly yes
    appendfilename "appendonly.aof"
    appendfsync everysec
    no-appendfsync-on-rewrite no
    auto-aof-rewrite-percentage 100
    auto-aof-rewrite-min-size 64mb

    # 性能配置（适配绑核）
    hash-max-ziplist-entries 512
    hash-max-ziplist-value 64
    list-max-ziplist-size -2
    list-compress-depth 0
    set-max-intset-entries 512
    zset-max-ziplist-entries 128
    zset-max-ziplist-value 64
    hll-sparse-max-bytes 3000

    # 线程配置（Redis 6.0+ 多线程 I/O）
    io-threads 4
    io-threads-do-reads yes

    # 日志配置
    loglevel notice
    logfile ""
    syslog-enabled no
---
# Redis StatefulSet
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: redis-cluster
  namespace: cache
spec:
  serviceName: redis-cluster
  replicas: 3
  selector:
    matchLabels:
      app: redis
  template:
    metadata:
      labels:
        app: redis
        koordinator.sh/qosClass: "LSE"
      annotations:
        scheduling.koordinator.sh/cpu-exclusive: "true"
        scheduling.koordinator.sh/cpu-bind-policy: "FullPCPUs"
        scheduling.koordinator.sh/numa-aware: "true"
    spec:
      schedulerName: koord-scheduler
      priorityClassName: high-priority
      containers:
      - name: redis
        image: redis:7-alpine
        resources:
          requests:
            cpu: "4"
            memory: "32Gi"
          limits:
            cpu: "4"
            memory: "32Gi"
        ports:
        - containerPort: 6379
          name: redis
        - containerPort: 16379
          name: gossip
        command:
        - /bin/sh
        - -c
        - |
          echo "=== Redis CPU Pinning Info ==="
          echo "Process ID: $$"
          echo "CPU Affinity:"
          taskset -cp $$
          echo "NUMA Policy:"
          numactl -p $$ || echo "numactl not available"
          echo ""
          echo "=== Starting Redis ==="
          exec redis-server /etc/redis/redis.conf
        volumeMounts:
        - name: data
          mountPath: /data
        - name: config
          mountPath: /etc/redis
        livenessProbe:
          exec:
            command:
            - redis-cli
            - ping
          initialDelaySeconds: 30
          periodSeconds: 5
        readinessProbe:
          exec:
            command:
            - redis-cli
            - ping
          initialDelaySeconds: 5
          periodSeconds: 1
      volumes:
      - name: config
        configMap:
          name: redis-config
  volumeClaimTemplates:
  - metadata:
      name: data
    spec:
      accessModes:
        - ReadWriteOnce
      storageClassName: fast-ssd
      resources:
        requests:
          storage: 100Gi
---
# Redis Service
apiVersion: v1
kind: Service
metadata:
  name: redis-cluster
  namespace: cache
spec:
  clusterIP: None
  selector:
    app: redis
  ports:
  - port: 6379
    targetPort: 6379
    name: redis
  - port: 16379
    targetPort: 16379
    name: gossip
```

#### 验证与性能测试

```bash
# 1. 验证 Redis Pod CPU 绑定
for i in 0 1 2; do
  echo "=== redis-cluster-$i ==="
  kubectl -n cache exec redis-cluster-$i -- taskset -cp 1
  kubectl -n cache exec redis-cluster-$i -- cat /proc/1/status | grep Cpus_allowed_list
done

# 2. 性能基准测试
kubectl -n cache exec redis-cluster-0 -- redis-benchmark \
  -h 127.0.0.1 \
  -p 6379 \
  -c 50 \
  -n 1000000 \
  -t set,get,lpush,lpop \
  -q

# 3. 延迟测试
kubectl -n cache exec redis-cluster-0 -- redis-benchmark \
  -h 127.0.0.1 \
  -p 6379 \
  -c 1 \
  -n 100000 \
  -t ping \
  -q

# 4. 监控 NUMA 内存分配
kubectl -n cache exec redis-cluster-0 -- cat /proc/1/numa_maps

# 5. 查看 Redis 性能统计
kubectl -n cache exec redis-cluster-0 -- redis-cli info stats
kubectl -n cache exec redis-cluster-0 -- redis-cli info memory
```

### 5.3 案例3：Nginx 网关绑核

#### 场景描述
高流量 Nginx 网关，需要处理大量并发连接，CPU 绑核以减少请求处理延迟。

#### 完整配置

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: nginx-config
  namespace: ingress
data:
  nginx.conf: |
    worker_processes auto;
    worker_rlimit_nofile 100000;
    worker_cpu_affinity auto;

    events {
        worker_connections 10000;
        use epoll;
        multi_accept on;
    }

    http {
        include /etc/nginx/mime.types;
        default_type application/octet-stream;

        # 日志格式
        log_format main '$remote_addr - $remote_user [$time_local] "$request" '
                        '$status $body_bytes_sent "$http_referer" '
                        '"$http_user_agent" "$http_x_forwarded_for" '
                        'rt=$request_time uct="$upstream_connect_time" '
                        'uht="$upstream_header_time" urt="$upstream_response_time"';

        access_log /var/log/nginx/access.log main;
        error_log /var/log/nginx/error.log warn;

        # 性能优化
        sendfile on;
        tcp_nopush on;
        tcp_nodelay on;
        keepalive_timeout 65;
        keepalive_requests 10000;
        reset_timedout_connection on;

        # Gzip 压缩
        gzip on;
        gzip_vary on;
        gzip_proxied any;
        gzip_comp_level 6;
        gzip_types text/plain text/css text/xml text/javascript
                   application/json application/javascript application/xml+rss;

        # 上游配置
        upstream backend {
            least_conn;
            server backend-service:8080 max_fails=3 fail_timeout=30s;
            keepalive 100;
        }

        server {
            listen 80;
            server_name _;

            location / {
                proxy_pass http://backend;
                proxy_http_version 1.1;
                proxy_set_header Connection "";
                proxy_set_header Host $host;
                proxy_set_header X-Real-IP $remote_addr;
                proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
                proxy_set_header X-Forwarded-Proto $scheme;

                proxy_connect_timeout 5s;
                proxy_send_timeout 60s;
                proxy_read_timeout 60s;
            }

            # 健康检查
            location /healthz {
                access_log off;
                return 200 "OK\n";
                add_header Content-Type text/plain;
            }
        }
    }
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: nginx-gateway
  namespace: ingress
spec:
  replicas: 4
  selector:
    matchLabels:
      app: nginx-gateway
  template:
    metadata:
      labels:
        app: nginx-gateway
        koordinator.sh/qosClass: "LS"  # 延迟敏感
      annotations:
        scheduling.koordinator.sh/cpu-exclusive: "true"
    spec:
      schedulerName: koord-scheduler
      priorityClassName: high-priority
      containers:
      - name: nginx
        image: nginx:alpine
        resources:
          requests:
            cpu: "2"
            memory: "2Gi"
          limits:
            cpu: "2"
            memory: "2Gi"
        ports:
        - containerPort: 80
          name: http
        volumeMounts:
        - name: config
          mountPath: /etc/nginx/nginx.conf
          subPath: nginx.conf
        - name: logs
          mountPath: /var/log/nginx
        livenessProbe:
          httpGet:
            path: /healthz
            port: 80
          initialDelaySeconds: 30
          periodSeconds: 10
        readinessProbe:
          httpGet:
            path: /healthz
            port: 80
          initialDelaySeconds: 5
          periodSeconds: 5
      volumes:
      - name: config
        configMap:
          name: nginx-config
      - name: logs
        emptyDir: {}
---
apiVersion: v1
kind: Service
metadata:
  name: nginx-gateway
  namespace: ingress
spec:
  selector:
    app: nginx-gateway
  ports:
  - port: 80
    targetPort: 80
    name: http
  type: LoadBalancer
```

### 5.4 案例4：Java 应用绑核

#### 场景描述
Java 微服务应用，绑定 CPU 后配置 JVM 以获得最佳性能。

#### 完整配置

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: java-app-config
  namespace: backend
data:
  JVM_OPTIONS: >
    -XX:+UseG1GC
    -XX:MaxGCPauseMillis=200
    -XX:+UseStringDeduplication
    -XX:+UseNUMA
    -XX:+PrintGCDetails
    -XX:+PrintGCTimeStamps
    -Xlog:gc*:file=/var/log/app/gc.log:time,uptime:filecount=10,filesize=100M
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: java-backend
  namespace: backend
spec:
  replicas: 6
  selector:
    matchLabels:
      app: java-backend
  template:
    metadata:
      labels:
        app: java-backend
        koordinator.sh/qosClass: "LSE"
      annotations:
        scheduling.koordinator.sh/cpu-exclusive: "true"
        scheduling.koordinator.sh/cpu-bind-policy: "FullPCPUs"
    spec:
      schedulerName: koord-scheduler
      containers:
      - name: app
        image: my-java-app:latest
        resources:
          requests:
            cpu: "4"
            memory: "8Gi"
          limits:
            cpu: "4"
            memory: "8Gi"
        env:
        - name: JVM_OPTIONS
          valueFrom:
            configMapKeyRef:
              name: java-app-config
              key: JVM_OPTIONS
        # 根据 CPU 核心数设置线程池
        - name: JAVA_OPTS
          value: "-Xms6g -Xmx6g -XX:ParallelGCThreads=4 -XX:ConcGCThreads=2"
        - name: SERVER_THREADS
          value: "200"  # 50 * CPU_CORES
        - name: WORKER_THREADS
          value: "100"  # 25 * CPU_CORES
        command:
        - /bin/sh
        - -c
        - |
          echo "=== JVM CPU Pinning Verification ==="
          echo "Process ID: $$"
          echo "CPU Affinity:"
          taskset -cp $$
          echo ""
          echo "Starting Java Application..."
          exec java $JVM_OPTIONS $JAVA_OPTS -jar /app.jar
        volumeMounts:
        - name: logs
          mountPath: /var/log/app
        livenessProbe:
          httpGet:
            path: /actuator/health
            port: 8080
          initialDelaySeconds: 60
          periodSeconds: 10
        readinessProbe:
          httpGet:
            path: /actuator/health/readiness
            port: 8080
          initialDelaySeconds: 30
          periodSeconds: 5
      volumes:
      - name: logs
        emptyDir: {}
```

### 5.5 案例5：C++ 计算密集型应用绑核

#### 场景描述
C++ 高性能计算服务，需要绑定物理核心并优化 NUMA 访问。

#### 完整配置

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: hpc-app-config
  namespace: hpc
data:
  run.sh: |
    #!/bin/bash
    set -e

    echo "=== HPC Application Startup ==="
    echo "Process ID: $$"

    # 显示 CPU 亲和性
    echo "CPU Affinity:"
    taskset -cp $$

    # 显示 NUMA 策略
    echo "NUMA Policy:"
    numactl -p $$ 2>/dev/null || echo "numactl not available"

    # 显示 CPU 信息
    echo "CPU Info:"
    lscpu | grep -E "^CPU\(s\)|^Thread|^Core|^Socket|^NUMA"

    # 设置线程亲和性（应用内部）
    export OMP_PROC_BIND=close
    export OMP_PLACES=cores
    export OMP_NUM_THREADS=8

    # 启动应用
    echo "Starting application..."
    exec /app/hpc-server \
      --threads=8 \
      --bind=cores \
      --numa-policy=local
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: hpc-compute
  namespace: hpc
spec:
  replicas: 2
  selector:
    matchLabels:
      app: hpc-compute
  template:
    metadata:
      labels:
        app: hpc-compute
        koordinator.sh/qosClass: "LSE"
      annotations:
        scheduling.koordinator.sh/cpu-exclusive: "true"
        scheduling.koordinator.sh/cpu-bind-policy: "PhysicalCore"
        scheduling.koordinator.sh/numa-aware: "true"
    spec:
      schedulerName: koord-scheduler
      priorityClassName: high-priority
      nodeSelector:
        # 选择具有相同 CPU 型号的节点
        node.koordinator.sh/cpu-model: "Intel_Xeon"
      containers:
      - name: compute
        image: hpc-app:latest
        resources:
          requests:
            cpu: "8"      # 8 个物理核
            memory: "32Gi"
          limits:
            cpu: "8"
            memory: "32Gi"
        command:
        - /bin/bash
        - /scripts/run.sh
        volumeMounts:
        - name: scripts
          mountPath: /scripts
        - name: data
          mountPath: /data
        - name: tmp
          mountPath: /tmp
        ports:
        - containerPort: 9000
          name: grpc
      volumes:
      - name: scripts
        configMap:
          name: hpc-app-config
          defaultMode: 0755
      - name: data
        persistentVolumeClaim:
          claimName: hpc-data-pvc
      - name: tmp
        emptyDir:
          medium: Memory  # 使用内存作为临时目录
          sizeLimit: 4Gi
```

---

## 6. 最佳实践

### 6.1 绑核决策树

```
┌─────────────────────────────────────────────────────────────────┐
│                     是否需要 CPU 绑核？                          │
└─────────────────────────────────────────────────────────────────┘
                                  │
                                  ▼
                    ┌─────────────────────────┐
                    │ 应用是否对延迟敏感？     │
                    └─────────────────────────┘
                         │              │
                        Yes            No
                         │              │
                         ▼              ▼
              ┌──────────────────┐    ┌──────────────────┐
              │ 是否为计算密集？  │    │ 不需要绑核       │
              └──────────────────┘    │ 使用常规 QoS      │
                   │         │        └──────────────────┘
                  Yes        No
                   │         │
                   ▼         ▼
          ┌──────────┐  ┌──────────┐
          │ 绑定物理核│  │ 绑定逻辑核│
          │ LSE QoS  │  │ LS QoS   │
          └──────────┘  └──────────┘
```

### 6.2 资源配额建议

| 应用类型 | CPU 配置 | QoS 类 | 绑核策略 |
|----------|----------|--------|----------|
| 数据库 | requests=limits | LSE | FullPCPUs |
| 缓存 | requests=limits | LSE | FullPCPUs |
| 网关 | requests=limits | LS | Exclusive |
| 计算 | requests=limits | LSE | PhysicalCore |
| 批处理 | requests < limits | BE | 无绑定 |

### 6.3 验证清单

```bash
#!/bin/bash
# CPU 绑核验证脚本

echo "=== CPU Pinning Verification ==="

# 1. 检查 Pod 使用的 QoS 类
echo "1. QoS Class:"
kubectl get pod $POD_NAME -o jsonpath='{.metadata.labels.koordinator\.sh/qosClass}'

# 2. 检查 CPU 请求与限制
echo "2. CPU Requests/Limits:"
kubectl get pod $POD_NAME -o jsonpath='{.spec.containers[0].resources.requests.cpu}'
kubectl get pod $POD_NAME -o jsonpath='{.spec.containers[0].resources.limits.cpu}'

# 3. 检查 Pod 所在节点
echo "3. Node:"
kubectl get pod $POD_NAME -o jsonpath='{.spec.nodeName}'

# 4. 进入 Pod 检查 CPU 亲和性
echo "4. CPU Affinity in Pod:"
kubectl exec $POD_NAME -- taskset -cp 1

# 5. 检查 cgroup 配置
NODE=$(kubectl get pod $POD_NAME -o jsonpath='{.spec.nodeName}')
PID=$(kubectl exec $POD_NAME -- echo $$)
echo "5. Cgroup cpuset:"
ssh $NODE "cat /proc/$PID/status | grep Cpus_allowed_list"
```

### 6.4 性能监控

```yaml
# Prometheus 监控配置
apiVersion: v1
kind: ConfigMap
metadata:
  name: cpu-pinning-metrics
  namespace: monitoring
data:
  cpu-pinning-rules.yml: |
    groups:
      - name: cpu_pinning
        rules:
          # 绑核 Pod 的 CPU 使用率
          - record: pod:pinned_cpu_usage_percent
            expr: |
              sum(rate(container_cpu_usage_seconds_total{pod=~".*"},
                      5m)) by (pod)
              / sum(kube_pod_container_resource_requests{resource="cpu",
                      pod=~".*"}) by (pod) * 100

          # 上下文切换率（越低越好）
          - record: pod:context_switches_per_second
            expr: |
              rate(container_context_switches_total{pod=~".*"}[5m])

          # 缓存命中率
          - record: pod:cache_reference_ratio
            expr: |
              rate(container_cache_references_total{pod=~".*"}[5m])
              / rate(container_cache_misses_total{pod=~".*"}[5m])
```

### 6.5 故障排查

| 问题 | 可能原因 | 解决方案 |
|------|----------|----------|
| Pod 无法调度 | 没有足够的独占 CPU | 降低 requests 或增加节点 |
| 性能未提升 | 绑核配置错误 | 检查 taskset 输出 |
| 延迟抖动 | NUMA 跨节点访问 | 添加 numa-aware annotation |
| CPU 使用不均 | OMP/MQ 配置不当 | 调整线程绑定策略 |

---

## 附录

### A. 常用命令参考

```bash
# 查看 CPU 拓扑
lscpu
lscpu -p=CPU,CORE,SOCKET,NODE

# 查看进程 CPU 亲和性
taskset -cp <pid>
cat /proc/<pid>/status | grep Cpus_allowed

# 设置 CPU 亲和性
taskset -c 0-3 <command>

# 查看 NUMA 策略
numactl -p <pid>
numactl --hardware

# 查看上下文切换
vmstat 1
cat /proc/<pid>/status | grep voluntary

# 查看缓存统计
perf stat -e cache-references,cache-misses <command>
```

### B. 性能测试工具

```bash
# sysbench CPU 测试
sysbench cpu --threads=8 --time=60 run

# redis-benchmark
redis-benchmark -c 50 -n 1000000 -t set,get

# mysqlslap
mysqlslap --concurrency=50 --iterations=100 \
  --number-of-queries=1000 --auto-generate-sql

# wrk (HTTP 压测)
wrk -t12 -c400 -d30s http://localhost:8080/
```

### C. 参考资料

- **Koordinator 官方文档**: https://koordinator.sh/docs/
- **Linux CPU Affinity**: https://man7.org/linux/man-pages/man7/cpuset.7.html
- **NUMA 架构**: https://www.kernel.org/doc/Documentation/ABI/testing/sysfs-devices-node
- **Kubernetes CPU Management**: https://kubernetes.io/docs/tasks/administer-cluster/cpu-management-policies/

---

**文档版本**: v1.0
**最后更新**: 2025-01-20