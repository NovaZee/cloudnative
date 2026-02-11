# LocalPV CSI Driver (cloudnative demo)

一个最小可用的 **LocalPV CSI Driver**，用“本地目录”实现动态供给，并提供 **超分/超卖（thin-provision）** 的容量核算逻辑。

## 功能

- **LocalPV 动态供给**：每个 Volume 对应节点本地 `baseDir/volumes/<volumeID>` 目录
- **WaitForFirstConsumer**：PVC 先等 Pod 选定节点后再创建本地目录
- **超分/超卖**：按 `ConfigMap` 配置的 `overprovisionRatio` 进行容量核算（允许逻辑申请总量 > 物理容量）
- **热更新**：监听 `ConfigMap` 变更，运行时自动生效（影响后续 CreateVolume）
- **拓扑约束**：使用自定义拓扑 key `localpv.csi.cloudnative.io/node`（避免写入受保护的 `topology.kubernetes.io/*` 标签）

## 快速开始（kind/本地集群）

1) 构建镜像并加载进 kind：

```bash
docker build -t localpv-csi:dev -f pkg/localpv/Dockerfile .
kind load docker-image localpv-csi:dev --name lab
```

如果你看到 `exec format error`（常见于 Apple Silicon/kind 或混合架构集群），通常是 **镜像架构与节点架构不匹配**：

```bash
kubectl get node -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.nodeInfo.architecture}{"\n"}{end}'
```

在 arm64 节点上需要 arm64 镜像（或多架构镜像）；如果你曾经手动 `docker pull`/`kind load` 过 sidecar 镜像，建议用正确平台重新拉取并覆盖：

```bash
docker pull --platform=linux/arm64 registry.k8s.io/sig-storage/csi-provisioner:v3.5.0
docker pull --platform=linux/arm64 registry.k8s.io/sig-storage/csi-node-driver-registrar:v2.8.0
docker pull --platform=linux/arm64 registry.k8s.io/sig-storage/livenessprobe:v2.10.0
kind load docker-image registry.k8s.io/sig-storage/csi-provisioner:v3.5.0 --name lab
```

2) 部署 CSI Driver + StorageClass：

```bash
kubectl apply -f pkg/localpv/config/deploy.yaml
kubectl -n localpv-system get pods -o wide
```

3) 创建 PVC + Pod 进行验证：

```bash
kubectl apply -f pkg/localpv/config/example.yaml
kubectl get pvc,pod -o wide
```

## 超分配置（ConfigMap）

默认监听 `localpv-system/localpv-overprovision` 的 `config.yaml`：

```yaml
defaultOverprovisionRatio: 1.0
defaultReservedBytes: 1Gi
pools:
  default:
    overprovisionRatio: 2.0
    reservedBytes: 5Gi
```

更新后可直接生效（driver 会日志提示 reload）：

```bash
kubectl -n localpv-system edit cm localpv-overprovision
```
