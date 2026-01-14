# Pod 网络运维

本文档介绍如何通过 nsenter 进入 Pod 的网络命名空间执行网络运维操作，并特别针对 Cilium 网络环境提供排查方案。

## 目录

1. [查找 Pod PID](#查找-pod-pid)
2. [使用 nsenter 进入网络命名空间](#使用-nsenter-进入网络命名空间)
3. [常用网络操作](#常用网络操作)
4. [Cilium 网络环境运维](#cilium-网络环境运维)
5. [实用脚本](#实用脚本)
6. [故障排查](#故障排查)

---

## 查找 Pod PID

要对 Pod 内部执行网络操作，首先需要找到 Pod 容器的进程 ID（PID）。

### 方法 1：使用 containerd（Kubernetes 常见方式）

```bash
# 步骤 1：获取 Pod 的容器 ID
kubectl get pod <pod-name> -n <namespace> -o jsonpath='{.status.containerStatuses[0].containerID}'

# 输出示例：containerd://<container-id>

# 步骤 2：提取容器 ID（移除 'containerd://' 前缀）
CONTAINER_ID=$(kubectl get pod <pod-name> -n <namespace> -o jsonpath='{.status.containerStatuses[0].containerID}' | sed 's/.*://')

# 步骤 3：使用 containerd 查找 PID
# 对于 containerd，PID 存储在容器的状态文件中
PID=$(cat /run/containerd/io.containerd.runtime.v2.task/k8s.io/<container-id>/state.json | jq -r '.Pid')

# 或使用 ctr 命令
ctr -n k8s.io task ls | grep <container-id-short>
PID=$(ctr -n k8s.io task ls | grep <container-id-short> | awk '{print $2}')
```

### 方法 2：使用 crictl

```bash
# 步骤 1：列出容器并找到 Pod 的容器
crictl ps | grep <pod-name>

# 步骤 2：获取容器 ID
CONTAINER_ID=$(crictl ps | grep <pod-name> | awk '{print $1}')

# 步骤 3：获取 PID
crictl inspect $CONTAINER_ID | jq '.info.pid'
```

### 方法 3：使用 docker（旧版本）

```bash
# 直接从 docker inspect 获取 PID
docker inspect <container-id> | jq '.[0].State.Pid'
```

### 快捷单行命令

```bash
# 获取 Pod PID 的函数
function get_pod_pid() {
  local POD_NAME=$1
  local NAMESPACE=${2:-default}
  local CONTAINER_ID=$(kubectl get pod $POD_NAME -n $NAMESPACE -o jsonpath='{.status.containerStatuses[0].containerID}' | sed 's/.*://')
  cat /run/containerd/io.containerd.runtime.v2.task/k8s.io/$CONTAINER_ID/state.json 2>/dev/null | jq -r '.Pid' || \
    ctr -n k8s.io task ls | grep ${CONTAINER_ID:0:12} | awk '{print $2}'
}

# 使用方式
get_pod_pid <pod-name> <namespace>
```

---

## 使用 nsenter 进入网络命名空间

获得 PID 后，使用 nsenter 进入网络命名空间。

### 基本语法

```bash
# 进入进程的网络命名空间
nsenter -t <PID> -n

# -t: 目标 PID
# -n: 进入网络命名空间
```

### 不进入 Shell 直接执行命令

```bash
# 在 Pod 的网络命名空间中执行单个命令
nsenter -t <PID> -n <command>

# 示例：检查网络接口
nsenter -t <PID> -n ip addr

# 执行多个命令
nsenter -t <PID> -n bash -c "ip addr && ip route"
```

### 交互式 Shell

```bash
# 在 Pod 的网络命名空间中获取交互式 shell
nsenter -t <PID> -n /bin/bash

# 或进入所有命名空间（网络、挂载、UTS、IPC、pid）
nsenter -t <PID> -m -u -i -n -p /bin/bash
```

---

## 常用网络操作

进入网络命名空间后，可以执行各种网络运维操作。

### 1. ARP 表操作

```bash
# 显示 ARP 表
nsenter -t <PID> -n arp -n

# 以数字格式显示 ARP 条目
nsenter -t <PID> -n ip neigh show

# 删除 ARP 条目
nsenter -t <PID> -n ip neigh del <IP-ADDRESS> dev <interface>

# 添加静态 ARP 条目
nsenter -t <PID> -n ip neigh add <IP-ADDRESS> lladdr <MAC-ADDRESS> dev <interface> nud permanent
```

### 2. 路由表操作

```bash
# 显示路由表
nsenter -t <PID> -n route -n

# 使用 ip 命令显示路由表
nsenter -t <PID> -n ip route show

# 显示更详细的路由表信息
nsenter -t <PID> -n ip route show table all

# 添加路由
nsenter -t <PID> -n ip route add <network> via <gateway> dev <interface>

# 删除路由
nsenter -t <PID> -n ip route del <network>

# 添加默认路由
nsenter -t <PID> -n ip route add default via <gateway> dev <interface>
```

### 3. 网络接口操作

```bash
# 显示所有网络接口
nsenter -t <PID> -n ip link show

# 显示详细接口信息
nsenter -t <PID> -n ip -d link show

# 启用接口
nsenter -t <PID> -n ip link set <interface> up

# 禁用接口
nsenter -t <PID> -n ip link set <interface> down

# 修改接口 MTU
nsenter -t <PID> -n ip link set dev <interface> mtu <MTU_SIZE>

# 为接口添加 IP 地址
nsenter -t <PID> -n ip addr add <IP>/<CIDR> dev <interface>

# 从接口删除 IP 地址
nsenter -t <PID> -n ip addr del <IP>/<CIDR> dev <interface>
```

### 4. 网络连接调试

```bash
# 显示所有网络连接
nsenter -t <PID> -n ss -tunap

# 显示监听套接字
nsenter -t <PID> -n ss -tlnp

# 显示已建立的连接
nsenter -t <PID> -n ss -tnp

# 显示网络统计信息
nsenter -t <PID> -n netstat -s

# 测试连通性（ping）
nsenter -t <PID> -n ping -c 3 <destination-ip>

# 测试 TCP 连通性
nsenter -t <PID> -n timeout 5 bash -c 'cat < /dev/tcp/<host>/<port>'
```

### 5. iptables 操作

```bash
# 列出所有 iptables 规则
nsenter -t <PID> -n iptables -L -v -n

# 列出 NAT 规则
nsenter -t <PID> -n iptables -t nat -L -v -n

# 列出 filter 表规则
nsenter -t <PID> -n iptables -S

# 添加规则
nsenter -t <PID> -n iptables -A <chain> -j <target>

# 删除规则
nsenter -t <PID> -n iptables -D <chain> <rule-number>
```

### 6. 网络命名空间信息

```bash
# 显示进程所在的网络命名空间
nsenter -t <PID> -n readlink /proc/self/ns/net

# 显示主机上的所有网络命名空间
ls -la /var/run/netns/

# 显示网络接口统计信息
nsenter -t <PID> -n ip -s link show

# 显示套接字缓冲区使用情况
nsenter -t <PID> -n ss -m
```

---

## Cilium 网络环境运维

Cilium 是基于 eBPF 的 Kubernetes 网络方案，提供了强大的网络可观测性和安全能力。

### Cilium CLI 基础命令

```bash
# 查看 Cilium 状态
cilium status

# 查看 Cilium 配置
cilium config view

# 查看集群所有端点
cilium endpoint list

# 查看特定端点详情
cilium endpoint get <endpoint-id>

# 查看所有节点
cilium node list

# 查看特定节点信息
cilium node get <node-id>

# 查看 Cilium 版本
cilium version

# 查看 Cilium 日志
cilium logs
```

### 网络策略排查

#### 1. 查看网络策略

```bash
# 列出所有网络策略
cilium networkpolicy list

# 查看特定策略详情
cilium networkpolicy get <policy-name> -n <namespace>

# 以 YAML 格式查看策略
kubectl get cnp <policy-name> -n <namespace> -o yaml

# 查看策略与端点的关系
cilium networkpolicy get <policy-name> -n <namespace> --endpoints
```

#### 2. 策略调试模式

```bash
# 启用策略审计日志（不实际拒绝流量）
cilium policy enable --audit-mode

# 禁用审计模式（启用实际策略执行）
cilium policy disable --audit-mode

# 查看策略拒绝日志
cilium logs --type drop
```

#### 3. 策略流向追踪

```bash
# 查看源到目标的策略允许/拒绝情况
cilium policy trace --src <source-labels> --dst <destination-labels>

# 示例：追踪从 app=frontend 到 app=backend 的流量
cilium policy trace --src labels.app=frontend --dst labels.app=backend

# 使用 YAML 文件追踪策略
cilium policy trace -f policy.yaml
```

### Hubble 可观测性

Hubble 是 Cilium 的网络可观测性平台，提供实时流量可见性。

#### 1. Hubble 基础命令

```bash
# 查看实时流量（类似 tcpdump）
hubble observe

# 按命名空间过滤
hubble observe -n <namespace>

# 按标签过滤
hubble observe --label app=nginx

# 查看特定 Pod 的流量
hubble observe --pod <pod-name> -n <namespace>

# 显示流量详细信息（包括 L3-L7）
hubble observe -v

# 只显示被丢弃的流量
hubble observe --drop

# 只显示 HTTP 流量
hubble observe --protocol http
```

#### 2. 流量查询与分析

```bash
# 查看过去的流量记录
hubble observe --since 1h

# 查看特定端口流量
hubble observe --port 8080

# 查看特定 IP 的流量
hubble observe --source-ip 10.0.0.1

# 组合条件查询
hubble observe -n default --pod app=nginx --port 443 --protocol http

# 输出到文件进行分析
hubble observe -n default > traffic-dump.txt
```

#### 3. Hubble UI

```bash
# 端口转发访问 Hubble UI
kubectl port-forward -n kube-system deployment/hubble-ui 12000:8080

# 访问 http://localhost:12000
```

### eBPF 相关网络排查

#### 1. 查看端点和身份

```bash
# 查看所有端点及其身份标签
cilium endpoint list -o jsonpath='{[*].id}{"\n"}{[*].status.identity.id}{"\n"}'

# 查看特定 Pod 的身份
kubectl exec -n <namespace> <pod-name> -- cat /sys/fs/cgroup/cilium.slice/identity

# 查看身份到标签的映射
cilium identity list
```

#### 2. BPF 映射查看

```bash
# 查看 ARP 表（BPF 实现）
cilium bpf arp list

# 查看 LXC（端点）信息
cilium bpf lxc list

# 查看 NAT 映射
cilium bpf nat list

# 查看 IP 地址映射
cilium bpf ipcache list

# 查看 CT（连接跟踪）表
cilium bpf ct list global

# 查看特定连接的 CT 信息
cilium bpf ct list global | grep <src-ip>
```

#### 3. 数据路径统计

```bash
# 查看全局统计数据
cilium metrics list

# 查看特定端点的统计
cilium endpoint get <endpoint-id> --stats

# 查看 Proxy（如 Envoy）统计
cilium proxy stats list

# 实时监控指标
cilium monitor
```

### Cilium 网络故障排查流程

#### 场景 1：Pod 无法访问外部服务

```bash
# 1. 确认 Pod 端点状态
cilium endpoint list
cilium endpoint get <endpoint-id>

# 2. 检查是否有网络策略阻止
cilium policy get --labels

# 3. 使用 Hubble 观察实时流量
hubble observe --pod <pod-name> -n <namespace>

# 4. 检查连接跟踪表
cilium bpf ct list global | grep <destination-ip>

# 5. 检查 egress 相关配置
cilium config view| grep -i egress

# 6. 进入 Pod 网络命名空间验证路由
# (获取 PID 后执行)
PID=$(get_pod_pid <pod-name> <namespace>)
nsenter -t $PID -n ip route
nsenter -t $PID -n ping -c 3 <external-ip>
```

#### 场景 2：跨节点 Pod 通信失败

```bash
# 1. 检查所有节点状态
cilium node list

# 2. 检查节点间隧道状态
cilium bpf tunnel list

# 3. 查看 Cilium 隧道配置
cilium config view | grep tunnel

# 4. 使用 Hubble 追踪跨节点流量
hubble observe --source-node <node1> --destination-node <node2>

# 5. 检查是否有网络策略影响
cilium policy trace --src labels.kubernetes.io/hostname=<node1> \
                     --dst labels.kubernetes.io/hostname=<node2>

# 6. 检查节点端点
cilium endpoint list --nodes <node-name>

# 7. 查看 Cilium 健康状态
cilium status --all
```

#### 场景 3：DNS 解析问题

```bash
# 1. 检查 CoreDNS/ClusterDNS 端点
cilium endpoint list | grep -i dns

# 2. 使用 Hubble 观察 DNS 流量
hubble observe --port 53 --protocol udp

# 3. 检查 DNS 策略
cilium networkpolicy list | grep -i dns

# 4. 查看是否启用 DNS 代理
cilium config view | grep -i dns

# 5. 进入 Pod 网络命名空间测试 DNS
PID=$(get_pod_pid <pod-name> <namespace>)
nsenter -t $PID -n nslookup kubernetes.default.svc.cluster.local
```

#### 场景 4：网络策略不生效

```bash
# 1. 确认策略已部署
cilium networkpolicy list
kubectl get cnp -A

# 2. 查看策略详情
cilium networkpolicy get <policy-name> -n <namespace>

# 3. 追踪策略决策
cilium policy trace --src <source-labels> --dst <dest-labels>

# 4. 验证端点标签
kubectl get pod <pod-name> -n <namespace> --show-labels
cilium endpoint get <endpoint-id>

# 5. 启用审计模式测试策略
cilium policy enable --audit-mode
# 执行测试流量
cilium logs --type audit

# 6. 查看策略拒绝日志
hubble observe --drop
```

#### 场景 5：Service 访问异常

```bash
# 1. 检查 Service 和端点
kubectl get endpoints <service-name> -n <namespace>
cilium endpoint list | grep <service-labels>

# 2. 查看 Service 相关的 BPF 映射
cilium bpf svc list

# 3. 使用 Hubble 观察 Service 流量
hubble observe --dst-labels "<service-labels>"

# 4. 检查 NodePort/ClusterIP 配置
cilium config view | grep -E "node-port|cluster-"

# 5. 进入 Pod 测试 Service 连通性
PID=$(get_pod_pid <pod-name> <namespace>)
nsenter -t $PID -n curl -v http://<service-name>.<namespace>.svc.cluster.local:<port>
```

### Cilium 与 nsenter 联合排查

```bash
# 综合排查脚本
function cilium_pod_debug() {
  local POD_NAME=$1
  local NAMESPACE=${2:-default}

  echo "========================================"
  echo "Cilium Pod 网络排查：$POD_NAME"
  echo "========================================"
  echo ""

  # 1. 获取 Pod 信息
  echo "=== Pod 基本信息 ==="
  kubectl get pod $POD_NAME -n $NAMESPACE -o wide
  kubectl get pod $POD_NAME -n $NAMESPACE --show-labels
  echo ""

  # 2. 获取 PID
  local CONTAINER_ID=$(kubectl get pod $POD_NAME -n $NAMESPACE -o jsonpath='{.status.containerStatuses[0].containerID}' 2>/dev/null | sed 's/.*://')
  local PID=$(cat /run/containerd/io.containerd.runtime.v2.task/k8s.io/$CONTAINER_ID/state.json 2>/dev/null | jq -r '.Pid')

  if [ -z "$PID" ] || [ "$PID" = "null" ]; then
    echo "错误：无法获取 PID"
    return 1
  fi

  echo "=== 容器 PID: $PID ==="
  echo ""

  # 3. Cilium 端点信息
  echo "=== Cilium 端点信息 ==="
  local POD_IP=$(kubectl get pod $POD_NAME -n $NAMESPACE -o jsonpath='{.status.podIP}')
  cilium endpoint list | grep $POD_IP || echo "未找到对应的 Cilium 端点"
  echo ""

  # 4. Pod 内网络配置
  echo "=== Pod 网络接口 ==="
  nsenter -t $PID -n ip addr
  echo ""

  echo "=== Pod 路由表 ==="
  nsenter -t $PID -n ip route
  echo ""

  # 5. 连接跟踪信息
  echo "=== Pod 相关的 CT 条目 ==="
  cilium bpf ct list global | grep $POD_IP || echo "无相关 CT 条目"
  echo ""

  # 6. 最近流量（通过 Hubble）
  echo "=== 最近的流量（Hubble，需安装 Hubble） ==="
  command -v hubble >/dev/null 2>&1 && \
    hubble observe --pod $POD_NAME -n $NAMESPACE --since 5m || \
    echo "Hubble 未安装"
  echo ""
}

# 使用方式
cilium_pod_debug <pod-name> <namespace>
```

---

## 实用脚本

### 脚本 1：通用 Pod 网络调试函数

将此函数添加到 `~/.bashrc` 或 `~/.zshrc`：

```bash
# Pod 网络调试函数
pod-net-debug() {
  local POD_NAME=$1
  local NAMESPACE=${2:-default}
  local COMMAND=${3:-/bin/bash}

  # 验证输入
  if [ -z "$POD_NAME" ]; then
    echo "使用方法：pod-net-debug <pod-name> [namespace] [command]"
    echo "示例：pod-net-debug my-pod default 'ip addr'"
    return 1
  fi

  # 获取容器 ID
  local CONTAINER_ID=$(kubectl get pod $POD_NAME -n $NAMESPACE -o jsonpath='{.status.containerStatuses[0].containerID}' 2>/dev/null | sed 's/.*://')

  if [ -z "$CONTAINER_ID" ]; then
    echo "错误：在命名空间 '$NAMESPACE' 中找不到 pod '$POD_NAME' 的容器"
    return 1
  fi

  # 获取 PID
  local PID=$(cat /run/containerd/io.containerd.runtime.v2.task/k8s.io/$CONTAINER_ID/state.json 2>/dev/null | jq -r '.Pid')

  if [ -z "$PID" ] || [ "$PID" = "null" ]; then
    echo "错误：找不到容器 '$CONTAINER_ID' 的 PID"
    return 1
  fi

  echo "进入 Pod 的网络命名空间：$POD_NAME (PID: $PID, Container: ${CONTAINER_ID:0:12})"
  echo "执行命令：$COMMAND"
  echo "---"

  # 在网络命名空间中执行命令
  nsenter -t $PID -n $COMMAND
}

# 使用示例：
# pod-net-debug my-pod
# pod-net-debug my-pod kube-system 'ip addr'
# pod-net-debug my-pod default 'arp -n'
# pod-net-debug my-pod default 'route -n'
```

### 脚本 2：全面网络诊断

```bash
# 对 Pod 执行完整网络诊断
pod-net-diag() {
  local POD_NAME=$1
  local NAMESPACE=${2:-default}

  echo "========================================"
  echo "Pod 网络诊断：$POD_NAME"
  echo "命名空间：$NAMESPACE"
  echo "========================================"
  echo ""

  # 获取 PID（复用上述逻辑）
  local CONTAINER_ID=$(kubectl get pod $POD_NAME -n $NAMESPACE -o jsonpath='{.status.containerStatuses[0].containerID}' 2>/dev/null | sed 's/.*://')
  local PID=$(cat /run/containerd/io.containerd.runtime.v2.task/k8s.io/$CONTAINER_ID/state.json 2>/dev/null | jq -r '.Pid')

  if [ -z "$PID" ] || [ "$PID" = "null" ]; then
    echo "错误：找不到 PID"
    return 1
  fi

  echo "=== 网络接口 ==="
  nsenter -t $PID -n ip addr
  echo ""

  echo "=== 路由表 ==="
  nsenter -t $PID -n ip route
  echo ""

  echo "=== ARP 表 ==="
  nsenter -t $PID -n ip neigh
  echo ""

  echo "=== 监听端口 ==="
  nsenter -t $PID -n ss -tlnp
  echo ""

  echo "=== 已建立连接 ==="
  nsenter -t $PID -n ss -tnp
  echo ""

  echo "=== 接口统计 ==="
  nsenter -t $PID -n ip -s link show
  echo ""
}

# 使用方式：
# pod-net-diag my-pod default
```

### 脚本 3：快捷别名

```bash
# 常用操作的快捷别名
alias pod-ip='pod-net-debug $1 $2 "ip addr"'
alias pod-route='pod-net-debug $1 $2 "ip route"'
alias pod-arp='pod-net-debug $1 $2 "ip neigh"'
alias pod-ss='pod-net-debug $1 $2 "ss -tunap"'

# 使用方式：
# pod-ip my-pod default
# pod-route my-pod default
# pod-arp my-pod default
```

### 脚本 4：主机与 Pod 网络对比

```bash
# 对比主机和 Pod 的网络配置
pod-net-compare() {
  local POD_NAME=$1
  local NAMESPACE=${2:-default}

  echo "========================================"
  echo "主机与 Pod 网络配置对比"
  echo "========================================"
  echo ""

  # 获取 PID
  local CONTAINER_ID=$(kubectl get pod $POD_NAME -n $NAMESPACE -o jsonpath='{.status.containerStatuses[0].containerID}' 2>/dev/null | sed 's/.*://')
  local PID=$(cat /run/containerd/io.containerd.runtime.v2.task/k8s.io/$CONTAINER_ID/state.json 2>/dev/null | jq -r '.Pid')

  if [ -z "$PID" ] || [ "$PID" = "null" ]; then
    echo "错误：找不到 PID"
    return 1
  fi

  echo "=== 主机网络接口 ==="
  ip addr show | grep -E "^[0-9]+:|inet "
  echo ""

  echo "=== Pod 网络接口 ==="
  nsenter -t $PID -n ip addr show | grep -E "^[0-9]+:|inet "
  echo ""

  echo "=== 主机路由表 ==="
  ip route
  echo ""

  echo "=== Pod 路由表 ==="
  nsenter -t $PID -n ip route
  echo ""
}

# 使用方式：
# pod-net-compare my-pod default
```

### 脚本 5：Cilium 环境专用排查

```bash
# Cilium 环境下的 Pod 网络排查
cilium-net-debug() {
  local POD_NAME=$1
  local NAMESPACE=${2:-default}

  echo "========================================"
  echo "Cilium 网络排查：$POD_NAME ($NAMESPACE)"
  echo "========================================"
  echo ""

  # 检查 Cilium 是否安装
  if ! command -v cilium &> /dev/null; then
    echo "错误：cilium CLI 未安装"
    return 1
  fi

  # 获取 Pod IP
  local POD_IP=$(kubectl get pod $POD_NAME -n $NAMESPACE -o jsonpath='{.status.podIP}' 2>/dev/null)

  if [ -z "$POD_IP" ]; then
    echo "错误：无法获取 Pod IP"
    return 1
  fi

  echo "Pod IP: $POD_IP"
  echo ""

  # 1. 查找端点
  echo "=== Cilium 端点 ==="
  cilium endpoint list | grep $POD_IP || echo "未找到端点"
  echo ""

  # 2. 查看身份
  echo "=== 身份信息 ==="
  kubectl get pod $POD_NAME -n $NAMESPACE --show-labels
  echo ""

  # 3. 查看相关策略
  echo "=== 相关网络策略 ==="
  local POD_LABELS=$(kubectl get pod $POD_NAME -n $NAMESPACE -o jsonpath='{.metadata.labels}')
  echo "Pod 标签：$POD_LABELS"
  echo ""

  # 4. 连接跟踪
  echo "=== 连接跟踪条目 ==="
  cilium bpf ct list global | grep $POD_IP || echo "无相关 CT 条目"
  echo ""

  # 5. 最近丢弃的流量
  if command -v hubble &> /dev/null; then
    echo "=== 最近丢弃的流量 ==="
    timeout 5 hubble observe --drop --pod $POD_NAME -n $NAMESPACE || true
    echo ""
  fi

  # 6. 网络配置（使用 nsenter）
  local CONTAINER_ID=$(kubectl get pod $POD_NAME -n $NAMESPACE -o jsonpath='{.status.containerStatuses[0].containerID}' 2>/dev/null | sed 's/.*://')
  local PID=$(cat /run/containerd/io.containerd.runtime.v2.task/k8s.io/$CONTAINER_ID/state.json 2>/dev/null | jq -r '.Pid')

  if [ -n "$PID" ] && [ "$PID" != "null" ]; then
    echo "=== Pod 网络接口 ==="
    nsenter -t $PID -n ip addr
    echo ""
  fi
}

# 使用方式：
# cilium-net-debug my-pod default
```

---

## 故障排查

### 问题 1：权限被拒绝

```bash
# 解决方案：使用 sudo
sudo nsenter -t <PID> -n <command>
```

### 问题 2：找不到 PID 文件

```bash
# 检查容器是否仍在运行
kubectl get pod <pod-name> -n <namespace>

# 验证容器运行时
# 对于 containerd
ls /run/containerd/io.containerd.runtime.v2.task/k8s.io/

# 对于 cri-o
crictl ps
```

### 问题 3：nsenter 命令未找到

```bash
# 安装包含 nsenter 的 util-linux 包
# Ubuntu/Debian
sudo apt-get install util-linux

# RHEL/CentOS
sudo yum install util-linux
```

### 问题 4：Cilium 端点无法创建

```bash
# 检查 Cilium 状态
cilium status

# 查看 Cilium 日志
cilium logs --rel 100

# 检查 Cilium Agent Pod
kubectl logs -n kube-system -l k8s-app=cilium --tail=100

# 验证 CNI 配置
ls -la /etc/cni/net.d/
cat /etc/cni/net.d/05-cilium.conflist
```

### 问题 5：Hubble 无法观察流量

```bash
# 检查 Hubble 状态
kubectl get pods -n kube-system | grep hubble

# 启用 Hubble
cilium hubble enable

# 重启 Cilium 使配置生效
kubectl rollout restart ds/cilium -n kube-system

# 检查 Hubble 配置
cilium config view | grep -i hubble
```

---

## 参考资源

- [nsenter 手册](https://man7.org/linux/man-pages/man1/nsenter.1.html)
- [Kubernetes 网络策略](https://kubernetes.io/zh/docs/concepts/services-networking/network-policies/)
- [Linux 网络命名空间](https://man7.org/linux/man-pages/man7/network_namespaces.7.html)
- [Cilium 官方文档](https://docs.cilium.io/)
- [Cilium GitHub](https://github.com/cilium/cilium)
- [Hubble 文档](https://docs.cilium.io/en/stable/network/observability/hubble/)