# CSI 开发流程规范与设计（基于 Kubernetes CSI 官方文档梳理）

本文面向 **CSI Driver 开发/运维**，基于 Kubernetes CSI Developer Documentation 对“Sidecar Containers / 部署方式 / CSI 对象 / Topology”等内容进行整理，给出一套可复用的 **开发流程规范** 与 **架构设计要点**。

> 核心理念：让 CSI Driver 专注实现 CSI gRPC 接口，把“watch Kubernetes API / 处理对象状态机 / leader election / 事件回写”等 Kubernetes 相关通用逻辑交给官方维护的 sidecar 容器完成。

---

## 1. Kubernetes 中 CSI Driver 的标准部署形态

官方推荐把 CSI Driver 拆为两类组件：**Controller Plugin** 与 **Node Plugin**。

### 1.1 Controller Plugin（控制面组件）

- 形态：通常以 `Deployment` 或 `StatefulSet` 部署在集群任意节点。
- 组成：实现 **CSI Controller Service** 的 Driver 容器 + 若干 **controller sidecar**。
- 通信方式：sidecar 与 driver 通过同一个 Pod 内共享的 **UNIX domain socket** 通信（一般用 `emptyDir` 挂载到 `/csi`）。
- HA：可以多副本，但建议开启 **leader election**，保证只有一个 active controller。
- 常见 controller sidecar：
  - `external-provisioner`
  - `external-attacher`
  - `external-resizer`
  - `external-snapshotter`

### 1.2 Node Plugin（节点组件）

- 形态：必须在每个节点运行，通常以 `DaemonSet` 部署。
- 组成：实现 **CSI Node Service** 的 Driver 容器 + `node-driver-registrar`（可选加 `livenessprobe`）。
- 与 kubelet 通信：
  - kubelet 通过 HostPath 暴露的 unix socket 调用 CSI Node RPC（`/var/lib/kubelet/plugins/<driver-name>/csi.sock`）
  - `node-driver-registrar` 通过第二个注册 socket（挂载到容器内通常为 `/registration`）完成插件注册
- 主机访问与挂载传播：
  - Node Plugin 需要访问宿主机路径（如 `/var/lib/kubelet`），用于执行 mount/unmount。
  - 挂载点必须设置 `mountPropagation: Bidirectional`，让 kubelet 能“看到”driver 容器创建的挂载。
- 集群前置条件：
  - 需要允许 **privileged pods**
  - 需要启用 **mount propagation**

---

## 2. Sidecar Containers：职责边界与选型规范

Sidecar Containers 的目标是“通用化” Kubernetes 交互逻辑：watch 对象 → 触发 CSI RPC → 回写 Kubernetes 对象状态。

下面按 sidecar 逐个说明其关注的 Kubernetes 对象、触发的 CSI RPC、以及 driver 需要声明的能力（capabilities）。

### 2.1 `external-provisioner`（动态供给）

- Kubernetes 侧：watch `PersistentVolumeClaim`，创建/删除 `PersistentVolume`
- CSI 侧：调用 `CreateVolume` / `DeleteVolume`
- 触发条件：
  - PVC 引用某个 `StorageClass`
  - `StorageClass.provisioner` 必须与 CSI `GetPluginInfo` 返回的 driver 名称一致
  - PV `reclaimPolicy: Delete` 且 PVC 删除时，会触发 `DeleteVolume`
- 规范要点：
  - Driver 必须声明 Controller capability：`CREATE_DELETE_VOLUME`
  - `StorageClass.parameters` 会透传到 `CreateVolumeRequest.parameters`
  - `csi.storage.k8s.io/*` 前缀的参数是保留字段，sidecar 会根据这些参数执行额外行为（如 secrets、fstype 等）
  - 支持 DataSource（快照/克隆）属于扩展能力：sidecar 会把数据源信息填入 `CreateVolume` 请求

### 2.2 `external-attacher`（Attach/Detach）

- Kubernetes 侧：watch `VolumeAttachment`
- CSI 侧：调用 `ControllerPublishVolume` / `ControllerUnpublishVolume`
- 规范要点：
  - Driver 需要声明 Controller capability：`PUBLISH_UNPUBLISH_VOLUME`
  - 如果你的存储“没有 attach 概念”（例如典型 NFS/本地目录型），推荐通过 `CSIDriver.spec.attachRequired: false` 跳过 attach 流程（见下文 “Skip Attach”）

### 2.3 `external-resizer`（在线/离线扩容）

- Kubernetes 侧：watch PVC 资源请求变更
- CSI 侧：调用 `ControllerExpandVolume`
- 规范要点：
  - Driver 需要声明 `VolumeExpansion` 插件能力

### 2.4 `external-snapshotter`（快照）

- Kubernetes 侧：watch `VolumeSnapshotContent`（以及启用时的 `VolumeGroupSnapshotContent`）
- CSI 侧：
  - `CreateSnapshot` / `DeleteSnapshot` / `ListSnapshots`
  - 若启用 VolumeGroupSnapshot：`CreateVolumeGroupSnapshot` / `DeleteVolumeGroupSnapshot` / `GetVolumeGroupSnapshot`
