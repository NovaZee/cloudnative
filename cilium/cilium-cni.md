# Cilium CNI cmdAdd 完整流程分析

> 本文档详细描述 Cilium CNI 插件中 `cmdAdd` 方法（Pod 网络配置）的完整执行流程
>
> **代码版本**: Cilium main branch
> **核心文件**: `plugins/cilium-cni/cmd/cmd.go`

---

## 目录

- [1. 概述](#1-概述)
- [2. 入口函数](#2-入口函数)
- [3. 详细流程](#3-详细流程)
- [4. 关键代码位置索引](#4-关键代码位置索引)
- [5. Hook 机制](#5-hook-机制)
- [6. IPAM 模式对比](#6-ipam-模式对比)
- [7. DatapathMode 对比](#7-datapathmode-对比)
- [8. 错误处理与资源清理](#8-错误处理与资源清理)

---

## 1. 概述

CNI ADD 操作由容器运行时（如 kubelet）在创建容器/Pod 时调用，用于配置容器网络。Cilium CNI 插件的 `cmdAdd` 方法负责：

1. 连接 Cilium Agent 获取配置
2. 分配 IP 地址（通过 Cilium IPAM 或委托插件）
3. 创建网络设备（veth/netkit）
4. 配置容器网络接口
5. 在 Cilium Agent 中注册 Endpoint

---

## 2. 入口函数

**文件**: `plugins/cilium-cni/cmd/cmd.go:523`

```go
func (cmd *Cmd) Add(args *skel.CmdArgs) (err error)
```

**输入参数**: `skel.CmdArgs`
- `args.StdinData`: CNI 网络配置（JSON）
- `args.Netns`: 容器网络命名空间路径
- `args.IfName`: 容器内接口名称（通常为 `eth0`）
- `args.ContainerID`: 容器 ID
- `args.Args`: CNI 参数（如 K8S_POD_NAME）

---

## 3. 详细流程

### 3.1 阶段一：初始化与配置加载

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           阶段一：初始化与配置加载                            │
└─────────────────────────────────────────────────────────────────────────────┘

                                    ┌─────────────┐
                                    │   1. 启动    │
                                    └──────┬──────┘
                                           │
                                           ▼
                          ┌────────────────────────────────────┐
                          │ 解析 CNI 配置 (LoadNetConf)        │
                          │ - 读取 stdin 配置                  │
                          │ - 设置日志                          │
                          │ - 启用调试模式 (可选)               │
                          └──────────────────┬─────────────────┘
                                           │
                                           ▼
                          ┌────────────────────────────────────┐
                          │ 提取 CNI 参数 (LoadArgs)           │
                          │ - K8S_POD_NAME                     │
                          │ - K8S_POD_NAMESPACE                │
                          │ - 其他 CNI args                    │
                          └──────────────────┬─────────────────┘
                                           │
                                           ▼
                          ┌────────────────────────────────────┐
                          │ 连接 Cilium Agent                  │
                          │ client.NewDefaultClientWithTimeout │
                          └──────────────────┬─────────────────┘
                                           │
                                           ▼
                          ┌────────────────────────────────────┐
                          │ 获取 Cilium 配置                   │
                          │ getConfigFromCiliumAgent()         │
                          │ - IPAM 模式                        │
                          │ - Datapath 模式                    │
                          │ - 地址池配置                        │
                          └──────────────────┬─────────────────┘
                                           │
                                           ▼
                          ┌────────────────────────────────────┐
                          │ 触发 onConfigReady Hooks           │
                          └──────────────────┬─────────────────┘
```

**关键代码**: `cmd.go:524-574`

```go
// 解析 CNI 配置
n, err := types.LoadNetConf(args.StdinData)

// 设置日志
cmd.setupLogging(n)

// 提取 CNI 参数
cniArgs := &types.ArgsSpec{}
cniTypes.LoadArgs(args.Args, cniArgs)

// 连接 Cilium Agent
c, err := client.NewDefaultClientWithTimeout(defaults.ClientConnectTimeout)

// 获取配置
conf, err := getConfigFromCiliumAgent(c)
```

---

### 3.2 阶段二：Chained Plugin 检查

```
                   ┌───────────────────────┴───────────────────────┐
                   │          检查是否 Chained Plugin?             │
                   │         (n.NetConf.RawPrevResult != 0)       │
                   └───────────────────────┬───────────────────────┘
                            YES                            │
            ┌────────────────────────┐           │ NO
            │ 执行 Chained ADD       │           │
            │ chainAction.Add()      │           │
            │ 返回结果              │           │
            └────────────────────────┘           │
                                                 │
                                                 ▼
                                          继续正常流程
```

**关键代码**: `cmd.go:579-608`

Cilium 支持作为 Chained CNI 插件运行。如果检测到 `PrevResult`，则会根据 `chaining-mode` 配置执行相应的链式逻辑。

---

### 3.3 阶段三：IP 地址分配

```
                   ┌──────────────────────────────────────────────────────┐
                   │                   IP 分配                             │
                   │         (根据 IpamMode 选择方式)                      │
                   │                                                       │
                   │  ┌────────────────────┐    ┌────────────────────┐   │
                   │  │ IPAMDelegatedPlugin│    │   Cilium Agent     │   │
                   │  │                    │    │                    │   │
                   │  │ DelegateAdd()      │    │ client.IPAMAllo... │   │
                   │  │ (外部插件)          │    │ (Cilium 内置)      │   │
                   │  └────────────────────┘    └────────────────────┘   │
                   └──────────────────────────┬───────────────────────────┘
                                           │
                                           ▼
                          ┌────────────────────────────────────┐
                          │ 验证 IPAM 结果                     │
                          │ - SufficientAddressing()           │
                          │ - 检查 IPv4/IPv6                 │
                          └──────────────────┬─────────────────┘
```

**关键代码**: `cmd.go:634-657`

```go
if conf.IpamMode == ipamOption.IPAMDelegatedPlugin {
    // 委托给外部 IPAM 插件
    ipam, releaseIPsFunc, err = allocateIPsWithDelegatedPlugin(context.TODO(), conf, n, args.StdinData)
} else {
    // 使用 Cilium Agent 内置 IPAM
    ipam, releaseIPsFunc, err = allocateIPsWithCiliumAgent(scopedLogger, c, cniArgs, epConf.IPAMPool())
}
```

---

### 3.4 阶段四：网络设备创建

```
                   ┌──────────────────────────────────────────────────────┐
                   │                 设置网络设备                           │
                   │            (根据 DatapathMode 选择)                   │
                   │                                                       │
                   │  ┌──────────────┐  ┌──────────────┐  ┌─────────────┐│
                   │  │   VETH       │  │  NETKIT      │  │ NETKIT-L2   ││
                   │  │              │  │              │  │             ││
                   │  │SetupVeth()   │  │SetupNetkit() │  │SetupNetkit()││
                   │  │L2 Mode       │  │L3 Mode       │  │L2 Mode      ││
                   │  └──────────────┘  └──────────────┘  └─────────────┘│
                   │                   创建 veth/netkit pair              │
                   └──────────────────────────┬───────────────────────────┘
                                           │
                                           ▼
                          ┌────────────────────────────────────┐
                          │ 将容器端设备移入容器命名空间        │
                          │ netlink.LinkSetNsFd(epLink, ns.FD) │
                          │ 重命名: tmpIfName -> epConf.IfName │
                          └──────────────────┬─────────────────┘
```

**关键代码**: `cmd.go:690-728`

```go
switch conf.DatapathMode {
case datapathOption.DatapathModeVeth:
    l2Mode = true
    hostLink, epLink, tmpIfName, err = connector.SetupVeth(scopedLogger, cniID, linkConfig, sysctl)
case datapathOption.DatapathModeNetkit, datapathOption.DatapathModeNetkitL2:
    l2Mode = conf.DatapathMode == datapathOption.DatapathModeNetkitL2
    hostLink, epLink, tmpIfName, err = connector.SetupNetkit(scopedLogger, cniID, linkConfig, l2Mode, sysctl)
}

// 将容器端设备移入容器网络命名空间
netlink.LinkSetNsFd(epLink, ns.FD())

// 在容器命名空间内重命名接口
ns.Do(func() error {
    return link.Rename(tmpIfName, epConf.IfName())
})
```

---

### 3.5 阶段五：IP 地址配置

```
                   ┌──────────────────────────────────────────────────────┐
                   │              处理 IPv4/IPv6 地址                      │
                   │                                                       │
                   │  ┌─────────────────────────────────────────────┐    │
                   │  │ For IPv6:                                     │    │
                   │  │ - ep.Addressing.IPV6 = ipam.Address.IPV6      │    │
                   │  │ - prepareIP() 生成 IPConfig 和 Routes         │    │
                   │  │ - res.IPs, res.Routes 追加                    │    │
                   │  └─────────────────────────────────────────────┘    │
                   │                                                       │
                   │  ┌─────────────────────────────────────────────┐    │
                   │  │ For IPv4:                                     │    │
                   │  │ - ep.Addressing.IPV4 = ipam.Address.IPV4      │    │
                   │  │ - prepareIP() 生成 IPConfig 和 Routes         │    │
                   │  │ - res.IPs, res.Routes 追加                    │    │
                   │  └─────────────────────────────────────────────┘    │
                   └──────────────────────────┬───────────────────────────┘
```

**关键代码**: `cmd.go:742-776`

---

### 3.6 阶段六：特殊云平台路由配置

```
                   ┌──────────────────────────────────────────────────────┐
                   │         检查是否需要在 Host 端配置路由                 │
                   │              needsEndpointRoutingOnHost()            │
                   │                  (ENI/Azure/AlibabaCloud)            │
                   │                                                       │
                   │  如果需要:                                            │
                   │  ┌─────────────────────────────────────────────┐    │
                   │  │ interfaceAdd() - 在 host 端配置 IP 和路由     │    │
                   │  └─────────────────────────────────────────────┘    │
                   └──────────────────────────┬───────────────────────────┘
```

**关键代码**: `cmd.go:778-792`

某些云平台（AWS ENI、Azure、Alibaba Cloud）需要在主机命名空间中配置额外的路由规则。

---

### 3.7 阶段七：容器网络配置

```
                   ┌──────────────────────────────────────────────────────┐
                   │            在容器命名空间内配置接口                      │
                   │                    (ns.Do)                            │
                   │                                                       │
                   │  ┌─────────────────────────────────────────────┐    │
                   │  │ 1. reserveLocalIPPorts() - 保留本地端口      │    │
                   │  │ 2. Disable IPv6 (如需要)                     │    │
                   │  │ 3. configureIface() - 配置 IP、路由、规则     │    │
                   │  └─────────────────────────────────────────────┘    │
                   └──────────────────────────┬───────────────────────────┘
```

**关键代码**: `cmd.go:802-822`

```go
if err = ns.Do(func() error {
    // 保留本地端口范围
    if err := reserveLocalIPPorts(conf, sysctl); err != nil {
        scopedLogger.Warn("Unable to reserve local ip ports", logfields.Error, err)
    }

    // 启用 IPv6（如需要）
    if ipv6IsEnabled(ipam) {
        sysctl.Disable([]string{"net", "ipv6", "conf", "all", "disable_ipv6"})
    }

    // 配置接口
    macAddrStr, err = configureIface(scopedLogger, ipam, epConf.IfName(), state)
    return err
}); err != nil {
    return fmt.Errorf("unable to configure interfaces in container namespace: %w", err)
}
```

---

### 3.8 阶段八：Endpoint 注册与完成

```
                          ┌────────────────────────────────────┐
                          │ 获取 NetNS Cookie                  │
                          │ (用于后续快速查找)                 │
                          └──────────────────┬─────────────────┘
                                           │
                                           ▼
                          ┌────────────────────────────────────┐
                          │ 创建 Endpoint (同步构建)            │
                          │ c.EndpointCreate(ep)               │
                          │ - ep.SyncBuildEndpoint = true       │
                          │ - ep.ContainerNetnsPath             │
                          └──────────────────┬─────────────────┘
                                           │
                                           ▼
                   ┌──────────────────────────────────────────────────────┐
                   │          更新 MAC 地址 (非 Netkit L3 模式)            │
                   │                                                       │
                   │  从 Agent 返回的 Endpoint 获取 MAC 并设置到接口       │
                   └──────────────────────────┬───────────────────────────┘
                                           │
                                           ▼
                   ┌──────────────────────────────────────────────────────┐
                   │              配置网络参数                              │
                   │                                                       │
                   │  - configurePacketizationLayerPMTUD()                 │
                   │  - configureCongestionControl()                       │
                   └──────────────────────────┬───────────────────────────┘
                                           │
                                           ▼
                          ┌────────────────────────────────────┐
                          │ 添加容器接口到 Result                │
                          │ res.Interfaces.Append()             │
                          └──────────────────┬─────────────────┘
                                           │
                                           ▼
                          ┌────────────────────────────────────┐
                          │ 打印并返回 CNI Result                │
                          │ cniTypes.PrintResult(res, version)   │
                          └────────────────────────────────────┘
```

**关键代码**: `cmd.go:824-884`

```go
// 获取 NetNS Cookie
ns.Do(func() error {
    cookie, err = netns.GetNetNSCookie()
    return err
})

// 创建 Endpoint
ep.SyncBuildEndpoint = true
ep.ContainerNetnsPath = filepath.Join(defaults.NetNsPath, filepath.Base(args.Netns))
newEp, err = c.EndpointCreate(ep)

// 更新 MAC 地址（Netkit L3 模式除外）
if conf.DatapathMode != datapathOption.DatapathModeNetkit {
    ns.Do(func() error {
        return mac.ReplaceMacAddressWithLinkName(args.IfName, newEp.Status.Networking.Mac)
    })
}

// 配置拥塞控制等参数
ns.Do(func() error {
    configurePacketizationLayerPMTUD(scopedLogger, conf, sysctl)
    return configureCongestionControl(conf, sysctl)
})

// 返回结果
return cniTypes.PrintResult(res, n.CNIVersion)
```

---

## 4. 关键代码位置索引

| 阶段 | 文件:行号 | 函数/描述 |
|------|----------|----------|
| **入口** | `cmd.go:523` | `func (cmd *Cmd) Add(args *skel.CmdArgs)` |
| **解析配置** | `cmd.go:524-527` | `types.LoadNetConf()` |
| **连接 Agent** | `cmd.go:560` | `client.NewDefaultClientWithTimeout()` |
| **获取配置** | `cmd.go:565` | `getConfigFromCiliumAgent()` |
| **Chained 检查** | `cmd.go:579-608` | 检查 `RawPrevResult` |
| **IPAM 分配** | `cmd.go:634-638` | `allocateIPsWithDelegatedPlugin` / `allocateIPsWithCiliumAgent` |
| **准备 Endpoint** | `cmd.go:665` | `epConf.PrepareEndpoint(ipam)` |
| **创建设备** | `cmd.go:690-697` | `SetupVeth` / `SetupNetkit` |
| **移动到容器 NS** | `cmd.go:721-728` | `LinkSetNsFd` + `Rename` |
| **配置接口** | `cmd.go:802-822` | `configureIface()` |
| **创建 Endpoint** | `cmd.go:846` | `c.EndpointCreate(ep)` |
| **返回结果** | `cmd.go:884` | `cniTypes.PrintResult()` |

---

## 5. Hook 机制

Cilium CNI 提供了多个 Hook 点用于扩展功能：

### 5.1 Hook 类型

```go
// 配置就绪后触发
cmd.onConfigReady → OnConfigReady(n, cniArgs, conf)

// IPAM 完成后触发
cmd.onIPAMReady → OnIPAMReady(ipam)

// LinkConfig 准备好后触发
cmd.onLinkConfigReady → OnLinkConfigReady(&linkConfig)

// 接口配置完成后触发
cmd.onInterfaceConfigReady → OnInterfaceConfigReady(state, ep, res)
```

### 5.2 代码位置

`cmd.go:570-574, 659-663, 681-685, 794-798`

---

## 6. IPAM 模式对比

### 6.1 Cilium Agent 模式（默认）

**函数**: `allocateIPsWithCiliumAgent` (`cmd.go:172`)

```go
ipam, err := client.IPAMAllocate("", podName, ipamPoolName, true)
```

| 特性 | 描述 |
|------|------|
| **IP 分配者** | Cilium Agent 内置 IPAM 系统 |
| **调用方式** | `client.IPAMAllocate()` (gRPC/HTTP to Cilium Agent) |
| **适用场景** | Cilium 完全管理网络栈 |
| **配置位置** | Cilium Config (`ipam` 字段) |
| **支持功能** | 多集群 IPAM、IP 池隔离、静态 IP 分配 |

### 6.2 Delegated Plugin 模式

**函数**: `allocateIPsWithDelegatedPlugin` (`cmd.go:207`)

```go
ipamRawResult, err := cniInvoke.DelegateAdd(ctx, netConf.IPAM.Type, stdinData, nil)
```

| 特性 | 描述 |
|------|------|
| **IP 分配者** | 外部第三方 CNI IPAM 插件 |
| **调用方式** | `cniInvoke.DelegateAdd()` (执行外部二进制) |
| **适用场景** | 需要与其他 IPAM 系统集成 |
| **常见插件** | `host-local`, `dhcp`, `whereabouts` |
| **迁移场景** | 从其他 CNI 插件迁移时保留现有 IP |

### 6.3 对比总结

| 对比维度 | Cilium Agent | Delegated Plugin |
|---------|--------------|------------------|
| **IP 分配者** | Cilium Agent | 外部 CNI 插件 |
| **调用方式** | `client.IPAMAllocate()` | `cniInvoke.DelegateAdd()` |
| **适用场景** | Cilium 完全管理网络栈 | 与其他 IPAM 系统集成 |
| **配置位置** | Cilium Config (`ipam` 字段) | CNI 配置中的 `ipam` 段 |

---

## 7. DatapathMode 对比

### 7.1 三种模式

Cilium 支持三种 DatapathMode（定义在 `pkg/datapath/option/option.go:8-21`）：

| 模式 | 配置值 | 网络设备类型 | 工作模式 |
|------|--------|-------------|----------|
| **Veth** | `veth` | Virtual Ethernet Pair | L2 模式 |
| **Netkit (L3)** | `netkit` | Netkit Pair | L3 模式 |
| **Netkit (L2)** | `netkit-l2` | Netkit Pair | L2 模式 |

### 7.2 Veth 模式

**函数**: `SetupVeth` (`pkg/datapath/connector/veth.go:23`)

```go
veth := &netlink.Veth{
    LinkAttrs: netlink.LinkAttrs{
        Name:         lxcIfName,
        HardwareAddr: net.HardwareAddr(epHostMAC),
        TxQLen:       1000,
    },
    PeerName:         peerIfName,
    PeerHardwareAddr: net.HardwareAddr(epLXCMAC),
}
```

- **工作方式**: 二层以太网隧道
- **数据流**: 容器 → veth peer → 主机网络栈
- **特点**: 简单、成熟、广泛使用，需要 MAC 地址，默认模式

### 7.3 Netkit 模式

**函数**: `SetupNetkit` (`pkg/datapath/connector/netkit.go:23`)

```go
netkit := &netlink.Netkit{
    LinkAttrs: netlink.LinkAttrs{
        Name:         lxcIfName,
        HardwareAddr: net.HardwareAddr(epHostMAC),
    },
    Mode:       netlink.NETKIT_MODE_L3,  // L3 模式
    Policy:     netlink.NETKIT_POLICY_FORWARD,
    PeerPolicy: netlink.NETKIT_POLICY_BLACKHOLE,
    Scrub:      netlink.NETKIT_SCRUB_NONE,
    PeerScrub:  netlink.NETKIT_SCRUB_DEFAULT,
    DesiredHeadroom: uint16(cfg.DeviceHeadroom),
    DesiredTailroom: uint32(cfg.DeviceTailroom),
}
```

- **工作方式**: 三层路由模式
- **数据流**: 容器 → 路由 → 主机网络栈（无需 L2 封装）
- **内核要求**: Linux 6.7+
- **特点**: 性能更高，无需 MAC 地址（L3 模式），支持精细流量控制

### 7.4 对比总结

| 特性 | Veth | Netkit |
|------|------|--------|
| **内核版本要求** | 任意 | Linux 6.7+ |
| **性能** | 标准 | 更优（减少 L2 开销） |
| **流量策略** | 依赖 tc/ebpf | 内置 Policy 机制 |
| **Headroom/Tailroom** | 需手动配置 | 原生支持 |
| **Packet Scrubbing** | 不支持 | 支持 |
| **L3 模式** | 不支持 | 支持 |

### 7.5 使用建议

| 场景 | 推荐模式 |
|------|----------|
| 生产环境（稳定优先） | `veth` |
| 内核 >= 6.7（追求性能） | `netkit` |
| 需要特定 L2 特性 + Netkit | `netkit-l2` |
| 测试/开发环境 | `veth`（默认） |

---

## 8. 错误处理与资源清理

### 8.1 IP 释放机制

```go
// cmd.go:640-645
defer func() {
    if err != nil && releaseIPsFunc != nil {
        releaseIPsFunc(context.TODO())
    }
}()
```

IP 分配失败时，通过 `defer` + `releaseIPsFunc` 自动释放已分配的 IP 地址。

### 8.2 网络设备清理

```go
// cmd.go:701-711
defer func() {
    if err != nil {
        if err2 := netlink.LinkDel(hostLink); err2 != nil {
            scopedLogger.Warn("Failed to clean up and delete link", ...)
        }
    }
}()
```

设备创建失败时，自动删除已创建的 `hostLink`。

### 8.3 NetNS 句柄管理

```go
// cmd.go:616-620
ns, err := netns.OpenPinned(args.Netns)
if err != nil {
    return fmt.Errorf("opening netns pinned at %s: %w", args.Netns, err)
}
defer ns.Close()
```

确保网络命名空间句柄被正确释放。

---

## 附录：配置示例

### CNI 配置示例

```json
{
  "cniVersion": "1.0.0",
  "name": "cilium",
  "type": "cilium-cni",
  "ipam": {
    "type": "cilium-ipam"
  },
  "enable-debug": false,
  "log-file": "/var/run/cilium/cni.log",
  "chaining-mode": "portmap"
}
```

### DatapathMode 配置

```bash
# 使用 veth（默认）
cilium install --datapath-mode=veth

# 使用 netkit L3 模式
cilium install --datapath-mode=netkit

# 使用 netkit L2 模式
cilium install --datapath-mode=netkit-l2
```

---

## 总结

Cilium CNI 的 `cmdAdd` 流程是一个复杂但设计精良的过程，涵盖了从配置解析到网络设备创建的完整生命周期。主要特点：

1. **灵活性**: 支持多种 IPAM 模式和 DatapathMode
2. **可扩展**: 通过 Hook 机制支持自定义扩展
3. **健壮性**: 完善的错误处理和资源清理机制
4. **性能**: Netkit 模式提供更优的网络性能
5. **兼容性**: 支持与现有 CNI 插件链式部署

---

**文档版本**: 1.0
**最后更新**: 2026-01-13