# Koordinator QoS 使用指南

## 概述

Koordinator 是由阿里云开源的 Kubernetes 资源调度和 QoS 系统，旨在在 Kubernetes 集群中高效混部微服务、AI 和大数据等工作负载。它通过弹性资源配额、Pod 紧凑调度、资源超卖和容器资源隔离等方式，实现高资源利用率，同时保证工作负载的性能稳定性。Koordinator 支持灵活的调度策略、资源超卖和 QoS 机制，已在生产环境中大规模验证。

Koordinator 扩展了 Kubernetes 的 QoS 系统，引入独立的 QoS 类（通过标签 `koordinator.sh/qosClass` 指定），用于更好地管理资源隔离和优先级。Koordinator 的 QoS 类包括：SYSTEM、LSE（Latency Sensitive Exclusive）、LSR（Latency Sensitive Reserved）、LS（Latency Sensitive）和 BE（Best Effort）。这些 QoS 类兼容 Kubernetes 原生的 QoS（Guaranteed、Burstable、BestEffort），但提供了更精细的控制，尤其在 CPU 和内存资源上。

Koordinator 的 QoS 机制通过 QoSManager 动态调整资源参数，基于应用剖析、干扰检测和 SLO 配置，确保高优先级工作负载的性能。以下详细介绍几种 QoS 类的定义、使用场景、配置方法和示例。

## QoS 类详细介绍

Koordinator 定义了以下 QoS 类，用于表达 Pod 在节点上的运行质量，包括资源获取方式、比例和优先级。QoS 类通过 Pod 的标签 `koordinator.sh/qosClass` 指定。以下是几种主要 QoS 类的使用指南。

### 1. SYSTEM QoS 类
#### 定义与用途
SYSTEM 是最高优先级的 QoS 类，用于关键的系统级 DaemonSet Pod，例如 CSI 插件、网络插件等。这些 Pod 是 Kubernetes 节点正常运行的基础，如果它们无法正常工作，整个节点将失效。SYSTEM Pod 的资源使用必须严格限制，避免过度消耗；其资源参数应以最高优先级设置，不受其他 Pod 影响。在资源短缺时，应优先挤压或终止其他 Pod（如 LS 或 BE）。

- **资源管理行为**：资源可能独占（例如使用 cpuset），不与其他 Pod 共享。建议为 SYSTEM Pod 创建独立的 cgroup。
- **差异**：不同于 Kubernetes 的 `system-reserved`（内核进程如 sshd）和 `kube-reserved`（kubelet 和 containerd 等），SYSTEM 专为 DaemonSet Pod 设计。
- **使用场景**：系统组件、关键守护进程，需要绝对优先级的环境。

#### 配置方法
- 通过 Pod 标签指定：`koordinator.sh/qosClass: "SYSTEM"`
- 集群级配置：在 ConfigMap `slo-controller-config` 中启用相关 QoS 策略（例如 CPU/Memory QoS）。
- 高级：使用 Containerd-NRI 或 Koord-Runtime-Proxy 修改 cgroup 路径，确保独立管理。

#### 示例
```yaml
apiVersion: v1
kind: Pod
metadata:
  name: system-pod-example
  labels:
    koordinator.sh/qosClass: "SYSTEM"  # 指定 SYSTEM QoS
spec:
  containers:
  - name: csi-plugin
    image: example/csi-plugin:latest
    resources:
      limits:
        cpu: "1"
        memory: "1Gi"
      requests:
        cpu: "1"
        memory: "1Gi"
  schedulerName: default-scheduler
```

在资源压力下，SYSTEM Pod 的资源不会被回收，确保稳定性。

### 2. LSE (Latency Sensitive Exclusive) QoS 类
#### 定义与用途
LSE 是延迟敏感且独占资源的 QoS 类，适用于需要独占 CPU/内存以避免干扰的高敏感工作负载。它提供增强的隔离，确保 Pod 不与其他 Pod 共享硬件资源（如 SMT 核心），减少侧信道攻击风险和性能抖动。

- **资源管理行为**：独占资源分配（如 cpuset），高优先级调度。内存回收延迟，保护页面缓存。
- **差异**：比 LS 更严格的隔离，适合极端低延迟需求。
- **使用场景**：实时计算、金融交易系统、视频处理等，需要独占资源的延迟敏感应用。

#### 配置方法
- Pod 标签：`koordinator.sh/qosClass: "LSE"`
- CPU QoS 配置（ConfigMap）：启用 Core Scheduling，设置高优先级。
- Memory QoS 配置：设置 `minLimitPercent` 和 `lowLimitPercent` 以保护内存。

#### 示例
```yaml
apiVersion: v1
kind: Pod
metadata:
  name: lse-pod-example
  labels:
    koordinator.sh/qosClass: "LSE"  # 指定 LSE QoS
  annotations:
    koordinator.sh/memoryQOS: '{"policy": "auto", "minLimitPercent": 50}'  # 保护 50% 请求内存
spec:
  containers:
  - name: realtime-app
    image: example/realtime-app:latest
    resources:
      limits:
        cpu: "4"
        memory: "8Gi"
      requests:
        cpu: "4"
        memory: "8Gi"
```