- 规范要点：
  - Driver 需要声明 Controller capability：`CREATE_DELETE_SNAPSHOT`（以及组快照相关 capability）
  - 使用 snapshot Beta/GA 还需要部署 **snapshot controller**（属于集群控制器）

### 2.5 `node-driver-registrar`（kubelet 注册）

- 作用：通过 `NodeGetInfo` 获取 driver 信息，并用 kubelet plugin registration 机制在该节点注册 CSI driver。
- 规范要点：
  - 所有 CSI drivers 都应部署该 sidecar（Node Plugin 形态）
  - kubelet 会直接调用 `NodeGetInfo` / `NodeStageVolume` / `NodePublishVolume` 等 Node RPC

### 2.6 `livenessprobe`（健康探测）

- 作用：监控 CSI driver 健康状态，并通过 Kubernetes Liveness Probe 机制触发重启。
- 规范要点：
  - 推荐所有 CSI drivers 都部署，用于提高可用性

### 2.7 其他（了解即可）

- `cluster-driver-registrar`：已 deprecated（CSIDriver 对象应直接写在部署清单里）
- `external-health-monitor-controller`：Alpha，调用 `ListVolumes` 或 `ControllerGetVolume` 并在 PVC 上报异常事件
- `external-health-monitor-agent`：Deprecated，被 Kubernetes 的 `CSIVolumeHealth` 功能替代；通过 `NodeGetVolumeStats` 检查并在 Pod 上报异常事件
- `external-snapshot-metadata`：Changed Block Tracking 相关；通过 Kubernetes SnapshotMetadata Service API 给备份应用提供快照元数据

---

## 3. CSI 对象（CSIDriver / CSINode）与行为定制

### 3.1 `CSIDriver` 对象：发现与行为开关

`CSIDriver` 对象有两个核心目的：

1) **简化发现**：用户可以 `kubectl get csidrivers` 看到安装了哪些 CSI driver  
2) **定制 Kubernetes 行为**：例如是否需要 attach、是否在 mount 时传 pod 信息等

关键字段（常用）：

- `metadata.name`：必须等于 CSI Driver 的完整名称（`GetPluginInfo` 返回的 name）
- `spec.attachRequired`：
  - `true`：Kubernetes 会走 attach/detach 流程（通常需要 external-attacher + ControllerPublish/Unpublish）
  - `false`：Kubernetes 跳过 attach（典型 NFS/LocalPV 推荐）
- `spec.podInfoOnMount`：是否让 kubelet 在 `NodePublishVolume` 的 `volume_context` 里携带 Pod 信息
- `spec.fsGroupPolicy`：是否以及如何支持 fsGroup 修改权限/归属
- `spec.volumeLifecycleModes`：支持 `Persistent` / `Ephemeral` 等

> 规范：CSIDriver 对象应该在 driver 的部署清单里 **显式创建**，并在整个 driver 生命周期内保持存在。

### 3.2 `CSINode` 对象：nodeID、可用性、拓扑 keys

`CSINode` 用于承载 node 维度的 CSI 信息，典型包括：

- `nodeID`：Kubernetes nodeName 与 CSI nodeID 的映射（来自 `NodeGetInfo`）
- driver 是否在该节点已注册（可用性信号）
- topology keys：该 driver 在此 node 上声明的拓扑 key 列表

---

## 4. Topology 设计规范（强烈建议掌握）

Topology 用于解决“卷不是所有节点都可访问”的问题，本地盘/本地目录类存储通常必须使用（至少用于把卷约束在某个节点上）。

### 4.1 CSI Driver 侧需要做什么

要支持 Topology，官方要求：

- Driver 宣告 Plugin capability：`VOLUME_ACCESSIBILITY_CONSTRAINTS`
- `NodeGetInfoResponse` 填充 `accessible_topology`
  - 这些 key/value 会用于：
    - 填充 `CSINode` 对象
    - 并把 topology labels 写到 Node 上
- `CreateVolumeRequest.accessibility_requirements` 会携带拓扑约束

### 4.2 `WaitForFirstConsumer` 的意义（本地卷强依赖）

在 `StorageClass.volumeBindingMode` 中：

- `Immediate`：external-provisioner 会把“全量拓扑集合”传给 `CreateVolume`（更像“预先建卷”）
- `WaitForFirstConsumer`：external-provisioner 会等待 scheduler 先选定节点，然后把该节点拓扑放到 `preferred` 的第一项，再调用 `CreateVolume`（更适合 LocalPV/对节点有强约束的存储）

### 4.3 拓扑 key 命名建议（避坑）

拓扑 key 最好使用 **自有域名** 前缀，避免使用 `kubernetes.io/`、`k8s.io/`、`topology.kubernetes.io/` 这类可能被集群策略/NodeRestriction 限制写入的命名空间。

