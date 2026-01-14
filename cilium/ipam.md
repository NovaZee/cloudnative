# Cilium IPAM 详解

Cilium 作为 Kubernetes 网络插件，其 IP 地址管理（IPAM）部分负责为 Pod 分配唯一 IP 地址。理解 Cilium IPAM 是构建稳定、大规模集群网络的基石。

---

## 1. 什么是 IPAM？

IPAM 指的是 **IP Address Management（IP 地址管理）**。在 Kubernetes 网络模型中，每个 Pod 必须具有一个在集群内可路由的唯一 IP。IPAM 的核心职责是：

- 定义 Pod IP 地址池
- 为 Node 和 Pod 分配 IP 地址
- 管理地址分配生命周期（分配、回收、冲突避免）

---

## 2. Cilium 的 IPAM 主要模式

Cilium 支持多种 IPAM 模式，目前主要包括：

| 模式 | 含义 | 适用场景 |
|------|------|-----------|
| `cluster-pool` | Cilium 自管理集群 Pod IP 池 | 默认方案 |
| `kubernetes` | 使用 Kubernetes 控制器分配 Node.PodCIDR | 与 kube-controller-manager 协同 |
| `eni` | AWS ENI 资源分配 IP | AWS EKS 集成 |
| `azure` | Azure VNET IPAM | AKS |
| `alibabacloud` | 阿里云 VPC IPAM | ACK |
| `multi-pool` | 多地址池；可选择不同池分配 IP | 多网段/隔离需求 |

---

## 3. 默认 IPAM：cluster-pool

### 3.1 核心逻辑

`cluster-pool` 是 Cilium 的默认 IPAM 模式。在此模式下：

- Cilium Operator 维护一个 **全局集群 Pod CIDR 池**
- 每个 Node 从这个全局池中 **按固定掩码分配一个子网**
- Node 本地的 Cilium Agent 从该子网中分配 Pod IP

这个机制属于 **集群全局池 + 本地分配策略**，具有高效、去中心化、扩展友好等优势。

---

### 3.2 默认参数

| 参数名称 | 默认值 | 说明 |
|---------|--------|------|
| `cluster-pool-ipv4-cidr` | `10.0.0.0/8` | 全局 IPv4 地址池 |
| `cluster-pool-ipv4-mask-size` | `24` | 每个 Node 分配的 IPv4 子网掩码 |
| `ipam` | `cluster-pool` | IPAM 模式 |

---

### 3.3 分配流程

1. 集群启动时，Cilium Operator 构建全局 IP 池
2. Node 加入时，Operator 按掩码切分全局池
3. 为每个 Node 分配一个可用子网（如 `10.0.1.0/24`）
4. Node 上的 Cilium Agent：
    - 维护本地子网
    - 当 Pod 创建时，优先从本地子网分配 IP
    - Pod 删除时回收 IP

这种方式减少了跨组件交互，提升了性能与稳定性。

---

### 3.4 优点与缺点

**优点：**
- 与 Kubernetes 默认 CIDR 无依赖关系
- 支持自动扩容
- Pod 地址分配低延迟

**缺点：**
- 初始规划需要合理设计 IP 池
- 需要维护 Operator 高可用

---

## 4. kubernetes 模式

在 `kubernetes` 模式下：

- Cilium 不自行管理全局池
- 使用 Kubernetes 的 Node.PodCIDR 字段分配地址
- 由 kube-controller-manager 控制 PodCIDR 分配

适合 kubeadm 或 云厂商默认启用 NodeCIDR 的场景。

---

## 5. 云厂商专用 IPAM

### AWS ENI（`eni`）

在 AWS EKS 中，Pod IP 直接从 **AWS ENI（Elastic Network Interface）** 中分配。Cilium 通过 AWS API 获取 ENI 的可用 IP，并分发给 Pod。

---

### Azure（`azure`）

在 AKS 中集成 Azure VNET IP 资源，利用 Azure 的网络资源池进行 Pod IP 分配。

---

### 阿里云（`alibabacloud`）

在 ACK 中，Cilium 可以通过阿里云分配可用 IP 地址，集成云端网络资源管理。

---

## 6. 新特性：multi-pool

`multi-pool` 是一种更灵活的 IPAM 模式，允许：

- 定义多个 IP 地址池
- 按标签、节点、命名空间选择不同池
- 更好地支持多租户、隔离策略

这是面向复杂网络需求的增强方案。

---

## 7. 关键 CRD 与组件关系

- **CiliumNode**
    - 记录 Node 的分配结果
    - 包含已分配的 CIDR 子网信息

- **CiliumOperator**
    - 管理全局池
    - 响应 Node 加 / 删除

- **Cilium Agent**
    - 本地分配 Pod IP
    - 维护 IP 分配状态

---

## 8. 实战配置示例

### 8.1 ConfigMap 方式

```yaml
kind: ConfigMap
apiVersion: v1
metadata:
  name: cilium-config
  namespace: kube-system
data:
  ipam: "cluster-pool"
  cluster-pool-ipv4-cidr: "10.0.0.0/8"
  cluster-pool-ipv4-mask-size: "24"
