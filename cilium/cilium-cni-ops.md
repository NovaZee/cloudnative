# Cilium CNI 运维手册

> Cilium CNI 插件故障排查与运维指南
>
> **适用版本**: Cilium 1.12+
> **文档版本**: 1.0
> **最后更新**: 2026-01-13

---

## 目录

- [1. 架构概述](#1-架构概述)
- [2. 文件位置](#2-文件位置)
- [3. 日志管理](#3-日志管理)
- [4. 常见问题排查](#4-常见问题排查)
- [5. 故障排查流程](#5-故障排查流程)
- [6. 调试技巧](#6-调试技巧)
- [7. 性能调优](#7-性能调优)
- [8. 应急处理](#8-应急处理)

---

## 1. 架构概述

### 1.1 组件关系

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        Cilium 网络组件架构                                   │
└─────────────────────────────────────────────────────────────────────────────┘

    ┌─────────┐         ┌─────────────────┐         ┌─────────────────┐
    │ kubelet │────────>│   cilium-cni    │────────>│  cilium-agent   │
    │ (常驻)  │  调用   │   (按需执行)    │  连接   │    (常驻)       │
    └─────────┘         └─────────────────┘         └─��───────────────┘
                               │                            │
                               │                            │
                        ┌──────┴──────┐            ┌──────┴──────┐
                        │ 网络设备创建  │            │ IPAM/策略    │
                        │ 接口配置     │            │ Endpoint管理│
                        └─────────────┘            └─────────────┘

    调用时机:
    - Pod ADD: 创建容器网络
    - Pod DEL: 清理容器网络
    - Pod CHECK: 检查容器网络状态
```

### 1.2 执行模式

| 组件 | 运行模式 | 调用者 | 生命周期 |
|------|----------|--------|----------|
| **cilium-cni** | 可执行程序 | kubelet | 调用时启动，执行完立即退出 |
| **cilium-agent** | 守护进程 | Kubernetes | 常驻运行 (DaemonSet) |

---

## 2. 文件位置

### 2.1 路径汇总

| 类型 | 路径 | 说明 |
|------|------|------|
| **CNI 二进制** | `/opt/cni/bin/cilium-cni` | 可执行文件 |
| **CNI 配置** | `/etc/cni/net.d/05-cilium.conflist` | CNI 配置文件 |
| **CNI 日志** | `/var/run/cilium/cilium-cni.log` | 运行时日志 |
| **Agent Socket** | `/var/run/cilium/cilium.sock` | API 通信 |
| **运行时状态** | `/var/run/cilium/` | 状态文件目录 |

### 2.2 快速检查命令

```bash
# 检查 CNI 插件是否安装
ls -la /opt/cni/bin/cilium-cni

# 检查 CNI 配置
cat /etc/cni/net.d/05-cilium.conflist

# 检查运行目录
ls -la /var/run/cilium/

# 检查 CNI 日志
ls -la /var/run/cilium/cilium-cni.log
```

### 2.3 Kubernetes 环境挂载

```yaml
# cilium-agent DaemonSet 挂载点
volumeMounts:
  # CNI 二进制目录
  - name: cni-path
    mountPath: /host/opt/cni/bin

  # CNI 配置目录
  - name: cni-conf-dir
    mountPath: /host/etc/cni/net.d

  # 运行时目录
  - name: cilium-run
    mountPath: /var/run/cilium
```

---

## 3. 日志管理

### 3.1 日志配置

**CNI 配置文件** (`/etc/cni/net.d/05-cilium.conflist`):

```json
{
  "cniVersion": "1.0.0",
  "name": "cilium",
  "type": "cilium-cni",
  "log-file": "/var/run/cilium/cilium-cni.log",
  "log-level": "info",
  "enable-debug": false,
  "ipam": {
    "type": "cilium-ipam"
  }
}
```

**日志级别**:

| 级别 | 用途 | 性能影响 |
|------|------|----------|
| `debug` | 详细调试信息 | 高 |
| `info` | 常规运行信息 | 低 |
| `warn` | 警告信息 | 极低 |
| `error` | 仅错误 | 极低 |

### 3.2 日志查看命令

```bash
# 1. 实时查看 CNI 日志
tail -f /var/run/cilium/cilium-cni.log

# 2. 查看最近的错误
grep -i error /var/run/cilium/cilium-cni.log | tail -50

# 3. 查看 Pod 网络配置过程
grep "Processing CNI ADD" /var/run/cilium/cilium-cni.log

# 4. 查看 IP 分配情况
grep "IPAM" /var/run/cilium/cilium-cni.log

# 5. Kubernetes 环境查看
kubectl -n kube-system exec ds/cilium -- tail -f /var/run/cilium/cilium-cni.log

# 6. 查看特定 Pod 的 CNI 调用
grep "<pod-name>" /var/run/cilium/cilium-cni.log

# 7. 统计错误类型
grep -i error /var/run/cilium/cilium-cni.log | awk '{print $NF}' | sort | uniq -c
```

### 3.3 启用调试模式

**临时启用** (修改配置文件):

```bash
# 编辑 CNI 配置
vi /etc/cni/net.d/05-cilium.conflist

# 修改以下字段
{
  "log-level": "debug",
  "enable-debug": true
}
```

**永久启用** (Helm 安装):

```bash
helm upgrade cilium cilium/cilium \
  --namespace kube-system \
  --set cni.logLevel=debug \
  --set cni.enableDebug=true
```

### 3.4 日志分析示例

```bash
# 查看 Pod 创建失败的详细信息
grep -A 20 "Processing CNI ADD" /var/run/cilium/cilium-cni.log | grep -B 5 -A 15 "error"

# 查看 IP 分配延迟
grep "IPAMAllocate" /var/run/cilium/cilium-cni.log | \
  awk '{print $1, $2}' | \
  awk 'NR>1 {print ($1-$prev)*1000" ms"; prev=$1}'

# 统计最近的 CNI 调用次数
echo "ADD: $(grep -c "CNI ADD" /var/run/cilium/cilium-cni.log)"
echo "DEL: $(grep -c "CNI DEL" /var/run/cilium/cilium-cni.log)"
```

---

## 4. 常见问题排查

### 4.1 Pod 无法启动 (ContainerCreating)

**症状**: Pod 一直处于 `ContainerCreating` 状态

**排查步骤**:

```bash
# 1. 查看 Pod 事件
kubectl describe pod <pod-name> -n <namespace>

# 2. 常见错误信息
# - "Failed to create pod network"
# - "Unable to connect to Cilium agent"
# - "IP allocation failed"

# 3. 检查 CNI 插件
ls -la /opt/cni/bin/cilium-cni
file /opt/cni/bin/cilium-cni  # 确认是可执行文件

# 4. 测试 CNI 插件
/opt/cni/bin/cilium-cni --version

# 5. 检查 Cilium Agent
kubectl -n kube-system get pod -l k8s-app=cilium
kubectl -n kube-system logs ds/cilium --tail=100
```

**常见原因与解决方案**:

| 错误信息 | 原因 | 解决方案 |
|---------|------|----------|
| `Unable to connect to Cilium agent` | Agent 未运行或 Socket 不可达 | 检查 `cilium-agent` Pod 状态，检查 `/var/run/cilium/cilium.sock` |
| `IPAM allocation failed` | IP 池耗尽或配置错误 | 检查 `cilium ipam lease list`，扩大 IP 池 |
| `Timeout` | 网络设备创建超时 | 检查节点资源，增加 `--cni-wait-timeout` |

---

### 4.2 Pod 无法访问网络

**症状**: Pod 启动成功但网络不通

**排查步骤**:

```bash
# 1. 检查 Pod 网络接口
kubectl exec -it <pod-name> -- ip addr
# 应该看到 eth0 或其他网络接口

# 2. 检查路由
kubectl exec -it <pod-name> -- ip route

# 3. 检查 DNS
kubectl exec -it <pod-name> -- nslookup kubernetes.default

# 4. 检查 Endpoint 状态
cilium endpoint list
cilium endpoint info <pod-id>

# 5. 检查网络策略
cilium policy get

# 6. 测试连通性
kubectl exec -it <pod-name> -- ping -c 3 8.8.8.8
kubectl exec -it <pod-name> -- curl -I https://www.google.com
```

---

### 4.3 CNI 调用超时

**症状**: kubelet 报告 CNI 调用超时

**排查步骤**:

```bash
# 1. 查看 CNI 日志中的执行时间
grep "Processing CNI ADD" -A 5 /var/run/cilium/cilium-cni.log | grep "duration"

# 2. 检查 Agent 响应时间
cilium status

# 3. 检查系统资源
top -bn1 | head -20
df -h

# 4. 检查 Socket 连接
ls -la /var/run/cilium/cilium.sock
ss -xln | grep cilium
```

**优化方案**:

```bash
# 增加超时时间 (kubelet)
# 编辑 /etc/kubernetes/kubelet
--network-plugin=cni \
--cni-wait-timeout=60s

# 优化 Cilium Agent 性能
# 减少 BPF 程序复杂度
# 禁用不必要的功能
```

---

### 4.4 IP 分配冲突

**症状**: 多个 Pod 获得相同 IP 或 IP 分配失败

**排查步骤**:

```bash
# 1. 查看已分配的 IP
cilium ipam lease list

# 2. 检查重复 IP
cilium ipam lease list | awk '{print $2}' | sort | uniq -d

# 3. 查看特定 Pod 的 IP
cilium endpoint list -o jsonpath='{range .items[*]}{.spec.identity}{": "}{.status.networking.addressing}{"\n"}{end}'

# 4. 强制释放 IP
cilium ipam release <ip-address>

# 5. 检查 IP 池配置
kubectl get cm cilium-config -n kube-system -o yaml | grep -i ipam
```

---

### 4.5 CNI 配置错误

**症状**: CNI 调用失败，日志显示配置错误

**排查步骤**:

```bash
# 1. 验证配置文件语法
cat /etc/cni/net.d/05-cilium.conflist | jq .

# 2. 检查配置文件权限
ls -la /etc/cni/net.d/
# 应该是 644 或 600

# 3. 比对工作节点的配置
diff /etc/cni/net.d/05-cilium.conflist <other-node>:/etc/cni/net.d/05-cilium.conflist

# 4. 检查 CNI 配置路径
kubelet --cni-conf-dir
# 默认: /etc/cni/net.d
```

---

## 5. 故障排查流程

### 5.1 完整排查树

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         Pod 网络问题排查流程                                │
└─────────────────────────────────────────────────────────────────────────────┘

                                    Pod 问题
                                       │
                              ┌────────┴────────┐
                              │                 │
                        Pod 启动失败      Pod 运行但网络不通
                              │                 │
                              ▼                 ▼
                    ┌─────────────────┐   ┌─────────────────┐
                    │ 1. 检查 Pod 事件  │   │ 1. 检查 Endpoint  │
                    │ kubectl describe│   │ cilium endpoint  │
                    └────────┬────────┘   └────────┬────────┘
                             │                      │
                             ▼                      ▼
                    ┌─────────────────┐   ┌─────────────────┐
                    │ 2. 查看 CNI 日志  │   │ 2. 检查网络策略  │
                    │ /var/run/cilium/ │   │ cilium policy    │
                    └────────┬────────┘   └────────┬────────┘
                             │                      │
                             ▼                      ▼
                    ┌─────────────────┐   ┌─────────────────┐
                    │ 3. 检查 Agent    │   │ 3. 测试网络连通性│
                    │ kubectl logs     │   │ ping/curl       │
                    └────────┬────────┘   └────────┬────────┘
                             │                      │
                             └──────────┬───────────┘
                                        ▼
                              ┌─────────────────┐
                              │ 4. 检查系统状态  │
                              │ top/df/iostat   │
                              └────────┬────────┘
                                       │
                                       ▼
                              ┌─────────────────┐
                              │ 5. 收集诊断信息  │
                              │ cilium bugtool  │
                              └─────────────────┘
```

### 5.2 快速诊断脚本

```bash
#!/bin/bash
# cni-quick-diagnose.sh

echo "=== Cilium CNI 快速诊断 ==="
echo ""

echo "1. CNI 插件检查"
ls -la /opt/cni/bin/cilium-cni
echo ""

echo "2. CNI 配置检查"
cat /etc/cni/net.d/05-cilium.conflist | jq .
echo ""

echo "3. Cilium Agent 状态"
kubectl -n kube-system get pod -l k8s-app=cilium
echo ""

echo "4. CNI 日志最近错误"
grep -i error /var/run/cilium/cilium-cni.log | tail -10
echo ""

echo "5. Socket 检查"
ls -la /var/run/cilium/cilium.sock
ss -xln | grep cilium
echo ""

echo "6. 最近 CNI 调用"
echo "ADD: $(grep -c 'CNI ADD' /var/run/cilium/cilium-cni.log 2>/dev/null || echo 0)"
echo "DEL: $(grep -c 'CNI DEL' /var/run/cilium/cilium-cni.log 2>/dev/null || echo 0)"
echo ""

echo "7. IPAM 状态"
cilium ipam lease list | head -20
echo ""

echo "8. Endpoint 状态"
cilium endpoint list | head -20
echo ""

echo "=== 诊断完成 ==="
```

---

## 6. 调试技巧

### 6.1 手动执行 CNI

```bash
# 模拟 kubelet 调用 CNI ADD
cat <<EOF | CNI_COMMAND=ADD \
  CNI_CONTAINERID=test123 \
  CNI_NETNS=/var/run/netns/test \
  CNI_IFNAME=eth0 \
  CNI_PATH=/opt/cni/bin \
  /opt/cni/bin/cilium-cni < /proc/self/fd/0
{
  "cniVersion": "1.0.0",
  "name": "cilium",
  "type": "cilium-cni",
  "ipam": {
    "type": "cilium-ipam"
  }
}
EOF

# 模拟 CNI DEL
cat <<EOF | CNI_COMMAND=DEL \
  CNI_CONTAINERID=test123 \
  CNI_NETNS=/var/run/netns/test \
  CNI_IFNAME=eth0 \
  CNI_PATH=/opt/cni/bin \
  /opt/cni/bin/cilium-cni < /proc/self/fd/0
{
  "cniVersion": "1.0.0",
  "name": "cilium",
  "type": "cilium-cni"
}
EOF
```

### 6.2 使用 cilium-dbg

```bash
# 进入 Cilium 调试模式
cilium-dbg status

# 查看详细 Endpoint 信息
cilium-dbg endpoint list -o json

# 查看网络策略详情
cilium-dbg policy get -o json

# 实时监控事件
cilium-dbg monitor

# 查看路由表
cilium-dbg route list
```

### 6.3 抓包分析

```bash
# 在 Pod 中抓包
kubectl exec -it <pod-name> -- tcpdump -i eth0 -w /tmp/capture.pcap

# 在主机上抓 veth 流量
# 1. 找到对应的 veth pair
ip link show | grep <pod-id>

# 2. 抓包
tcpdump -i veth<id> -w /tmp/veth-capture.pcap

# 分析 BPF 程序
bpftool prog list
bpftool prog show id <prog-id> --json
```

### 6.4 启用详细日志

```bash
# 1. 修改 CNI 配置
cat > /etc/cni/net.d/05-cilium.conflist <<EOF
{
  "cniVersion": "1.0.0",
  "name": "cilium",
  "type": "cilium-cni",
  "log-file": "/var/run/cilium/cilium-cni.log",
  "log-level": "debug",
  "enable-debug": true,
  "ipam": {
    "type": "cilium-ipam"
  }
}
EOF

# 2. 重新部署 Pod 触发 CNI 调用

# 3. 查看详细日志
tail -f /var/run/cilium/cilium-cni.log
```

---

## 7. 性能调优

### 7.1 CNI 调用性能

**监控指标**:

```bash
# 统计 CNI 调用耗时
grep "Processing CNI ADD" -A 2 /var/run/cilium/cilium-cni.log | \
  grep "duration" | \
  awk '{print $NF}' | \
  sort -n

# 计算 P50, P95, P99
cat <<'SCRIPT' | python3
import numpy as np
times = []
# 将上面的耗时数据放入 times 数组
print(f"P50: {np.percentile(times, 50)}ms")
print(f"P95: {np.percentile(times, 95)}ms")
print(f"P99: {np.percentile(times, 99)}ms")
SCRIPT
```

**优化建议**:

| 配置项 | 默认值 | 优化建议 |
|--------|--------|----------|
| `log-level` | `info` | 生产环境使用 `warn` 或 `error` |
| `enable-debug` | `false` | 保持 `false` |
| `log-file` | 启用 | 高负载场景可考虑禁用 |

### 7.2 Agent 性能

```bash
# 查看 Agent 资源使用
kubectl -n kube-system top pod -l k8s-app=cilium

# 调整 Agent 资源限制
helm upgrade cilium cilium/cilium \
  --namespace kube-system \
  --set agent.resources.requests.cpu=200m \
  --set agent.resources.requests.memory=512Mi \
  --set agent.resources.limits.cpu=1000m \
  --set agent.resources.limits.memory=2Gi
```

### 7.3 IPAM 性能

```bash
# 查看 IPAM 分配速度
time cilium ipam allocate

# 预分配 IP 池
kubectl patch cm cilium-config -n kube-system --type merge \
  -p '{"data":{"ipam":"operator|=pool|ipv4-range=10.0.0.0-10.255.255.255"}}'
```

---

## 8. 应急处理

### 8.1 CNI 插件损坏

```bash
# 症状: /opt/cni/bin/cilium-cni 不存在或无法执行

# 1. 备份当前版本
cp /opt/cni/bin/cilium-cni /opt/cni/bin/cilium-cni.bak

# 2. 从正常的节点复制
scp <healthy-node>:/opt/cni/bin/cilium-cni /opt/cni/bin/cilium-cni
chmod +x /opt/cni/bin/cilium-cni

# 3. 或从官方下载
wget https://github.com/cilium/cilium/releases/download/v<version>/cilium-linux-amd64.tar.gz
tar -xzf cilium-linux-amd64.tar.gz
cp cilium-linux-amd64/plugins/cilium-cni/cilium-cni /opt/cni/bin/
chmod +x /opt/cni/bin/cilium-cni
```

### 8.2 CNI 配置丢失

```bash
# 症状: /etc/cni/net.d/ 目录为空或配置丢失

# 1. 从其他节点复制
scp <healthy-node>:/etc/cni/net.d/05-cilium.conflist /etc/cni/net.d/

# 2. 或重新生成配置
kubectl -n kube-system get configmap cilium-config -o jsonpath='{.data.cni-config}' \
  > /etc/cni/net.d/05-cilium.conflist

# 3. 重启相关 Pod
kubectl delete pod <affected-pods>
```

### 8.3 Agent 无响应

```bash
# 症状: CNI 日志显示 "Unable to connect to Cilium agent"

# 1. 检查 Agent Pod
kubectl -n kube-system get pod -l k8s-app=cilium

# 2. 重启 Agent
kubectl -n kube-system delete pod -l k8s-app=cilium

# 3. 检查 Socket
ls -la /var/run/cilium/cilium.sock
rm -f /var/run/cilium/cilium.sock  # 如有必要

# 4. 手动测试连接
nc -U -v /var/run/cilium/cilium.sock
```

### 8.4 网络大规模中断

```bash
# 应急步骤

# 1. 立即收集诊断信息
cilium bugtool --output /tmp/cilium-bugtool-output.tar.gz

# 2. 检查最近的配置变更
kubectl get events --sort-by='.lastTimestamp' | tail -50

# 3. 检查是否有大规模 Endpoint 问题
cilium endpoint list --wide

# 4. 如需回滚
kubectl rollout undo daemonset/cilium -n kube-system

# 5. 检查是否有 BPF 程序异常
bpftool prog list
```

### 8.5 IP 地址耗尽

```bash
# 症状: 日志显示 "IP allocation failed" 或 "No available IPs"

# 1. 检查当前 IP 使用情况
cilium ipam lease list | wc -l

# 2. 扩大 IP 池
kubectl patch cm cilium-config -n kube-system --type merge \
  -p '{"data":{"ipv4-native-routing-cidr":"10.0.0.0/8"}}'

# 3. 清理过期租约
cilium ipam lease expire --older-than 24h

# 4. 查看未使用的 Endpoint
cilium endpoint list | grep disconnected
```

---

## 9. 监控告警

### 9.1 关键指标

```yaml
# Prometheus 监控指标
- cilium_cni_operations_total          # CNI 调用总数
- cilium_cni_operations_duration_seconds # CNI 调用耗时
- cilium_cni_errors_total               # CNI 错误总数
- cilium_ipam_available_ips             # 可用 IP 数量
- cilium_endpoint_state                 # Endpoint 状态
```

### 9.2 告警规则示例

```yaml
# cilium-cni-alerts.yaml
groups:
- name: cilium-cni
  rules:
  - alert: CNIHighErrorRate
    expr: rate(cilium_cni_errors_total[5m]) > 0.1
    for: 5m
    labels:
      severity: warning
    annotations:
      summary: "CNI error rate too high"
      description: "CNI error rate is {{ $value }} errors/sec"

  - alert: CNISlowOperations
    expr: histogram_quantile(0.99, cilium_cni_operations_duration_seconds) > 5
    for: 5m
    labels:
      severity: warning
    annotations:
      summary: "CNI operations too slow"
      description: "P99 CNI operation time is {{ $value }}s"

  - alert: IPPoolExhausted
    expr: cilium_ipam_available_ips < 100
    for: 5m
    labels:
      severity: critical
    annotations:
      summary: "IP pool nearly exhausted"
      description: "Only {{ $value }} IPs available"
```

---

## 10. 附录

### 10.1 常用命令速查

```bash
# === 基础检查 ===
ls -la /opt/cni/bin/cilium-cni                    # 检查 CNI 插件
cat /etc/cni/net.d/05-cilium.conflist            # 查看配置
tail -f /var/run/cilium/cilium-cni.log           # 查看日志

# === Kubernetes 操作 ===
kubectl -n kube-system get pod -l k8s-app=cilium  # 查看 Agent 状态
kubectl -n kube-system logs ds/cilium             # 查看 Agent 日志
kubectl describe pod <pod-name>                   # 查看 Pod 事件

# === Cilium 操作 ===
cilium status                                     # 查看集群状态
cilium endpoint list                              # 查看 Endpoint
cilium ipam lease list                            # 查看 IP 分配
cilium policy get                                 # 查看网络策略

# === 网络调试 ===
kubectl exec -it <pod> -- ip addr                  # 查看 Pod 网络
kubectl exec -it <pod> -- ip route                # 查看路由表
kubectl exec -it <pod> -- ping -c 3 <target>      # 测试连通性

# === 故障排查 ===
cilium bugtool                                   # 收集诊断信息
cilium-dbg status                                # 详细状态
cilium-dbg monitor                               # 实时监控
```

### 10.2 故障排查检查清单

```
□ CNI 插件是否存在 (/opt/cni/bin/cilium-cni)
□ CNI 配置是否正确 (/etc/cni/net.d/05-cilium.conflist)
□ Cilium Agent 是否运行 (kubectl get pod -n kube-system)
□ Socket 文件是否存在 (/var/run/cilium/cilium.sock)
□ 日志文件是否可写 (/var/run/cilium/cilium-cni.log)
□ IP 池是否充足 (cilium ipam lease list)
□ 节点资源是否充足 (top, df)
□ 系统时间是否同步 (timedatectl)
□ 防火墙规则是否正确 (iptables -L)
□ BPF 程序是否加载 (bpftool prog list)
```

### 10.3 联系支持

当需要联系 Cilium 支持时，请提供以下信息：

```bash
# 生成诊断包
cilium bugtool

# 收集系统信息
kubectl version > /tmp/kubectl-version.txt
kubectl get nodes > /tmp/nodes.txt
kubectl get pods -A > /tmp/pods.txt

# CNI 信息
cp /etc/cni/net.d/05-cilium.conflist /tmp/
tail -100 /var/run/cilium/cilium-cni.log > /tmp/cni-log.txt
```

**相关文档**:
- [Cilium 官方文档](https://docs.cilium.io/)
- [CNI 规范](https://github.com/containernetworking/cni)