# Cilium IPAM 固定 Pod IP 代码走读文档

## 目录

- [一、概述](#一概述)
- [二、核心数据结构](#二核心数据结构)
- [三、代码文件组织](#三代码文件组织)
- [四、Pod 固定 IP 分配流程](#四pod-固定-ip-分配流程)
- [五、IP 回收机制](#五ip-回收机制)
- [六、CEP 守护机制](#六cep-守护机制)
- [七、子网分配器](#七子网分配器)
- [八、Cilium Agent IP 分配](#八cilium-agent-ip-分配)
- [九、配置参数](#九配置参数)
- [十、完整流程图](#十完整流程图)

---

## 一、概述

### 1.1 功能说明

固定 Pod IP 功能是为 Kubernetes Pod（特别是 StatefulSet）分配持久化 IP 地址的 IPAM 控制器扩展。当 Pod 重启或重新调度时，它将获得相同的 IP 地址。

### 1.2 核心特性

| 特性 | 说明 |
|------|------|
| 固定 IP 绑定 | 通过 `Owner` 字段将 IP 与 Pod 绑定 |
| 自动回收 | 当上层资源删除时自动回收 IP |
| 子网管理 | 支持动态子网分配，避免 IP 段重叠 |
| CEP 守护 | 自动处理 CiliumEndpoint 残留问题 |
| 高可用 | 支持 Leader 选举，多实例部署 |
| IPv4/IPv6 | 双栈支持 |
| 孤儿 IP 清理 | Pod 跨节点调度时自动清理旧 IP |

### 1.3 架构组件

```
┌─────────────────────────────────────────────────────────────────┐
│                        Kubernetes Cluster                      │
└─────────────────────────────────────────────────────────────────┘
                              │
        ┌─────────────────────┼─────────────────────┐
        │                     │                     │
        ▼                     ▼                     ▼
┌──────────────┐    ┌──────────────┐    ┌──────────────┐
│  Pod (业务)  │    │  Cilium CNI  │    │ Cilium Agent │
└──────────────┘    └──────────────┘    └──────────────┘
        │                     │                     │
        │                     ▼                     ▼
        │            ┌──────────────┐    ┌──────────────┐
        │            │  CiliumNode  │◄───│  IPAM Module │
        │            │     CRD      │    └──────────────┘
        │            └──────────────┘              │
        │                     │                     │
        ▼                     ▼                     ▼
┌─────────────────────────────────────────────────────────────────┐
│                     IPAM Controller (本实现)                    │
│  - Pod 事件监听                                                 │
│  - 固定 IP 标记                                                 │
│  - IP 池管理                                                    │
│  - 资源回收                                                     │
│  - CEP 守护                                                     │
└─────────────────────────────────────────────────────────────────┘
```

---

## 二、核心数据结构

### 2.1 AllocationIP (`pkg/ipam/types/types.go:30-49`)

```go
// AllocationIP 是一个可用于分配或已分配的 IP
type AllocationIP struct {
    // Owner 是 IP 的所有者。当 IP 被分配时设置此字段。
    // 格式: "namespace/podName"
    //
    // 当 Owner 为空时，表示 IP 未被分配，可被任意 Pod 使用
    // 当 Owner 非空时，表示 IP 已被固定分配给指定 Pod
    //
    // +optional
    Owner string `json:"owner,omitempty"`

    // Resource 表示 IP 关联的资源
    // 格式: "namespace/Kind/name"
    // 例如: "default/StatefulSet/mysql-cluster"
    // 用于在上层资源删除时自动回收 IP
    //
    // +optional
    Resource string `json:"resource,omitempty"`
}
```

**字段语义说明：**

| 字段 | 值为空 | 值非空 |
|------|--------|--------|
| `Owner` | IP 未被分配，可被任意 Pod 使用 | IP 已固定分配给指定 Pod |
| `Resource` | 无上层资源关联 | 关联的上层资源（用于 IP 回收判断） |

### 2.2 IPAMSpec (`pkg/ipam/types/types.go:54-124`)

```go
// IPAMSpec 是节点的 IPAM 规范
type IPAMSpec struct {
    // Pool 是节点可用于分配的 IP 列表
    // 当 IP 被使用时，IP 将保留在此列表中，但会被添加到 Status.IPAM.Used
    //
    // 格式: map[string]AllocationIP
    // Key: IP 地址字符串
    // Value: AllocationIP 结构
    Pool AllocationMap `json:"pool,omitempty"`

    // PodCIDRs 是节点可用于分配的 CIDR 列表
    PodCIDRs []string `json:"podCIDRs,omitempty"`

    // MinAllocate 是节点首次启动时必须分配的最小 IP 数
    MinAllocate int `json:"min-allocate,omitempty"`

    // MaxAllocate 是可分配给节点的最大 IP 数
    MaxAllocate int `json:"max-allocate,omitempty"`

    // PreAllocate 定义了 IPAM 规范中必须可用的 IP 地址数量
    PreAllocate int `json:"pre-allocate,omitempty"`

    // MaxAboveWatermark 是超过预分配水位线的最大地址数
    MaxAboveWatermark int `json:"max-above-watermark,omitempty"`
}
```

**CiliumNode.Spec.IPAM.Pool 示例：**

```yaml
spec:
  ipam:
    pool:
      "10.240.1.10":
        owner: "default/mysql-cluster-0"
        resource: "default/StatefulSet/mysql-cluster"
      "10.240.1.11":
        owner: "default/mysql-cluster-1"
        resource: "default/StatefulSet/mysql-cluster"
      "10.240.1.12":           # 未分配的 IP
        {}
      "10.240.1.13":
        owner: "default/mysql-cluster-2"
        resource: "default/StatefulSet/mysql-cluster"
    podCIDRs:
    - 10.240.1.0/24
```

### 2.3 IPAMStatus (`pkg/ipam/types/types.go:131-160`)

```go
// IPAMStatus 是节点的 IPAM 状态
type IPAMStatus struct {
    // Used 列出 Spec.IPAM.Pool 中已分配并正在使用的所有 IP
    Used AllocationMap `json:"used,omitempty"`

    // PodCIDRs 列出分配给此节点的每个 pod CIDR 的状态
    PodCIDRs PodCIDRMap `json:"pod-cidrs,omitempty"`

    // Operator 是节点的 Operator 状态
    OperatorStatus OperatorStatus `json:"operator-status,omitempty"`

    // ReleaseIPs 跟踪每个考虑释放的 IP 的状态
    ReleaseIPs map[string]IPReleaseStatus `json:"release-ips,omitempty"`
}
```

---

## 三、代码文件组织

### 3.1 目录结构

```
ipam/
├── main.go                 # 主入口、Leader 选举、事件循环
├── pod.go                  # Pod 固定 IP 分配核心逻辑
├── cep.go                  # CiliumEndpoint 守护
├── subnet_allocator.go     # 子网分配器
├── podsynchronizer.go      # Pod MAC 同步
├── inject.go               # Admission Webhook
├── ipam_family.go          # IP 地址族工具
├── CLAUDE.md               # Claude Code 指南
├── FIXED_PODIP_GUIDE.md    # 功能指南
└── ReadMe.md               # 设计文档

pkg/ipam/
├── types/
│   └── types.go            # 核心数据结构定义
├── crd.go                  # CRD 模式 IPAM 实现
├── allocator.go            # IP 分配器接口
└── ...
```

### 3.2 核心文件功能说明

| 文件 | 行数 | 功能 |
|------|------|------|
| `main.go` | 622 | 主入口、Leader 选举、三个 WorkQueue 事件处理 |
| `pod.go` | 220 | Pod 固定 IP 分配、孤儿 IP 清理 |
| `cep.go` | 232 | CiliumEndpoint 守护、残留清理 |
| `subnet_allocator.go` | 348 | 子网分配、重叠检测 |
| `podsynchronizer.go` | - | MAC 地址同步 |
| `pkg/ipam/crd.go` | 700+ | Cilium Agent IP 分配逻辑 |
| `pkg/ipam/types/types.go` | 485 | 数据结构定义 |

---

## 四、Pod 固定 IP 分配流程

### 4.1 入口函数：DoPodAddHandle (`ipam/pod.go:25-131`)

这是处理 Pod 添加/更新事件的核心函数。

**函数签名：**
```go
func DoPodAddHandle(
    ciliumClientset *versioned.Clientset,
    pod *v1.Pod,
    selector labels.Selector,
    excludePrefixes []string,
    isAdd bool,
) error
```

**处理流程：**

```
┌─────────────────────────────────────────────────────────────────────┐
│                      DoPodAddHandle()                              │
├─────────────────────────────────────────────────────────────────────┤
│ 1. 前置检查                                                         │
│    ├── nodeName == "" ? → 返回                                      │
│    ├── podIP == "" ? → 返回                                         │
│    └── 构建 key: "namespace/podName"                               │
│                                                                     │
│ 2. 标签选择器匹配                                                    │
│    ├── selector.Matches(pod.Labels)                                 │
│    └── 不匹配 → 返回                                                │
│                                                                     │
│ 3. DBBranch 标签排除检查                                             │
│    ├── 检查 pod.Labels[dbBranchLabelKey]                           │
│    ├── 值是否有前缀在 excludePrefixes 中?                           │
│    └── 匹配排除前缀 → 跳过                                           │
│                                                                     │
│ 4. 孤儿 IP 清理（仅 Add 事件）                                       │
│    └── CleanupOrphanedIPAllocations(key, nodeName)                 │
│                                                                     │
│ 5. 解析 Pod IP                                                      │
│    ├── 遍历 pod.Status.PodIPs                                      │
│    ├── IPv4 → podIPv4                                              │
│    └── IPv6 → podIPv6                                              │
│                                                                     │
│ 6. 获取 CiliumNode                                                  │
│    └── ciliumNodeLister.Get(nodeName)                             │
│                                                                     │
│ 7. 构建 Resource 字段                                               │
│    └── "namespace/Kind/name" (从 OwnerReferences 获取)             │
│                                                                     │
│ 8. 标记固定 IP                                                      │
│    ├── IPv4: ipOwnerChanged() → FlagIpForPod()                     │
│    └── IPv6: ipOwnerChanged() → FlagIpForPod()                     │
│                                                                     │
│ 9. 更新 CiliumNode                                                  │
│    └── ciliumClientset.CiliumV2().CiliumNodes().Update()          │
└─────────────────────────────────────────────────────────────────────┘
```

**关键代码解析：**

```go
// 1. 前置检查
nodeName := pod.Spec.NodeName
if nodeName == "" || pod.Status.PodIP == "" {
    return nil  // Pod 未调度或无 IP，跳过
}
key := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)

// 2. 标签选择器匹配
if !selector.Matches(labels.Set(pod.Labels)) {
    return nil  // 不匹配选择器，跳过
}

// 3. DBBranch 标签排除
if len(excludePrefixes) > 0 {
    if dbBranchValue, exists := pod.Labels[dbBranchLabelKey]; exists {
        for _, prefix := range excludePrefixes {
            if prefix != "" && strings.HasPrefix(dbBranchValue, prefix) {
                return nil  // 匹配排除前缀，跳过
            }
        }
    }
}

// 4. 孤儿 IP 清理（仅 Add 事件）
if isAdd {
    CleanupOrphanedIPAllocations(ciliumClientset, key, nodeName)
}

// 5. 解析 Pod IP（双栈支持）
var podIPv4, podIPv6 net.IP
for _, podIp := range pod.Status.PodIPs {
    ip := net.ParseIP(podIp.IP)
    if ip == nil {
        continue
    }
    if DeriveFamily(ip) == IPv4 {
        podIPv4 = ip
    } else {
        podIPv6 = ip
    }
}

// 兼容 PodIPs 字段为空的情况
if (podIPv4 == nil || podIPv6 == nil) && pod.Status.PodIP != "" {
    podIP := net.ParseIP(pod.Status.PodIP)
    if DeriveFamily(podIP) == IPv4 {
        podIPv4 = podIP
    } else {
        podIPv6 = podIP
    }
}

// 6. 获取 CiliumNode
cn, err := ciliumNodeLister.Get(nodeName)
if err != nil {
    return nil
}

// 7. 构建 Resource 字段
podOwner := ""
if pod.OwnerReferences != nil && len(pod.OwnerReferences) > 0 {
    podOwner = fmt.Sprintf("%s/%s/%s",
        pod.Namespace,
        pod.OwnerReferences[0].Kind,
        pod.OwnerReferences[0].Name)
}

// 8. 标记固定 IP（检查是否需要更新）
var updatedCN *v2.CiliumNode
if podIPv4 != nil {
    if ipOwnerChanged(cn, podOwner, key, podIPv4.String()) {
        updatedCN = FlagIpForPod(cn.DeepCopy(), key, podOwner, podIPv4.String())
    }
}

if *enableStaticPodIPv6 && podIPv6 != nil {
    if ipOwnerChanged(cn, podOwner, key, podIPv6.String()) {
        if updatedCN == nil {
            updatedCN = cn.DeepCopy()
        }
        updatedCN = FlagIpForPod(updatedCN, key, podOwner, podIPv6.String())
    }
}

// 9. 更新 CiliumNode
if updatedCN != nil {
    ctx, cancel := context.WithTimeout(context.TODO(), time.Second*20)
    defer cancel()
    _, err = ciliumClientset.CiliumV2().CiliumNodes().Update(ctx, updatedCN, metav1.UpdateOptions{})
}
```

### 4.2 FlagIpForPod (`ipam/pod.go:133-142`)

标记固定 IP 的核心函数。

**函数签名：**
```go
func FlagIpForPod(cn *v2.CiliumNode, owner, resource, ipInfo string) *v2.CiliumNode
```

**实现：**
```go
func FlagIpForPod(cn *v2.CiliumNode, owner, resource, ipInfo string) *v2.CiliumNode {
    // 初始化 Pool（如果为空）
    if cn.Spec.IPAM.Pool == nil {
        cn.Spec.IPAM.Pool = make(map[string]types.AllocationIP, 0)
    }
    // 设置 IP 的 Owner 和 Resource
    cn.Spec.IPAM.Pool[ipInfo] = types.AllocationIP{
        Owner:    owner,    // "namespace/podName"
        Resource: resource, // "namespace/Kind/name"
    }
    return cn
}
```

**效果：**
```yaml
# 更新前
spec:
  ipam:
    pool:
      "10.240.1.10": {}

# 更新后
spec:
  ipam:
    pool:
      "10.240.1.10":
        owner: "default/mysql-cluster-0"
        resource: "default/StatefulSet/mysql-cluster"
```

### 4.3 ipOwnerChanged (`ipam/pod.go:144-146`)

检查 IP Owner 是否变化。

```go
func ipOwnerChanged(cn *v2.CiliumNode, owner, resource, ip string) bool {
    return cn.Spec.IPAM.Pool[ip].Owner != owner ||
           cn.Spec.IPAM.Pool[ip].Resource != resource
}
```

### 4.4 CleanupOrphanedIPAllocations (`ipam/pod.go:174-219`)

清理孤儿 IP：当 Pod 调度到不同节点时，清理旧节点上的 IP 分配。

**函数签名：**
```go
func CleanupOrphanedIPAllocations(
    ciliumClientset *versioned.Clientset,
    podKey string,           // "namespace/podName"
    currentNodeName string,  // 当前节点名称
) error
```

**处理流程：**

```
┌─────────────────────────────────────────────────────────────────────┐
│              CleanupOrphanedIPAllocations()                        │
├─────────────────────────────────────────────────────────────────────┤
│ 1. 获取所有 CiliumNode                                              │
│    └── ciliumNodeLister.List(labels.Everything())                 │
│                                                                     │
│ 2. 遍历每个 CiliumNode                                              │
│    ├── 跳过当前节点                                                  │
│    └── 遍历 cn.Spec.IPAM.Pool                                      │
│                                                                     │
│ 3. 检查 IP Owner                                                    │
│    └── alloc.Owner == podKey ?                                     │
│                                                                     │
│ 4. 清理孤儿 IP                                                      │
│    ├── 设置 Pool[ip] = AllocationIP{} (清空 Owner)                 │
│    └── ciliumClientset.CiliumV2().CiliumNodes().Update()          │
└─────────────────────────────────────────────────────────────────────┘
```

**关键代码：**
```go
func CleanupOrphanedIPAllocations(ciliumClientset *versioned.Clientset, podKey, currentNodeName string) error {
    // 1. 获取所有 CiliumNode
    cnList, err := ciliumNodeLister.List(labels.Everything())
    if err != nil {
        return fmt.Errorf("failed to list CiliumNodes: %v", err)
    }

    // 2. 遍历每个 CiliumNode
    for _, cn := range cnList {
        if cn.Name == currentNodeName {
            continue  // 跳过当前节点
        }

        if cn.Spec.IPAM.Pool == nil {
            continue
        }

        needsUpdate := false
        updatedCN := cn.DeepCopy()

        // 3. 检查 IP Owner
        for ip, alloc := range updatedCN.Spec.IPAM.Pool {
            if alloc.Owner == podKey {
                l.Infof("found orphaned IP %s on node %s, releasing", ip, cn.Name)
                // 4. 清理孤儿 IP
                updatedCN.Spec.IPAM.Pool[ip] = types.AllocationIP{}
                needsUpdate = true
            }
        }

        if !needsUpdate {
            continue
        }

        // 更新 CiliumNode
        ctx, cancel := context.WithTimeout(context.TODO(), time.Second*30)
        defer cancel()
        _, err := ciliumClientset.CiliumV2().CiliumNodes().Update(ctx, updatedCN, metav1.UpdateOptions{})
        if err != nil {
            l.Errorf("failed to update CiliumNode %s: %v", cn.Name, err)
            continue
        }
        l.Infof("successfully released orphaned IP on node %s", cn.Name)
    }

    return nil
}
```

**场景示例：**
```
初始状态：
- Pod mysql-cluster-0 在 node-1 上，IP 为 10.240.1.10
- node-1.Spec.IPAM.Pool["10.240.1.10"].Owner = "default/mysql-cluster-0"

Pod 被重新调度到 node-2：
1. Pod 在 node-2 上启动，获得新 IP 10.240.2.10
2. CleanupOrphanedIPAllocations() 被调用
3. 发现 node-1 上有孤儿 IP 10.240.1.10
4. 清空 node-1.Spec.IPAM.Pool["10.240.1.10"].Owner
5. 10.240.1.10 变为可用 IP

最终状态：
- node-1.Spec.IPAM.Pool["10.240.1.10"].Owner = "" (可用)
- node-2.Spec.IPAM.Pool["10.240.2.10"].Owner = "default/mysql-cluster-0"
```

---

## 五、IP 回收机制

### 5.1 DoOwnerIpRecycle (`ipam/main.go:553-606`)

回收已删除资源的固定 IP。

**处理流程：**

```
┌─────────────────────────────────────────────────────────────────────┐
│                     DoOwnerIpRecycle()                             │
├─────────────────────────────────────────────────────────────────────┤
│ 遍历 cn.Spec.IPAM.Pool:                                             │
│                                                                     │
│ 1. 跳过未分配的 IP                                                  │
│    └── v.Owner == "" ? → continue                                  │
│                                                                     │
│ 2. 解析 Resource 字段                                               │
│    └── rs = strings.Split(v.Resource, "/")                         │
│       rs[0] = namespace                                            │
│       rs[1] = Kind (如 "StatefulSet")                              │
│       rs[2] = name                                                 │
│                                                                     │
│ 3. 检查 Pod 状态                                                    │
│    ├── 解析 Owner: "namespace/podName"                             │
│    ├── podLister.Pods(namespace).Get(podName)                     │
│    └── Pod 存在且在不同节点? → needRecycle = true                  │
│                                                                     │
│ 4. 检查资源存在性（仅当 Pod 不存在时）                               │
│    └── switch rs[1]:                                               │
│        case "StatefulSet":                                         │
│            stsLister.StatefulSets(rs[0]).Get(rs[2])               │
│            IsNotFound? → needRecycle = true                        │
│                                                                     │
│ 5. 执行回收                                                         │
│    └── cn.Spec.IPAM.Pool[ip] = AllocationIP{}                     │
│       (清空 Owner 和 Resource 字段)                                │
└─────────────────────────────────────────────────────────────────────┘
```

**关键代码：**
```go
func DoOwnerIpRecycle(l *logrus.Entry, cn *v2.CiliumNode) (needUpdate bool) {
    for k, v := range cn.Spec.IPAM.Pool {
        // 1. 跳过未分配的 IP
        if v.Owner == "" {
            continue
        }

        l = l.WithField("ip", k).WithField("owner", v.Owner).WithField("resource", v.Resource)

        // 2. 解析 Resource 字段
        rs := strings.Split(v.Resource, "/")
        if len(rs) != 3 {
            l.Warningf("skip invalid resource ip recycle check.")
            continue
        }
        needRecycle := false

        // 3. 检查 Pod 状态
        ownerParts := strings.Split(v.Owner, "/")
        podFound := false
        if len(ownerParts) == 2 {
            pod, err := podLister.Pods(ownerParts[0]).Get(ownerParts[1])
            if err == nil {
                podFound = true
                // Pod 在不同节点上，需要回收
                if pod.Spec.NodeName != "" && pod.Spec.NodeName != cn.Name {
                    l.Infof("pod exists on different node %s, recycling IP", pod.Spec.NodeName)
                    needRecycle = true
                }
            }
        }

        // 4. 检查资源存在性（仅当 Pod 不存在时）
        if !needRecycle && !podFound {
            switch rs[1] {
            case "StatefulSet":
                _, err := stsLister.StatefulSets(rs[0]).Get(rs[2])
                if errors.IsNotFound(err) {
                    needRecycle = true
                } else if err != nil {
                    l.Errorf("get sts info failed %v.", err)
                    continue
                }
            default:
                l.Warningf("skip unsupported resource.")
            }
        }

        // 5. 执行回收
        if !needRecycle {
            continue
        }
        l.Infof("owner is not exists or moved to another node, do recycle.")
        cn.Spec.IPAM.Pool[k] = types.AllocationIP{}  // 清空 Owner 和 Resource
        needUpdate = true
    }
    return
}
```

**场景示例：**

**场景一：StatefulSet 删除**
```
初始状态：
- StatefulSet mysql-cluster 存在
- mysql-cluster-0 的 IP: 10.240.1.10
- node-1.Spec.IPAM.Pool["10.240.1.10"] = {
    Owner: "default/mysql-cluster-0",
    Resource: "default/StatefulSet/mysql-cluster"
  }

删除 StatefulSet：
1. kubectl delete statefulset mysql-cluster
2. DoOwnerIpRecycle() 检测到 StatefulSet 不存在
3. 清空 Owner 和 Resource

最终状态：
- node-1.Spec.IPAM.Pool["10.240.1.10"] = {} (IP 可被重新分配)
```

**场景二：Pod 调度到不同节点**
```
初始状态：
- Pod mysql-cluster-0 在 node-1 上
- node-1.Spec.IPAM.Pool["10.240.1.10"] = {
    Owner: "default/mysql-cluster-0",
    Resource: "default/StatefulSet/mysql-cluster"
  }

Pod 调度到 node-2：
1. Pod 在 node-2 上启动，获得新 IP
2. DoOwnerIpRecycle() 在 node-1 上检测到 Pod 在不同节点
3. 清空 node-1 上的 Owner

最终状态：
- node-1.Spec.IPAM.Pool["10.240.1.10"] = {} (旧 IP 可被重新分配)
- node-2.Spec.IPAM.Pool["10.240.2.10"] = {
    Owner: "default/mysql-cluster-0",
    Resource: "default/StatefulSet/mysql-cluster"
  }
```

---

## 六、CEP 守护机制

### 6.1 概述

CiliumEndpoint (CEP) 是 Cilium 中用于记录 Pod 网络信息的 CRD。CEP 守护机制确保：

1. CEP 不存在时自动重建
2. CEP 删除中超时处理
3. CEP 与 Pod IP 一致性检查

### 6.2 KeepPodCepWithContext (`ipam/cep.go:25-97`)

CEP 守护的主循环。

**处理流程：**

```
┌─────────────────────────────────────────────────────────────────────┐
│                 KeepPodCepWithContext()                            │
├─────────────────────────────────────────────────────────────────────┤
│ 定时器：每 cepTerminationTimeout/2 触发一次                          │
│                                                                     │
│ 1. 扫描所有 CEP                                                      │
│    └── cepLister.List(labels.Everything())                        │
│                                                                     │
│ 2. 处理删除中的 CEP                                                  │
│    └── cep.DeletionTimestamp != nil ?                              │
│        └── DoCepKeeper(..., isDeleting=true, ...)                 │
│                                                                     │
│ 3. 处理队列事件                                                      │
│    └── cepQueue.Get():                                             │
│        ├── 获取 CEP                                                │
│        ├── isNotFound = (CEP 不存在)                               │
│        ├── isDeleting = (CEP 删除中)                               │
│        └── DoCepKeeper(namespace, name, isNotFound, isDeleting)   │
└─────────────────────────────────────────────────────────────────────┘
```

**关键代码：**
```go
func KeepPodCepWithContext(ctx context.Context, lp *logrus.Entry, queue workqueue.RateLimitingInterface) {
    tick := time.NewTicker(*cepTerminationTimeout / 2)  // 默认 15 分钟
    defer tick.Stop()

    for {
        select {
        case <-ctx.Done():
            queue.ShutDown()
            return
        case <-tick.C:
            // 定时扫描删除中的 CEP
            cepList, err := cepLister.List(labels.Everything())
            if err != nil {
                continue
            }
            for _, cep := range cepList {
                if cep.DeletionTimestamp == nil {
                    continue
                }
                err = DoCepKeeper(l, cep.Namespace, cep.Name, false, true, cep)
                if err != nil {
                    l.Errorf("DoCepKeeper failed: %v", err)
                }
            }
        default:
            // 处理队列事件
            key, quit := queue.Get()
            if quit {
                return
            }
            ns, name, err := cache.SplitMetaNamespaceKey(key.(string))
            if err != nil {
                queue.Done(key)
                continue
            }

            isNotFound := false
            cep, err := cepLister.CiliumEndpoints(ns).Get(name)
            if err != nil {
                if errors.IsNotFound(err) {
                    isNotFound = true
                } else {
                    queue.Done(key)
                    continue
                }
            }

            isDeleting := (cep != nil && cep.DeletionTimestamp != nil)
            err = DoCepKeeper(l, ns, name, isNotFound, isDeleting, cep)

            if err != nil {
                queue.AddRateLimited(key)
            } else {
                queue.Forget(key)
            }
            queue.Done(key)
        }
    }
}
```

### 6.3 DoCepKeeper (`ipam/cep.go:99-183`)

CEP 守护的核心处理函数。

**处理场景：**

#### 场景一：CEP 不存在 (`isNotFound == true`)

```
┌─────────────────────────────────────────────────────────────────────┐
│ CEP 不存在 → 触发 CEP 重建                                          │
├─────────────────────────────────────────────────────────────────────┤
│ 1. 获取 Pod                                                         │
│    └── podLister.Pods(namespace).Get(name)                        │
│                                                                     │
│ 2. 检查 Pod 状态                                                    │
│    ├── pod.Spec.NodeName == "" ? → 跳过                             │
│    ├── pod.Status.PodIP == "" ? → 跳过                              │
│    ├── pod.Status.Phase != PodRunning ? → 跳过                      │
│    └── pod.Spec.HostNetwork == true ? → 跳过                        │
│                                                                     │
│ 3. 检查 cepTriggerTime 防止重入                                     │
│    └── lastTime.Add(5s).After(time.Now()) ? → 跳过                 │
│                                                                     │
│ 4. Patch Pod Label 触发 CEP 重建                                    │
│    └── PatchPodLabel(pod)                                          │
│       设置 metadata.labels.cepTriggerTime = now.Unix()             │
└─────────────────────────────────────────────────────────────────────┘
```

**关键代码：**
```go
if isNotFound {
    pod, err := podLister.Pods(namespace).Get(name)
    if err != nil {
        if errors.IsNotFound(err) {
            return nil  // Pod 不存在，等待回收
        }
        return fmt.Errorf("get pod failed: %v", err)
    }

    // 检查 Pod 状态
    if pod.Spec.NodeName == "" || pod.Status.PodIP == "" {
        return nil  // 等待 Pod 初始化
    }
    if pod.Status.Phase != v1.PodRunning || pod.Spec.HostNetwork {
        return nil  // 不需要 CEP
    }

    // 防止重入
    if pod.Labels != nil && pod.Labels["cepTriggerTime"] != "" {
        ti, err := strconv.ParseInt(pod.Labels["cepTriggerTime"], 10, 64)
        if err == nil {
            lastTime := time.Unix(ti, 0)
            if lastTime.Add(time.Second * 5).After(time.Now()) {
                return nil  // 自身更新导致的重入，忽略
            }
        }
    }

    // Patch Pod Label 触发 CEP 重建
    err = PatchPodLabel(pod)
    if err != nil {
        return fmt.Errorf("create missing cep failed: %v", err)
    }
    return nil
}
```

#### 场景二：CEP 删除中 (`isDeleting == true`)

```
┌─────────────────────────────────────────────────────────────────────┐
│ CEP 删除中 → 检查超时或 IP 不一致                                   │
├─────────────────────────────────────────────────────────────────────┤
│ 1. 获取 Pod                                                         │
│    └── podLister.Pods(namespace).Get(name)                        │
│                                                                     │
│ 2. Pod 不存在 → 检查超时                                            │
│    └── cep.DeletionTimestamp.Add(cepTerminationTimeout)            │
│       .Before(time.Now()) ?                                         │
│       → recycleCep(cep)                                            │
│                                                                     │
│ 3. Pod 存在 → 检查 IP 一致性                                        │
│    └── checkIfIsOk(cep, pod)                                       │
│       ├── cep.Status.Networking.NodeIP != pod.Status.HostIP ?      │
│       ├── cep.Status.Networking.Addressing[0].IPV4 !=              │
│       │     pod.Status.PodIP ?                                     │
│       └── 不一致 → recycleCep(cep)                                 │
└─────────────────────────────────────────────────────────────────────┘
```

**关键代码：**
```go
if isDeleting {
    pod, err := podLister.Pods(namespace).Get(name)
    if err != nil {
        if errors.IsNotFound(err) {
            // Pod 不存在，检查超时
            if cep.DeletionTimestamp.Add(*cepTerminationTimeout).Before(time.Now()) {
                l.Infof("recycle outdated cep.")
                err = recycleCep(cep)
                if err != nil {
                    return fmt.Errorf("recycle failed: %v", err)
                }
            }
            return nil
        }
        return fmt.Errorf("get pod failed: %v", err)
    }

    if pod.Status.PodIP == "" {
        return nil
    }

    // 检查 CEP 与 Pod IP 一致性
    ok, msg := checkIfIsOk(cep, pod)
    if ok {
        return nil
    }

    // IP 不一致，强制回收 CEP
    l.Errorf("check terminating cep with %s", msg)
    err = recycleCep(cep)
    if err != nil {
        return fmt.Errorf("recycle failed: %v", err)
    }
    return nil
}
```

### 6.4 checkIfIsOk (`ipam/cep.go:198-215`)

检查 CEP 与 Pod 的 IP 一致性。

```go
func checkIfIsOk(cep *cilium_v2.CiliumEndpoint, pod *v1.Pod) (bool, string) {
    // 1. 检查 Networking 字段
    if cep.Status.Networking == nil {
        return false, fmt.Sprintf("pod %s/%s with cep no networking.",
            pod.Namespace, pod.Name)
    }

    // 2. 检查 NodeIP
    if cep.Status.Networking.NodeIP != pod.Status.HostIP {
        return false, fmt.Sprintf("pod %s/%s with cep diff nodeip %s/%s.",
            pod.Namespace, pod.Name,
            cep.Status.Networking.NodeIP, pod.Status.HostIP)
    }

    // 3. 检查 Addressing
    if len(cep.Status.Networking.Addressing) != 1 {
        return false, fmt.Sprintf("cep %s/%s has invalid address info %#v.",
            pod.Namespace, pod.Name, cep.Status.Networking.Addressing)
    }

    // 4. 检查 IPV4
    if cep.Status.Networking.Addressing[0].IPV4 != pod.Status.PodIP {
        return false, fmt.Sprintf("cep %s/%s had different ip cep:%s, pod:%s.",
            pod.Namespace, pod.Name,
            cep.Status.Networking.Addressing[0].IPV4, pod.Status.PodIP)
    }

    return true, ""
}
```

### 6.5 recycleCep (`ipam/cep.go:217-226`)

强制回收 CEP（移除 Finalizers）。

```go
func recycleCep(cep *cilium_v2.CiliumEndpoint) error {
    newCep := cep.DeepCopy()
    newCep.Finalizers = []string{}  // 移除所有 Finalizers
    _, err := ciliumClientset.CiliumV2().CiliumEndpoints(cep.Namespace).
        Update(GetCtx(), newCep, metav1.UpdateOptions{})
    if err != nil {
        return fmt.Errorf("recycle cep update failed: %v", err)
    }
    return nil
}
```

### 6.6 PatchPodLabel (`ipam/cep.go:186-196`)

通过 Patch Pod Label 触发 CEP 重建。

```go
func PatchPodLabel(pod *v1.Pod) error {
    ctx, _ := context.WithTimeout(context.TODO(), time.Second*10)

    _, err := clientset.CoreV1().Pods(pod.Namespace).Patch(ctx, pod.Name,
        types.MergePatchType,
        []byte(fmt.Sprintf(`{"metadata": {"labels": {"cepTriggerTime": "%d"} } }`,
            time.Now().Unix())),
        metav1.PatchOptions{})
    if err != nil {
        return fmt.Errorf("patch pod label failed: %v", err)
    }
    return nil
}
```

---

## 七、子网分配器

### 7.1 SubnetAllocator 结构 (`ipam/subnet_allocator.go:18-30`)

```go
type SubnetAllocator struct {
    ipv4Pool     *net.IPNet      // IPv4 地址池
    ipv4MaskSize int             // IPv4 子网掩码大小
    ipv6Pool     *net.IPNet      // IPv6 地址池
    ipv6MaskSize int             // IPv6 子网掩码大小

    // 跟踪已分配的子网以防止重复
    allocatedIPv4 map[string]bool
    allocatedIPv6 map[string]bool
    mu            sync.Mutex

    log *logrus.Entry
}
```

### 7.2 初始化流程

```
┌─────────────────────────────────────────────────────────────────────┐
│                    SubnetAllocator 初始化                           │
├─────────────────────────────────────────────────────────────────────┤
│ 1. NewSubnetAllocator()                                             │
│    ├── 解析 IPv4 CIDR 和掩码大小                                     │
│    ├── 解析 IPv6 CIDR 和掩码大小                                     │
│    └── 初始化 allocatedIPv4/allocatedIPv6 map                      │
│                                                                     │
│ 2. InitWithCiliumNodes()                                           │
│    ├── 获取所有 CiliumNode                                          │
│    └── 调用 LoadExistingAllocations()                              │
│                                                                     │
│ 3. LoadExistingAllocations()                                       │
│    ├── 遍历所有节点的 PodCIDRs                                      │
│    ├── 检查 CIDR 是否在配置的池中                                    │
│    └── 将已分配的 CIDR 添加到 allocated map                        │
└─────────────────────────────────────────────────────────────────────┘
```

**关键代码：**
```go
func NewSubnetAllocator(ipv4CIDR, ipv4MaskSize, ipv6CIDR, ipv6MaskSize string) (*SubnetAllocator, error) {
    sa := &SubnetAllocator{
        allocatedIPv4: make(map[string]bool),
        allocatedIPv6: make(map[string]bool),
        log:           mlog.WithField("component", "subnet-allocator"),
    }

    // 解析 IPv4 配置
    if ipv4CIDR != "" {
        _, ipnet, err := net.ParseCIDR(ipv4CIDR)
        if err != nil {
            return nil, fmt.Errorf("invalid IPv4 CIDR: %v", err)
        }
        sa.ipv4Pool = ipnet

        maskSize, err := strconv.Atoi(ipv4MaskSize)
        if err != nil {
            return nil, fmt.Errorf("invalid IPv4 mask size: %v", err)
        }
        sa.ipv4MaskSize = maskSize
    }

    // 解析 IPv6 配置（类似）
    // ...

    return sa, nil
}

func (sa *SubnetAllocator) LoadExistingAllocations(nodes []*v2.CiliumNode) {
    sa.mu.Lock()
    defer sa.mu.Unlock()

    for _, node := range nodes {
        for _, cidr := range node.Spec.IPAM.PodCIDRs {
            ip, _, err := net.ParseCIDR(cidr)
            if err != nil {
                continue
            }

            // 只跟踪在池中的子网
            if ip.To4() != nil && sa.ipv4Pool != nil {
                if sa.ipv4Pool.Contains(ip) {
                    sa.allocatedIPv4[cidr] = true
                }
            } else if ip.To4() == nil && sa.ipv6Pool != nil {
                if sa.ipv6Pool.Contains(ip) {
                    sa.allocatedIPv6[cidr] = true
                }
            }
        }
    }
}
```

### 7.3 分配子网 (`ipam/subnet_allocator.go:148-182`)

```go
func (sa *SubnetAllocator) AllocateSubnets(node *v2.CiliumNode) ([]string, error) {
    sa.mu.Lock()
    defer sa.mu.Unlock()

    var newCIDRs []string

    // 分配 IPv4 子网
    if sa.ipv4Pool != nil && !sa.nodeHasFamilyCIDR(node, IPv4) {
        cidr, err := sa.allocateNextIPv4Subnet()
        if err != nil {
            return nil, fmt.Errorf("failed to allocate IPv4 subnet: %v", err)
        }
        if cidr != "" {
            newCIDRs = append(newCIDRs, cidr)
            sa.allocatedIPv4[cidr] = true
        }
    }

    // 分配 IPv6 子网
    if sa.ipv6Pool != nil && *enableIPv6 && !sa.nodeHasFamilyCIDR(node, IPv6) {
        cidr, err := sa.allocateNextIPv6Subnet()
        if err != nil {
            return nil, fmt.Errorf("failed to allocate IPv6 subnet: %v", err)
        }
        if cidr != "" {
            newCIDRs = append(newCIDRs, cidr)
            sa.allocatedIPv6[cidr] = true
        }
    }

    return newCIDRs, nil
}
```

### 7.4 重叠检测 (`ipam/subnet_allocator.go:184-241`)

```go
// subnetsOverlap 检查两个子网是否重叠
func subnetsOverlap(a, b *net.IPNet) bool {
    // 检查是否包含起始 IP
    if a.Contains(b.IP) || b.Contains(a.IP) {
        return true
    }

    // 检查是否包含广播地址
    aBroadcast := getBroadcastAddr(a)
    bBroadcast := getBroadcastAddr(b)

    if a.Contains(bBroadcast) || b.Contains(aBroadcast) {
        return true
    }

    return false
}

// getBroadcastAddr 返回子网的广播地址
func getBroadcastAddr(subnet *net.IPNet) net.IP {
    ip := subnet.IP.To4()
    if ip == nil {
        // IPv6
        ip = subnet.IP.To16()
        broadcast := make(net.IP, 16)
        for i := 0; i < 16; i++ {
            broadcast[i] = ip[i] | ^subnet.Mask[i]
        }
        return broadcast
    }

    // IPv4
    broadcast := make(net.IP, 4)
    for i := 0; i < 4; i++ {
        broadcast[i] = ip[i] | ^subnet.Mask[i]
    }
    return broadcast
}
```

---

## 八、Cilium Agent IP 分配

### 8.1 allocateNext (`pkg/ipam/crd.go:578-654`)

Cilium Agent 分配 IP 的核心函数。支持固定 IP 查找。

**函数签名：**
```go
func (n *nodeStore) allocateNext(
    allocated ipamTypes.AllocationMap,
    family Family,
    owner string,
) (net.IP, *ipamTypes.AllocationIP, error)
```

**处理流程：**

```
┌─────────────────────────────────────────────────────────────────────┐
│                       allocateNext()                               │
├─────────────────────────────────────────────────────────────────────┤
│ 1. 检查 Pod 注解                                                     │
│    └── pod.Annotations[CiliumIPAMPodAnnotation] ?                  │
│       → podIPAMCrdFlag = true                                      │
│                                                                     │
│ 2. CRD 模式且指定 Owner → 查找固定 IP                                │
│    └── n.conf.IPAMMode() == IPAMCRD && len(owner) != 0 ?          │
│       → 遍历 n.ownNode.Spec.IPAM.Pool                             │
│       → ipInfo.Owner == owner ?                                    │
│       → 返回固定 IP                                                 │
│                                                                     │
│ 3. Pod 要求固定 IP 但找不到 → 报错                                   │
│    └── podIPAMCrdFlag == true ?                                    │
│       → return error: "can't find ip with owner set"              │
│                                                                     │
│ 4. 分配下一个可用 IP                                                 │
│    └── 遍历 n.ownNode.Spec.IPAM.Pool                              │
│       ├── ip in allocated ? → continue                             │
│       ├── ip in ReleaseIPs ? → continue                            │
│       ├── ipInfo.Owner != "" ? → continue (IP 被占用)              │
│       └── 返回可用 IP                                               │
│                                                                     │
│ 5. 无可用 IP                                                        │
│    └── return error: "No more IPs available"                      │
└─────────────────────────────────────────────────────────────────────┘
```

**关键代码：**
```go
func (n *nodeStore) allocateNext(allocated ipamTypes.AllocationMap, family Family, owner string) (net.IP, *ipamTypes.AllocationIP, error) {
    n.mutex.RLock()
    defer n.mutex.RUnlock()

    // 1. 检查 Pod 注解
    podIPAMCrdFlag := false
    if n.k8sWatcher != nil {
        info := strings.Split(owner, "/")
        if len(info) == 2 {
            pod, err := n.k8sWatcher.GetCachedPod(info[0], info[1])
            if err == nil && pod.Annotations != nil && pod.Annotations[CiliumIPAMPodAnnotation] != "" {
                podIPAMCrdFlag = true
            }
        }
    }

    // 2. CRD 模式且指定 Owner → 查找固定 IP
    if n.conf.IPAMMode() == ipamOption.IPAMCRD && len(owner) != 0 {
        for ip, ipInfo := range n.ownNode.Spec.IPAM.Pool {
            if ipInfo.Owner == owner {
                parsedIP := net.ParseIP(ip)
                if parsedIP == nil {
                    return nil, nil, fmt.Errorf("invalid custom ip %s for %s", ip, owner)
                }
                if DeriveFamily(parsedIP) != family {
                    continue
                }
                // 返回固定 IP
                return parsedIP, &ipInfo, nil
            }
        }
    }

    // 3. Pod 要求固定 IP 但找不到
    if podIPAMCrdFlag {
        return nil, nil, fmt.Errorf("Pod %s with annotation %s set, can't find ip with owner set",
            owner, CiliumIPAMPodAnnotation)
    }

    // 4. 分配下一个可用 IP
    for ip, ipInfo := range n.ownNode.Spec.IPAM.Pool {
        if _, ok := allocated[ip]; !ok {
            // 跳过释放中的 IP
            if n.isIPInReleaseHandshake(ip) {
                continue
            }
            // 跳过已被占用的 IP
            if ipInfo.Owner != "" {
                continue
            }
            parsedIP := net.ParseIP(ip)
            if parsedIP == nil || DeriveFamily(parsedIP) != family {
                continue
            }
            return parsedIP, &ipInfo, nil
        }
    }

    // 5. 无可用 IP
    return nil, nil, fmt.Errorf("No more IPs available")
}
```

---

## 九、配置参数

### 9.1 命令行参数 (`ipam/main.go:60-81`)

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--labelSector` | JSON | `{"matchLabels":{"noStatic":"true"}}` | Pod 标签选择器 |
| `--ipRateNum` | int | `255` | 预分配 IP 水位线 |
| `--maxAllocatedIP` | int | `255` | 每节点最大分配 IP 数 |
| `--enableIPv6` | bool | `false` | 启用 IPv6 |
| `--enableStaticPodIPv6` | bool | `false` | 启用 Pod IPv6 固定 IP |
| `--excludeDbbranchPrefixs` | []string | `[]` | 排除的 DBBranch 标签前缀 |
| `--cluster-pool-ipv4-cidr` | string | 环境变量 | IPv4 集群池 CIDR |
| `--cluster-pool-ipv4-mask-size` | string | 环境变量 | IPv4 子网掩码大小 |
| `--cluster-pool-ipv6-cidr` | string | 环境变量 | IPv6 集群池 CIDR |
| `--cluster-pool-ipv6-mask-size` | string | 环境变量 | IPv6 子网掩码大小 |
| `--enable-leader-election` | bool | `true` | 启用 Leader 选举 |
| `--leader-election-namespace` | string | `$POD_NAMESPACE` | Leader 选举命名空间 |
| `--leader-election-id` | string | `cilium-ipam-leader` | Leader 选举锁资源名 |
| `--leader-election-lease-duration` | duration | `15s` | Leader 租约时长 |
| `--leader-election-renew-deadline` | duration | `10s` | Leader 续约截止时间 |
| `--leader-election-retry-period` | duration | `2s` | Leader 选举重试周期 |
| `--cepTerminationTimeout` | duration | `30m` | CEP 终止等待超时 |
| `--logLevel` | int | `4` | 日志级别 (0-6) |

### 9.2 环境变量

| 变量 | 说明 |
|------|------|
| `KUBECONFIG` | Kubernetes 配置文件路径 |
| `POD_NAMESPACE` | Pod 所在命名空间 |
| `CLUSTER_POOL_IPV4_CIDR` | IPv4 集群池 CIDR |
| `CLUSTER_POOL_IPV4_MASK_SIZE` | IPv4 子网掩码大小 |
| `CLUSTER_POOL_IPV6_CIDR` | IPv6 集群池 CIDR |
| `CLUSTER_POOL_IPV6_MASK_SIZE` | IPv6 子网掩码大小 |

---

## 十、完整流程图

### 10.1 Pod 创建与固定 IP 分配

```
┌─────────────────────────────────────────────────────────────────────┐
│                     Pod 创建与固定 IP 分配                          │
└─────────────────────────────────────────────────────────────────────┘

                    ┌─────────────┐
                    │  Pod 创建   │
                    └─────────────┘
                           │
                           ▼
        ┌──────────────────────────────────┐
        │ kubelet 通过 CNI 分配 IP         │
        │ Pod.Status.PodIP 被设置          │
        └──────────────────────────────────┘
                           │
                           ▼
        ┌──────────────────────────────────┐
        │ IPAM Controller Informer         │
        │ 监听到 Pod Add 事件               │
        └──────────────────────────────────┘
                           │
                           ▼
        ┌──────────────────────────────────┐
        │ podQueue.Add(podQueueItem{       │
        │   key: "ns/podName",             │
        │   isAdd: true                    │
        │ })                               │
        └──────────────────────────────────┘
                           │
                           ▼
        ┌──────────────────────────────────┐
        │ DoPodAddHandle() 处理            │
        ├──────────────────────────────────┤
        │ 1. 检查标签选择器                 │
        │ 2. 检查排除前缀                   │
        │ 3. CleanupOrphanedIPAllocations()│
        │ 4. 解析 Pod IPv4/IPv6            │
        │ 5. 构建 Resource 字段             │
        └──────────────────────────────────┘
                           │
                           ▼
        ┌──────────────────────────────────┐
        │ FlagIpForPod()                   │
        │ 设置 CiliumNode.Spec.IPAM.Pool   │
        │ [IP].Owner = "ns/podName"        │
        │ [IP].Resource = "ns/Kind/name"   │
        └──────────────────────────────────┘
                           │
                           ▼
        ┌──────────────────────────────────┐
        │ Update CiliumNode CRD            │
        └──────────────────────────────────┘
                           │
                           ▼
        ┌──────────────────────────────────┐
        │ Cilium Agent allocateNext()      │
        │ 查找 Owner 匹配的 IP              │
        │ 返回固定 IP                       │
        └──────────────────────────────────┘
                           │
                           ▼
        ┌──────────────────────────────────┐
        │ Pod 使用固定 IP                  │
        │ CEP 记录 IP 映射                  │
        └──────────────────────────────────┘
```

### 10.2 IP 回收流程

```
┌───────────���─────────────────────────────────────────────────────────┐
│                         IP 回收流程                                 │
└─────────────────────────────────────────────────────────────────────┘

        ┌──────────────────────────────────┐
        │ 触发条件：                         │
        │ 1. 定时 SyncCiliumNodeIPAlloc()   │
        │ 2. StatefulSet 删除               │
        │ 3. Pod 调度到不同节点             │
        └──────────────────────────────────┘
                           │
                           ▼
        ┌──────────────────────────────────┐
        │ DoOwnerIpRecycle() 遍历 Pool     │
        └──────────────────────────────────┘
                           │
                           ▼
        ┌──────────────────────────────────┐
        │ 检查 IP.Owner 是否为空            │
        │ 为空 → 跳过                        │
        └──────────────────────────────────┘
                           │
                           ▼
        ┌──────────────────────────────────┐
        │ 解析 Resource: "ns/Kind/name"    │
        └──────────────────────────────────┘
                           │
                           ▼
        ┌──────────────────────────────────┐
        │ 检查 Pod 是否存在                 │
        │ ┌────────────────────────────┐   │
        │ │ Pod 存在                  │   │
        │ │ ├── 在不同节点? → 回收      │   │
        │ │ └── 在当前节点? → 保持      │   │
        │ └────────────────────────────┘   │
        │ ┌────────────────────────────┐   │
        │ │ Pod 不存在                │   │
        │ │ └── 检查资源存在性          │   │
        │ └────────────────────────────┘   │
        └──────────────────────────────────┘
                           │
                           ▼
        ┌──────────────────────────────────┐
        │ switch Resource.Kind:            │
        │ case "StatefulSet":              │
        │   stsLister.Get(name)            │
        │   NotFound? → needRecycle = true │
        └──────────────────────────────────┘
                           │
                           ▼
        ┌──────────────────────────────────┐
        │ needRecycle == true?             │
        │ ┌────────────────────────────┐   │
        │ │ 清空 Owner 和 Resource     │   │
        │ │ IP 变为可用                 │   │
        │ └────────────────────────────┘   │
        └──────────────────────────────────┘
                           │
                           ▼
        ┌──────────────────────────────────┐
        │ Update CiliumNode CRD            │
        └──────────────────────────────────┘
```

### 10.3 CEP 守护流程

```
┌─────────────────────────────────────────────────────────────────────┐
│                        CEP 守护流程                                 │
└─────────────────────────────────────────────────────────────────────┘

        ┌──────────────────────────────────┐
        │ 触发条件：                         │
        │ 1. Pod Add/Update 事件            │
        │ 2. 定时扫描 (15分钟)               │
        └──────────────────────────────────┘
                           │
                           ▼
        ┌──────────────────────────────────┐
        │ KeepPodCepWithContext()          │
        └──────────────────────────────────┘
                           │
            ┌──────────────┴──────────────┐
            │                             │
            ▼                             ▼
   ┌─────────────────┐          ┌─────────────────┐
   │ 定时扫描删除中 CEP │          │ 处理队列事件     │
   └─────────────────┘          └─────────────────┘
            │                             │
            ▼                             ▼
   ┌─────────────────┐          ┌─────────────────┐
   │ DoCepKeeper()   │          │ DoCepKeeper()   │
   │ isDeleting=true │          │ isNotFound/     │
   │                 │          │ isDeleting      │
   └─────────────────┘          └─────────────────┘
            │                             │
            ▼                             ▼
   ┌─────────────────┐          ┌─────────────────┐
   │ 检查超时 (30分钟)│          │ CEP 不存在      │
   │ 检查 IP 一致性   │          │                 │
   └─────────────────┘          └─────────────────┘
            │                             │
            ▼                             ▼
   ┌─────────────────┐          ┌─────────────────┐
   │ 不一致或超时?   │          │ PatchPodLabel() │
   │ → recycleCep() │          │ 触发 CEP 重建    │
   └─────────────────┘          └─────────────────┘
```

---

## 附录

### A. 关键常量

```go
const dbBranchLabelKey = "DBBranch"  // DBBranch 标签键

const (
    IPv6 Family = "ipv6"
    IPv4 Family = "ipv4"
)

var cepTerminationTimeout = pflag.Duration(
    "cepTerminationTimeout",
    time.Minute*30,
    "cep max termination wait time")
```

### B. 日志级别

| 级别 | 值 | 说明 |
|------|-----|------|
| Panic | 0 | 最高级别，程序将终止 |
| Fatal | 1 | 致命错误 |
| Error | 2 | 错误 |
| Warning | 3 | 警告 |
| Info | 4 | 信息（默认） |
| Debug | 5 | 调试 |
| Trace | 6 | 最详细跟踪 |

### C. 常见问题排查

1. **Pod 没有获得固定 IP**
   - 检查 Pod 标签是否匹配 `labelSector`
   - 检查 Pod 是否有 NodeName 和 PodIP
   - 检查是否匹配排除前缀

2. **IP 回收失败**
   - 检查 StatefulSet 是否仍然存在
   - 检查 Controller 权限

3. **CEP 残留**
   - 检查 CEP DeletionTimestamp
   - 手动移除 Finalizers

---

**文档版本**: v1.0
**最后更新**: 2025-01-13
**基于代码**: Cilium v1.12.7 IPAM Fork