结合 Core Scheduling 配置：
```yaml
# slo-controller-config ConfigMap 片段
resource-qos-config: |
  {
    "clusterStrategy": {
      "policies": {"cpuPolicy": "coreSched"},
      "lsClass": {"cpuQOS": {"enable": true, "coreExpeller": true}}  # LSE 继承 LS 配置
    }
  }
```

### 3. LSR (Latency Sensitive Reserved) QoS 类
#### 定义与用途
LSR 是延迟敏感且预留资源的 QoS 类，适用于需要预留部分资源以保证性能的敏感工作负载。它结合了资源预留和延迟优化，在资源超卖时优先保护预留部分。

- **资源管理行为**：预留资源（reservation），高优先级调度。内存回收控制在预留范围内。
- **差异**：比 LS 多一层资源预留机制，适合有明确预留需求的场景。
- **使用场景**：数据库、缓存服务（如 Redis），需要稳定预留资源的在线服务。

#### 配置方法
- Pod 标签：`koordinator.sh/qosClass: "LSR"`
- Memory QoS：设置 `wmarkMinAdj: -25` 延迟回收。
- 默认用于 Guaranteed Pod（如果未指定 QoS）。

#### 示例
```yaml
apiVersion: v1
kind: Pod
metadata:
  name: lsr-pod-example
  labels:
    koordinator.sh/qosClass: "LSR"  # 指定 LSR QoS
  annotations:
    koordinator.sh/memoryQOS: '{"policy": "auto", "wmarkMinAdj": -25}'  # 延迟内存回收
spec:
  containers:
  - name: database
    image: redis:latest
    resources:
      limits:
        cpu: "2"
        memory: "4Gi"
      requests:
        cpu: "2"
        memory: "4Gi"
```

### 4. LS (Latency Sensitive) QoS 类
#### 定义与用途
LS 是延迟敏感 QoS 类，适用于对延迟敏感但允许一定资源共享的工作负载。它确保 Pod 在 CPU 调度中优先访问资源，减少唤醒延迟，并在内存压力下延迟回收。

- **资源管理行为**：高优先级（groupIdentity: 2 或 schedIdle: 0），异步内存回收。保护 LS Pod 不受 BE Pod 干扰。
- **差异**：高于 BE，低于 LSE/LSR 的隔离级别。
- **使用场景**：Web 服务、实时应用（如 NGINX），混部环境中需要低延迟的在线工作负载。

#### 配置方法
- Pod 标签：`koordinator.sh/qosClass: "LS"`
- CPU QoS：启用 Group Identity 或 Core Scheduling。
- Memory QoS：启用 `enable: true`，设置保护参数。

#### 示例
```yaml
apiVersion: v1
kind: Pod
metadata:
  name: ls-pod-example
  labels:
    koordinator.sh/qosClass: "LS"  # 指定 LS QoS
spec:
  containers:
  - name: nginx
    image: nginx:latest
    resources:
      limits:
        cpu: "4"
        memory: "10Gi"
      requests:
        cpu: "4"
        memory: "10Gi"
```

ConfigMap 示例（CPU QoS）：
```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: slo-controller-config
  namespace: koordinator-system
data:
  resource-qos-config: |
    {
      "clusterStrategy": {
        "lsClass": {
          "cpuQOS": {"enable": true, "groupIdentity": 2}
        }
      }
    }
```

### 5. BE (Best Effort) QoS 类
#### 定义与用途
BE 是尽力而为 QoS 类，适用于非关键工作负载，没有资源保证。在资源短缺时，BE Pod 优先被回收或降级。

- **资源管理行为**：低优先级（groupIdentity: -1 或 schedIdle: 1），内存回收加速（wmarkMinAdj: 50）。
- **差异**：最低优先级，不影响高优先级 Pod。
- **使用场景**：批处理作业、压力测试、开发环境。

#### 配置方法
- Pod 标签：`koordinator.sh/qosClass: "BE"`
- CPU/Memory QoS：启用低优先级设置。

#### 示例
```yaml
apiVersion: v1
kind: Pod
metadata:
  name: be-pod-example
  labels:
    koordinator.sh/qosClass: "BE"  # 指定 BE QoS
spec:
  containers:
  - name: stress
    image: polinux/stress
    command: ["stress", "-c", "1"]
```

ConfigMap 示例（Memory QoS）：
```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: slo-controller-config
  namespace: koordinator-system
data:
  resource-qos-config: |
    {
      "clusterStrategy": {
        "beClass": {
          "memoryQOS": {"enable": true, "wmarkMinAdj": 50}
        }
      }
    }
```

## 安装与验证
- 安装 Koordinator：参考 [https://koordinator.sh/docs/installation](https://koordinator.sh/docs/installation)。
- 验证 QoS：使用 `cat /sys/fs/cgroup/...` 检查 cgroup 参数，或 Koordinator 指标。
- 注意：QoS 配置需启用相关 feature gate，并兼容 Kubernetes 版本 ≥1.18。

## 参考
- 官方文档：[https://koordinator.sh/docs/](https://koordinator.sh/docs/)
- GitHub：[https://github.com/koordinator-sh/koordinator](https://github.com/koordinator-sh/koordinator)
- 基于网站 [https://koordinator.sh/](https://koordinator.sh/) 整理。