示例：

- ✅ `example.com/zone`
- ✅ `localpv.csi.cloudnative.io/node`
- ❌ `topology.kubernetes.io/hostname`（在一些集群中会被禁止由 node 更新）

---

## 5. 开发流程（从 0 到可发布的规范步骤）

下面给出一个“从需求到上线”的标准流程，建议按顺序执行：

1) **需求分解**：确定需要哪些能力
   - 动态供给？（`external-provisioner`）
   - Attach/Detach？（`external-attacher`）
   - 扩容？（`external-resizer`）
   - 快照/恢复？（`external-snapshotter` + snapshot controller）
   - 拓扑约束？（Topology + `WaitForFirstConsumer`）

2) **CSI 接口设计**：确定要实现的 CSI Services/RPC 与 capability 宣告
   - Identity：`GetPluginInfo` / `GetPluginCapabilities` / `Probe`
   - Controller：按功能实现 `CreateVolume/DeleteVolume`、`ControllerPublish/Unpublish`、`ControllerExpandVolume`、`CreateSnapshot/...`
   - Node：按介质实现 `NodeGetInfo`、`NodeStage/Unstage`、`NodePublish/Unpublish`

3) **Sidecar 选型**：按 capability 装配 sidecar（见第 2 节）

4) **部署设计**：
   - Controller Plugin（Deployment/StatefulSet）与 Node Plugin（DaemonSet）拆分
   - Socket 目录：
     - Pod 内：`emptyDir`（sidecar ↔ driver）
     - HostPath：`/var/lib/kubelet/plugins/<driver-name>`（kubelet ↔ driver）
     - HostPath：`/var/lib/kubelet/plugins_registry`（registrar ↔ kubelet）
   - Node 插件挂载传播：`Bidirectional`
   - privileged 与 mount propagation 保障

5) **Kubernetes 对象与行为开关**：
   - `CSIDriver`：明确 `attachRequired/podInfoOnMount/fsGroupPolicy/...`
   - `StorageClass`：尤其 LocalPV 场景建议 `WaitForFirstConsumer`

6) **RBAC**：
   - controller sidecar 需要 watch/patch PV/PVC/VolumeAttachment/Snapshot CRD 等（以各 sidecar repo 的示例为准）
   - 避免给 driver 容器过大权限；能给 sidecar 就别给 driver

7) **可观测性与稳定性**：
   - 日志：区分 driver 与 sidecar，设置合理 `-v`
   - 健康：部署 `livenessprobe`
   - 关键 RPC 需要幂等、可重试、合理返回 gRPC code（sidecar/控制器会据此重试）

8) **测试与发布**：
   - 单元测试：driver 内部逻辑
   - 功能测试：PVC/PV/POD 全链路（含删除/重建/节点重启）
   - 版本兼容：按官方文档的 sidecar “Supported Versions” 表选择与集群版本匹配的镜像

---

## 6. 与本仓库 `pkg/localpv` 的对应关系（便于落地）

当前 localpv driver 的关键设计点：

- 通过 Topology 将卷绑定到节点：
  - 拓扑 key：`localpv.csi.cloudnative.io/node`
  - `StorageClass.volumeBindingMode: WaitForFirstConsumer`
- `CSIDriver.spec.attachRequired: false`（跳过 attach/detach）
- 已包含的 sidecar：
  - `external-provisioner`（动态供给）
  - `node-driver-registrar`（kubelet 注册）
  - `livenessprobe`（健康）
- 若未来要支持：
  - 快照：补齐 Snapshot RPC + 部署 `external-snapshotter` + snapshot controller
  - 扩容：补齐 Expand RPC + 部署 `external-resizer`
  - attach：补齐 Publish/Unpublish + 部署 `external-attacher` + 将 `attachRequired` 置为 true

---

## 7. 参考（官方文档）

以下链接均来自 Kubernetes CSI Developer Documentation（建议从这里查版本表与参数细节）：

- Sidecar Containers：https://kubernetes-csi.github.io/docs/sidecar-containers.html
- Deploying CSI Driver：https://kubernetes-csi.github.io/docs/deploying.html
- external-provisioner：https://kubernetes-csi.github.io/docs/external-provisioner.html
- external-attacher：https://kubernetes-csi.github.io/docs/external-attacher.html
- external-resizer：https://kubernetes-csi.github.io/docs/external-resizer.html
- external-snapshotter：https://kubernetes-csi.github.io/docs/external-snapshotter.html
- node-driver-registrar：https://kubernetes-csi.github.io/docs/node-driver-registrar.html
- livenessprobe：https://kubernetes-csi.github.io/docs/livenessprobe.html
- CSIDriver Object：https://kubernetes-csi.github.io/docs/csi-driver-object.html
- CSINode Object：https://kubernetes-csi.github.io/docs/csi-node-object.html
- Topology：https://kubernetes-csi.github.io/docs/topology.html
- Skip Attach：https://kubernetes-csi.github.io/docs/skip-attach.html

