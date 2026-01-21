# Volcano GPU 资源管理

> 作者：Cloud Native Team
> 更新时间：2025-01-20

---

## 目录

1. [GPU 资源概述](#1-gpu-资源概述)
2. [Volcano GPU 管理机制](#2-volcano-gpu-管理机制)
3. [GPU 调度策略](#3-gpu-调度策略)
4. [配置方式](#4-配置方式)
5. [实战案例](#5-实战案例)
6. [性能优化](#6-性能优化)

---

## 1. GPU 资源概述

### 1.1 Kubernetes GPU 管理

Kubernetes 通过设备插件（Device Plugin）机制管理 GPU 资源：

```
┌───────────────────────────────��─────────────────────────────────┐
│              Kubernetes GPU 管理架构                             │
└─────────────────────────────────────────────────────────────────┘

  ┌─────────────────────────────────────────────────────────────┐
  │                     Kubernetes API Server                   │
  └────────────────────────────┬────────────────────────────────┘
                               │
                               ▼
  ┌─────────────────────────────────────────────────────────────┐
  │                    Scheduler                                 │
  │  - 识别 nvidia.com/gpu 资源                                  │
  │  - 根据 Pod requests 进行调度                               │
  └────────────────────────────┬────────────────────────────────┘
                               │
                               ▼
  ┌─────────────────────────────────────────────────────────────┐
  │                    Kubelet                                   │
  │  ┌──────────────────────────────────────────────────────┐  │
  │  │         NVIDIA Device Plugin                          │  │
  │  │  - 检测 GPU 设备                                       │  │
  │  │  - 上报资源到 API Server                               │  │
  │  │  - 分配 GPU 给 Pod                                     │  │
  │  └──────────────────────────────────────────────────────┘  │
  │                              │                               │
  │                              ▼                               │
  │  ┌──────────────────────────────────────────────────────┐  │
  │  │         Container Runtime (dockerd/containerd)        │  │
  │  │  - 挂载 GPU 设备                                      │  │
  │  │  - 设置 NVIDIA_VISIBLE_DEVICES                        │  │
  │  └──────────────────────────────────────────────────────┘  │
  └─────────────────────────────────────────────────────────────┘
                               │
                               ▼
  ┌─────────────────────────────────────────────────────────────┐
  │                    Pod with GPU                              │
  │  ┌──────────────────────────────────────────────────────┐  │
  │  │  Container                                           │  │
  │  │    - GPU 0, GPU 1 (设备透传)                          │  │
  │  │    - NVIDIA Driver                                   │  │
  │  │    - CUDA Toolkit                                    │  │
  │  └──────────────────────────────────────────────────────┘  │
  └─────────────────────────────────────────────────────────────┘
```

### 1.2 GPU 资源类型

| 资源名称 | 说明 | 典型值 |
|----------|------|--------|
| **nvidia.com/gpu** | NVIDIA GPU 整卡 | 1, 2, 4, 8 |
| **nvidia.com/mig-1g.5gb** | MIG 实例 (1 GPU, 5GB) | 1-7 |
| **amd.com/gpu** | AMD GPU | 1, 2, 4 |
| **intel.com/gpu** | Intel GPU | 1 |

### 1.3 GPU 分配模式

```
┌─────────────────────────────────────────────────────────────────┐
│                    GPU 分配模式对比                              │
└─────────────────────────────────────────────────────────────────┘

  1. 整卡模式 (Native)
  ┌─────────────────────────────────────────────────────────────┐
  │                                                             │
  │  GPU 0          GPU 1          GPU 2          GPU 3        │
  │  ┌─────────┐    ┌─────────┐    ┌─────────┐    ┌─────────┐  │
  │  │  Pod A  │    │  Pod A  │    │  Pod B  │    │  Idle   │  │
  │  │(整卡独占)│    │(整卡独占)│    │(整卡独占)│    │         │  │
  │  └─────────┘    └─────────┘    └─────────┘    └─────────┘  │
  │                                                             │
  │  优点：性能最好，无干扰                                     │
  │  缺点：资源利用率低                                         │
  └─────────────────────────────────────────────────────────────┘

  2. 共享模式 (vGPU/MIG)
  ┌─────────────────────────────────────────────────────────────┐
  │                                                             │
  │  GPU 0                      GPU 1                          │
  │  ┌────┬────┬────┬────┐     ┌────┬────┬────┬────┐          │
  │  │ A1 │ A2 │ B1 │ C1 │     │ A3 │ A4 │ B2 │ C2 │          │
  │  │25% │25% │25% │25% │     │25% │25% │25% │25% │          │
  │  └────┴────┴────┴────┘     └────┴────┴────┴────┘          │
  │                                                             │
  │  优点：资源利用率高，成本降低                               │
  │  缺点：存在性能干扰，需要隔离                               │
  └─────────────────────────────────────────────────────────────┘

  3. 时间切片模式 (Time-Slicing)
  ┌─────────────────────────────────────────────────────────────┐
  │                                                             │
  │  GPU 0 (Time-Sliced)                                       │
  │  ┌─────────────────────────────────────────────────────┐   │
  │  │ T1: Pod A │ T2: Pod B │ T3: Pod C │ T4: Pod A │ ... │   │
  │  └─────────────────────────────────────────────────────┘   │
  │                                                             │
  │  优点：无需特殊硬件支持                                     │
  │  缺点：性能受限，适合推理场景                              │
  └─────────────────────────────────────────────────────────────┘
```

---

## 2. Volcano GPU 管理机制

### 2.1 GPU 调度流程

```
┌─────────────────────────────────────────────────────────────────┐
│              Volcano GPU 调度流程                                │
└─────────────────────────────────────────────────────────────────┘

  Job 提交
     │
     ▼
  ┌─────────────────┐
  │  资源请求解析   │  nvidia.com/gpu: 4
  └────────┬────────┘
           │
           ▼
  ┌─────────────────┐
  │  节点筛选       │  过滤有足够 GPU 的节点
  │  Predicate      │
  └────────┬────────┘
           │
           ▼
  ┌─────────────────┐
  │  GPU 分配       │  分配具体的 GPU 设备
  │  Allocate       │  GPU 0,1,2,3
  └────────┬────────┘
           │
           ▼
  ┌─────────────────┐
  │  Gang Scheduling│  确保 Task 组同时调度
  │  minAvailable   │
  └────────┬────────┘
           │
           ▼
  ┌─────────────────┐
  │  Pod 创建       │  设置 NVIDIA_VISIBLE_DEVICES
  └────────┬────────┘
           │
           ▼
  ┌─────────────────┐
  │  容器启动       │  GPU 设备透传
  └─────────────────┘
```

### 2.2 Volcano GPU 特性

| 特性 | 说明 | 配置方式 |
|------|------|----------|
| **Gang Scheduling** | 所有 Pod 同时获得 GPU | `minAvailable` |
| **GPU 共享** | 多个 Pod 共享同一 GPU | `volcano.sh/gpu-memory` |
| **GPU 编号** | 指定具体 GPU 设备 | `volcano.sh/gpu-id` |
| **NUMA 亲和** | GPU 与 CPU NUMA 对齐 | Node Affinity |
| **拓扑感知** | 考虑 PCIe/NVLink 拓扑 | `volcano.sh/gpu-topology` |

### 2.3 相关 CRD

#### PodGroup GPU 资源

```yaml
apiVersion: scheduling.volcano.sh/v1alpha1
kind: PodGroup
metadata:
  name: gpu-job
spec:
  minMember: 4
  minResources:
    nvidia.com/gpu: "4"    # 总共需要 4 个 GPU
    cpu: "16"
    memory: "64Gi"
  queue: gpu-queue
```

---

## 3. GPU 调度策略

### 3.1 整卡调度

最基础的 GPU 调度方式，每个 Pod 独占整张 GPU 卡。

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: gpu-whole-card
spec:
  schedulerName: volcano
  minAvailable: 2
  tasks:
    - name: task
      replicas: 2
      template:
        spec:
          containers:
            - name: main
              image: pytorch/pytorch:2.0-cuda11.7
              resources:
                requests:
                  nvidia.com/gpu: "1"  # 整卡
                limits:
                  nvidia.com/gpu: "1"
              command: ["python", "train.py"]
```

### 3.2 GPU 共享调度

通过 GPU 内存实现共享调度：

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: gpu-shared
spec:
  schedulerName: volcano
  minAvailable: 1
  tasks:
    - name: task
      replicas: 1
      template:
        spec:
          containers:
            - name: main
              image: pytorch/pytorch:2.0-cuda11.7
              resources:
                requests:
                  nvidia.com/gpu: "1"
                  # GPU 内存共享请求
                  volcano.sh/gpu-memory: "8000"  # 8GB
                limits:
                  nvidia.com/gpu: "1"
                  volcano.sh/gpu-memory: "8000"
              command: ["python", "inference.py"]
```

### 3.3 GPU 绑定调度

绑定特定的 GPU 设备：

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: gpu-specific
spec:
  schedulerName: volcano
  minAvailable: 1
  tasks:
    - name: task
      replicas: 1
      template:
        metadata:
          annotations:
            # 指定使用 GPU 0 和 GPU 1
            volcano.sh/gpu-id: "0,1"
        spec:
          containers:
            - name: main
              image: nvidia/cuda:11.7.1-base-ubuntu20.04
              resources:
                requests:
                  nvidia.com/gpu: "2"
                limits:
                  nvidia.com/gpu: "2"
              command: ["nvidia-smi"]
```

---

## 4. 配置方式

### 4.1 基础 GPU 作业

#### 单 GPU 作业

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: single-gpu-job
spec:
  schedulerName: volcano
  minAvailable: 1
  queue: gpu-queue
  tasks:
    - name: training
      replicas: 1
      template:
        spec:
          restartPolicy: OnFailure
          containers:
            - name: main
              image: pytorch/pytorch:2.0-cuda11.7-cudnn8-runtime
              resources:
                requests:
                  nvidia.com/gpu: "1"
                  cpu: "4"
                  memory: "16Gi"
                limits:
                  nvidia.com/gpu: "1"
                  cpu: "8"
                  memory: "32Gi"
              command:
                - python
                - train.py
                - --batch-size
                - "32"
                - --epochs
                - "100"
```

#### 多 GPU 作业（单机多卡）

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: multi-gpu-single-node
spec:
  schedulerName: volcano
  minAvailable: 1
  queue: gpu-queue
  tasks:
    - name: ddp-training
      replicas: 1
      template:
        spec:
          restartPolicy: OnFailure
          containers:
            - name: main
              image: pytorch/pytorch:2.0-cuda11.7-cudnn8-runtime
              resources:
                requests:
                  nvidia.com/gpu: "4"  # 单机 4 卡
                  cpu: "16"
                  memory: "64Gi"
                limits:
                  nvidia.com/gpu: "4"
                  cpu: "32"
                  memory: "128Gi"
              # PyTorch DDP 启动命令
              command:
                - torchrun
                - --nproc_per_node=4
                - train.py
                - --batch-size
                - "128"
              env:
                - name: CUDA_VISIBLE_DEVICES
                  value: "0,1,2,3"
              # 验证 GPU 可见性
              lifecycle:
                postStart:
                  exec:
                    command:
                      - sh
                      - -c
                      - |
                        echo "=== GPU Devices ==="
                        nvidia-smi
                        echo "=== CUDA_VISIBLE_DEVICES ==="
                        echo $CUDA_VISIBLE_DEVICES
```

#### 分布式 GPU 作业（多机多卡）

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: distributed-gpu-job
spec:
  schedulerName: volcano
  minAvailable: 8  # 4 个节点 × 2 个 Worker
  queue: gpu-queue
  plugins:
    # 启用服务插件，方便 Worker 之间通信
    svc: []
  tasks:
    # Master 节点
    - name: master
      replicas: 1
      policies:
        - event: TaskCompleted
          action: CompleteJob
      template:
        spec:
          restartPolicy: OnFailure
          subdomain: executor
          containers:
            - name: master
              image: pytorch/pytorch:2.0-cuda11.7-cudnn8-runtime
              resources:
                requests:
                  nvidia.com/gpu: "1"
                  cpu: "4"
                  memory: "16Gi"
              command:
                - torchrun
                - --nnodes=4
                - --nproc_per_node=2
                - --rdzv_id=job1
                - --rdzv_backend=c10d
                - --rdzv_endpoint=$JOB_NAME-master-0.$JOB_NAME:29500
                - train.py

    # Worker 节点
    - name: worker
      replicas: 4
      template:
        spec:
          restartPolicy: OnFailure
          subdomain: executor
          containers:
            - name: worker
              image: pytorch/pytorch:2.0-cuda11.7-cudnn8-runtime
              resources:
                requests:
                  nvidia.com/gpu: "2"  # 每个 Worker 2 卡
                  cpu: "8"
                  memory: "32Gi"
              command:
                - torchrun
                - --nnodes=4
                - --nproc_per_node=2
                - --rdzv_id=job1
                - --rdzv_backend=c10d
                - --rdzv_endpoint=$JOB_NAME-master-0.$JOB_NAME:29500
                - train.py
```

### 4.2 GPU 拓扑感知调度

#### NVLink 感知

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: nvlink-aware-job
spec:
  schedulerName: volcano
  minAvailable: 1
  tasks:
    - name: training
      replicas: 1
      template:
        spec:
          affinity:
            # 选择有 NVLink 的节点
            nodeAffinity:
              requiredDuringSchedulingIgnoredDuringExecution:
                nodeSelectorTerms:
                  - matchExpressions:
                      - key: nvidia.com/NVLINK
                        operator: In
                        values:
                          - "true"
          containers:
            - name: main
              image: pytorch/pytorch:2.0-cuda11.7
              resources:
                requests:
                  nvidia.com/gpu: "4"
                limits:
                  nvidia.com/gpu: "4"
```

#### PCIe 拓扑感知

```yaml
# 节点标注 PCIe 拓扑
apiVersion: v1
kind: Node
metadata:
  name: gpu-node-1
  labels:
    # GPU 0-1 在同一个 PCIe root complex
    volcano.sh/gpu-group-0: "0,1"
    # GPU 2-3 在同一个 PCIe root complex
    volcano.sh/gpu-group-1: "2,3"
    # GPU 0-3 通过 NVLink 连接
    volcano.sh/gpu-nvlink: "0,1,2,3"
---
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: topology-aware-job
spec:
  schedulerName: volcano
  minAvailable: 1
  tasks:
    - name: training
      replicas: 1
      template:
        metadata:
          annotations:
            # 优先分配在同一 PCIe 组的 GPU
            volcano.sh/gpu-topology-policy: "pcie"
        spec:
          containers:
            - name: main
              image: pytorch/pytorch:2.0-cuda11.7
              resources:
                requests:
                  nvidia.com/gpu: "2"
                limits:
                  nvidia.com/gpu: "2"
```

### 4.3 GPU 共享配置

#### 内存比例共享

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: gpu-memory-share
spec:
  schedulerName: volcano
  minAvailable: 4
  tasks:
    - name: inference
      replicas: 4
      template:
        spec:
          containers:
            - name: main
              image: tensorflow/tensorflow:2.12.0-gpu
              resources:
                requests:
                  nvidia.com/gpu: "1"
                  # 请求 1/4 的 GPU 内存
                  volcano.sh/gpu-memory: "5000"  # 5GB / 20GB
                limits:
                  nvidia.com/gpu: "1"
                  volcano.sh/gpu-memory: "5000"
              # 使用 CUDA MPS 或 vGPU
              command: ["python", "inference.py"]
```

#### GPU 切片配置

```yaml
# ConfigMap: NVIDIA GPU 设备插件配置
apiVersion: v1
kind: ConfigMap
metadata:
  name: gpu-device-plugin-config
  namespace: kube-system
data:
  config.yaml: |
    version: v1
    sharing:
      timeSlicing:
        renameByDefault: false
        failRequestsGreaterThanOne: true
        resources:
          - name: nvidia.com/gpu
            replicas: 4  # 每个 GPU 切片为 4 个 vGPU
---
# 使用切片 GPU 的 Job
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: gpu-slice-job
spec:
  schedulerName: volcano
  minAvailable: 1
  tasks:
    - name: task
      replicas: 1
      template:
        spec:
          containers:
            - name: main
              image: python:3.9
              resources:
                requests:
                  nvidia.com/gpu: "1"  # 实际使用 1/4 GPU
                limits:
                  nvidia.com/gpu: "1"
              command: ["python", "gpu_task.py"]
```

---

## 5. 实战案例

### 5.1 案例1：单机多卡 PyTorch 训练

#### 场景描述
在单个节点上使用 4 张 GPU 进行分布式训练，使用 PyTorch DDP。

#### 完整配置

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: training-config
  namespace: ml
data:
  train.py: |
    import os
    import torch
    import torch.distributed as dist
    from torch.nn.parallel import DistributedDataParallel as DDP
    from torch.utils.data import DataLoader
    from torchvision import datasets, transforms

    def setup():
        """初始化分布式训练环境"""
        dist.init_process_group(backend="nccl")
        local_rank = int(os.environ.get("LOCAL_RANK"))
        torch.cuda.set_device(local_rank)
        return local_rank

    def cleanup():
        """清理分布式训练环境"""
        dist.destroy_process_group()

    def main():
        local_rank = setup()
        rank = dist.get_rank()
        world_size = dist.get_world_size()

        print(f"Rank {rank}/{world_size} initialized on GPU {local_rank}")

        # 创建模型并移动到 GPU
        model = torch.nn.Linear(784, 10).cuda(local_rank)
        ddp_model = DDP(model, device_ids=[local_rank])

        # 数据集
        transform = transforms.Compose([
            transforms.ToTensor(),
            transforms.Normalize((0.1307,), (0.3081,))
        ])
        dataset = datasets.MNIST('./data', train=True, download=True, transform=transform)
        sampler = torch.utils.data.distributed.DistributedSampler(dataset)
        loader = DataLoader(dataset, batch_size=32, sampler=sampler)

        # 优化器
        optimizer = torch.optim.Adam(ddp_model.parameters(), lr=0.001)

        # 训练循环
        for epoch in range(10):
            sampler.set_epoch(epoch)
            for batch_idx, (data, target) in enumerate(loader):
                data, target = data.cuda(local_rank), target.cuda(local_rank)
                optimizer.zero_grad()
                output = ddp_model(data.view(-1, 784))
                loss = torch.nn.functional.cross_entropy(output, target)
                loss.backward()
                optimizer.step()

                if batch_idx % 100 == 0 and rank == 0:
                    print(f"Epoch {epoch}, Batch {batch_idx}, Loss: {loss.item()}")

        if rank == 0:
            torch.save(ddp_model.state_dict(), "model.pth")

        cleanup()

    if __name__ == "__main__":
        main()
---
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: pytorch-ddp-training
  namespace: ml
spec:
  schedulerName: volcano
  minAvailable: 1
  queue: gpu-queue
  tasks:
    - name: ddp-worker
      replicas: 1
      template:
        spec:
          restartPolicy: OnFailure
          volumes:
            - name: data
              emptyDir: {}
            - name: config
              configMap:
                name: training-config
          containers:
            - name: training
              image: pytorch/pytorch:2.0-cuda11.7-cudnn8-runtime
              resources:
                requests:
                  nvidia.com/gpu: "4"
                  cpu: "16"
                  memory: "64Gi"
                limits:
                  nvidia.com/gpu: "4"
                  cpu: "32"
                  memory: "128Gi"
              volumeMounts:
                - name: data
                  mountPath: /data
                - name: config
                  mountPath: /app
              workingDir: /app
              command:
                - torchrun
                - --nproc_per_node=4
                - train.py
              env:
                - name: CUDA_DEVICE_MAX_CONNECTIONS
                  value: "1"
                - name: NCCL_IB_DISABLE
                  value: "0"
                - name: NCCL_SOCKET_IFNAME
                  value: "eth0"
              # 启动后验证 GPU
              lifecycle:
                postStart:
                  exec:
                    command:
                      - sh
                      - -c
                      - |
                        echo "=== GPU Status ==="
                        nvidia-smi
                        echo ""
                        echo "=== CUDA Devices ==="
                        python -c "import torch; print(f'CUDA available: {torch.cuda.is_available()}'); print(f'CUDA devices: {torch.cuda.device_count()}')"
```

#### 验证与监控

```bash
# 1. 查看 Job 状态
kubectl -n ml get vcjob pytorch-ddp-training
kubectl -n ml describe vcjob pytorch-ddp-training

# 2. 查看 GPU 使用情况
kubectl -n ml exec -it pytorch-ddp-training-ddp-worker-0 -- nvidia-smi

# 3. 查看 NCCL 通信
kubectl -n ml exec -it pytorch-ddp-training-ddp-worker-0 -- bash -c \
  'NCCL_DEBUG=INFO python -c "import torch; torch.distributed.init_process_group(backend=\"nccl\")"'

# 4. 监控训练日志
kubectl -n ml logs -f pytorch-ddp-training-ddp-worker-0

# 5. 查看性能统计
kubectl -n ml exec -it pytorch-ddp-training-ddp-worker-0 -- nvidia-smi dmon -s u -c 10
```

### 5.2 案例2：分布式 TensorFlow 训练

#### 场景描述
使用 TensorFlow 2.x 的 MultiWorkerMirroredStrategy 进行多节点分布式训练。

#### 完整配置

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: tf-training-script
  namespace: ml
data:
  train.py: |
    import os
    import tensorflow as tf
    import json

    # 获取 TF_CONFIG
    tf_config = json.loads(os.environ.get('TF_CONFIG'))
    task_type = tf_config['task']['type']
    task_index = tf_config['task']['index']

    print(f"Starting {task_type}:{task_index}")

    # 定义策略
    strategy = tf.distribute.MultiWorkerMirroredStrategy()
    print(f"Number of devices: {strategy.num_replicas_in_sync}")

    # 构建模型
    with strategy.scope():
        model = tf.keras.Sequential([
            tf.keras.layers.Conv2D(32, 3, activation='relu', input_shape=(28, 28, 1)),
            tf.keras.layers.MaxPooling2D(),
            tf.keras.layers.Flatten(),
            tf.keras.layers.Dense(128, activation='relu'),
            tf.keras.layers.Dense(10)
        ])
        model.compile(
            optimizer='adam',
            loss=tf.keras.losses.SparseCategoricalCrossentropy(from_logits=True),
            metrics=['accuracy']
        )

    # 加载数据
    (x_train, y_train), _ = tf.keras.datasets.mnist.load_data()
    x_train = x_train[..., tf.newaxis] / 255.0

    # 训练
    model.fit(x_train, y_train, epochs=10, batch_size=64)

    # 保存模型（仅 chief worker）
    if task_type == 'worker' and task_index == 0:
        model.save('/tmp/model')
        print("Model saved")
---
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: tensorflow-distributed
  namespace: ml
spec:
  schedulerName: volcano
  minAvailable: 6  # 1 PS + 5 Workers
  queue: gpu-queue
  plugins:
    svc: []
  tasks:
    # Parameter Server
    - name: ps
      replicas: 1
      template:
        spec:
          restartPolicy: OnFailure
          containers:
            - name: ps
              image: tensorflow:2.12.0-gpu
              resources:
                requests:
                  cpu: "4"
                  memory: "8Gi"
                limits:
                  cpu: "8"
                  memory: "16Gi"
              command: ["python", "-m", "tensorflow.python.distribute.parameter_server_training"]

    # Workers
    - name: worker
      replicas: 5
      template:
        metadata:
          annotations:
            # 优先将 Worker 调度到有足够 GPU 的节点
            volcano.sh/gpu-number: "2"
        spec:
          restartPolicy: OnFailure
          subdomain: tensorflow-distributed
          containers:
            - name: worker
              image: tensorflow:2.12.0-gpu
              resources:
                requests:
                  nvidia.com/gpu: "2"
                  cpu: "8"
                  memory: "32Gi"
                limits:
                  nvidia.com/gpu: "2"
                  cpu: "16"
                  memory: "64Gi"
              env:
                - name: TF_CONFIG
                  value: |
                    {
                      "cluster": {
                        "ps": ["tensorflow-distributed-ps-0.tensorflow-distributed:2222"],
                        "worker": [
                          "tensorflow-distributed-worker-0.tensorflow-distributed:2222",
                          "tensorflow-distributed-worker-1.tensorflow-distributed:2222",
                          "tensorflow-distributed-worker-2.tensorflow-distributed:2222",
                          "tensorflow-distributed-worker-3.tensorflow-distributed:2222",
                          "tensorflow-distributed-worker-4.tensorflow-distributed:2222"
                        ]
                      },
                      "task": {"type": "worker", "index": ${TASK_INDEX}}
                    }
              command:
                - sh
                - -c
                - |
                  # 设置 TF_CONFIG 环境变量
                  export TF_CONFIG=$(cat <<EOF
                  {
                    "cluster": {
                      "ps": ["tensorflow-distributed-ps-0.tensorflow-distributed:2222"],
                      "worker": [
                        "tensorflow-distributed-worker-0.tensorflow-distributed:2222",
                        "tensorflow-distributed-worker-1.tensorflow-distributed:2222",
                        "tensorflow-distributed-worker-2.tensorflow-distributed:2222",
                        "tensorflow-distributed-worker-3.tensorflow-distributed:2222",
                        "tensorflow-distributed-worker-4.tensorflow-distributed:2222"
                      ]
                    },
                    "task": {"type": "worker", "index": ${TASK_INDEX}}
                  }
                  EOF
                  )

                  # 验证 GPU
                  echo "=== GPU Status ==="
                  nvidia-smi

                  # 运行训练
                  python /app/train.py
              volumeMounts:
                - name: script
                  mountPath: /app
          volumes:
            - name: script
              configMap:
                name: tf-training-script
```

### 5.3 案例3：GPU 推理服务

#### 场景描述
部署高并发 GPU 推理服务，使用 GPU 共享提高资源利用率。

#### 完整配置

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: inference-service
  namespace: inference
data:
  inference.py: |
    import torch
    from fastapi import FastAPI
    from pydantic import BaseModel
    import numpy as np
    import uvicorn
    import os

    app = FastAPI()

    # 加载模型
    device = torch.device("cuda")
    model = torch.jit.load("/models/model.pt").to(device)
    model.eval()

    class Input(BaseModel):
        data: list

    @app.post("/predict")
    async def predict(input: Input):
        # 预处理
        x = torch.tensor(input.data).unsqueeze(0).to(device)

        # 推理
        with torch.no_grad():
            output = model(x)

        return {"prediction": output.cpu().numpy().tolist()}

    @app.get("/health")
    async def health():
        return {"status": "healthy"}

    if __name__ == "__main__":
        # 限制工作进程数（每个使用部分 GPU）
        num_workers = int(os.environ.get("NUM_WORKERS", "4"))
        uvicorn.run(app, host="0.0.0.0", port=8000, workers=num_workers)
---
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: gpu-inference-service
  namespace: inference
spec:
  schedulerName: volcano
  minAvailable: 1
  queue: gpu-queue
  tasks:
    - name: inference
      replicas: 1
      template:
        spec:
          restartPolicy: OnFailure
          containers:
            - name: inference
              image: pytorch/pytorch:2.0-cuda11.7-runtime
              resources:
                requests:
                  nvidia.com/gpu: "1"
                  volcano.sh/gpu-memory: "10000"  # 10GB 显存
                  cpu: "4"
                  memory: "8Gi"
                limits:
                  nvidia.com/gpu: "1"
                  volcano.sh/gpu-memory: "10000"
                  cpu: "8"
                  memory: "16Gi"
              ports:
                - containerPort: 8000
                  name: http
              env:
                - name: CUDA_VISIBLE_DEVICES
                  value: "0"
                - name: CUDA_DEVICE_MAX_CONNECTIONS
                  value: "4"
                - name: NUM_WORKERS
                  value: "4"
              command:
                - python
                - /app/inference.py
              volumeMounts:
                - name: model
                  mountPath: /models
                - name: app
                  mountPath: /app
          volumes:
            - name: model
              persistentVolumeClaim:
                claimName: model-pvc
            - name: app
              configMap:
                name: inference-service
---
apiVersion: v1
kind: Service
metadata:
  name: inference-service
  namespace: inference
spec:
  selector:
    volcano.sh/job-name: gpu-inference-service
  ports:
    - port: 80
      targetPort: 8000
      name: http
  type: LoadBalancer
```

### 5.4 案例4：HPC 混合精度训练

#### 场景描述
使用混合精度进行大规模深度学习训练，利用 Tensor Core 加速。

#### 完整配置

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: mixed-precision-training
  namespace: ml
spec:
  schedulerName: volcano
  minAvailable: 4  # 4 节点，每节点 4 GPU
  queue: gpu-queue
  tasks:
    - name: trainer
      replicas: 4
      template:
        metadata:
          annotations:
            # 选择支持 Tensor Core 的 GPU（Volta, Turing, Ampere）
            volcano.sh/gpu-architecture: "volta,turing,ampere"
        spec:
          restartPolicy: OnFailure
          containers:
            - name: training
              image: pytorch/pytorch:2.0-cuda11.7-cudnn8-runtime
              resources:
                requests:
                  nvidia.com/gpu: "4"
                  cpu: "16"
                  memory: "64Gi"
                limits:
                  nvidia.com/gpu: "4"
                  cpu: "32"
                  memory: "128Gi"
              # Apex 混合精度训练
              command:
                - python
                - -m
                - apex.parallel.multiproc
                - --nccl
                - --world_size=4
                - train.py
                - --amp
                - --opt-level O1
                - --loss-scale=dynamic
                - --batch-size=256
              env:
                # AMP 配置
                - name: NCCL_LL_THRESHOLD
                  value: "0"
                - name: NCCL_ALGO
                  value: "Ring"
                # Tensor Core 优化
                - name: TORCH_EXTENSIONS_DIR
                  value: /tmp/torch_extensions
              volumeMounts:
                - name: data
                  mountPath: /data
                - name: cache
                  mountPath: /tmp/torch_extensions
          volumes:
            - name: data
              persistentVolumeClaim:
                claimName: training-data-pvc
            - name: cache
              emptyDir: {}
```

### 5.5 案例5：GPU 容器健康检查

#### 场景描述
为 GPU 作业配置完善的健康检查和监控。

#### 完整配置

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: gpu-with-healthcheck
  namespace: ml
spec:
  schedulerName: volcano
  minAvailable: 1
  queue: gpu-queue
  tasks:
    - name: training
      replicas: 1
      template:
        spec:
          restartPolicy: OnFailure
          volumes:
            - name: health-check
              configMap:
                name: gpu-health-script
          containers:
            - name: training
              image: pytorch/pytorch:2.0-cuda11.7
              resources:
                requests:
                  nvidia.com/gpu: "2"
                  cpu: "8"
                  memory: "32Gi"
                limits:
                  nvidia.com/gpu: "2"
                  cpu: "16"
                  memory: "64Gi"
              command:
                - python
                - train.py
              # 启动探针
              startupProbe:
                exec:
                  command:
                    - sh
                    - /scripts/health.sh
                initialDelaySeconds: 30
                periodSeconds: 10
                failureThreshold: 30
              # 存活探针
              livenessProbe:
                exec:
                  command:
                    - sh
                    - /scripts/health.sh
                initialDelaySeconds: 60
                periodSeconds: 30
                timeoutSeconds: 10
                failureThreshold: 3
              # 就绪探针
              readinessProbe:
                exec:
                  command:
                    - sh
                    - /scripts/ready.sh
                initialDelaySeconds: 10
                periodSeconds: 5
              volumeMounts:
                - name: health-check
                  mountPath: /scripts
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: gpu-health-script
  namespace: ml
data:
  health.sh: |
    #!/bin/bash
    set -e

    echo "=== GPU Health Check ==="

    # 检查 nvidia-smi 可用
    if ! command -v nvidia-smi &> /dev/null; then
        echo "ERROR: nvidia-smi not found"
        exit 1
    fi

    # 检查 GPU 设备
    GPU_COUNT=$(nvidia-smi --query-gpu=count --format=csv,noheader | head -1)
    if [ "$GPU_COUNT" -lt 1 ]; then
        echo "ERROR: No GPU devices found"
        exit 1
    fi

    # 检查 GPU 状态
    GPU_STATUS=$(nvidia-smi --query-gpu=temperature.gpu,utilization.gpu,memory.used,memory.total --format=csv,noheader)
    echo "GPU Status: $GPU_STATUS"

    # 检查 CUDA
    python -c "import torch; assert torch.cuda.is_available(), 'CUDA not available'" || exit 1

    # 检查进程
    if ! pgrep -f "train.py" > /dev/null; then
        echo "WARNING: Training process not running"
    fi

    echo "Health check passed"
    exit 0

  ready.sh: |
    #!/bin/bash
    set -e

    echo "=== GPU Readiness Check ==="

    # 检查 CUDA 可用
    python -c "
    import torch
    assert torch.cuda.is_available(), 'CUDA not available'
    print(f'CUDA devices: {torch.cuda.device_count()}')
    for i in range(torch.cuda.device_count()):
        props = torch.cuda.get_device_properties(i)
        print(f'  GPU {i}: {props.name}, {props.total_memory / 1024**3:.1f}GB')
    "

    # 检查训练进程
    if pgrep -f "train.py" > /dev/null; then
        echo "Training process running"
        exit 0
    else
        echo "Training process not ready"
        exit 1
    fi
```

---

## 6. 性能优化

### 6.1 GPU 调度优化

#### 节点选择策略

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: optimized-gpu-job
spec:
  schedulerName: volcano
  minAvailable: 1
  queue: gpu-queue
  tasks:
    - name: training
      replicas: 1
      template:
        spec:
          # 节点选择：选择相同 GPU 型号
          nodeSelector:
            nvidia.com/gpu.product: "NVIDIA_A100-SXM4-40GB"
          affinity:
            # Pod 反亲和：分散到不同节点
            podAntiAffinity:
              preferredDuringSchedulingIgnoredDuringExecution:
                - weight: 100
                  podAffinityTerm:
                    labelSelector:
                      matchLabels:
                        app: training
                    topologyKey: kubernetes.io/hostname
            # 节点亲和：选择有 NVLink 的节点
            nodeAffinity:
              requiredDuringSchedulingIgnoredDuringExecution:
                nodeSelectorTerms:
                  - matchExpressions:
                      - key: nvidia.com/NVLINK
                        operator: In
                        values:
                          - "true"
          containers:
            - name: training
              image: pytorch/pytorch:2.0-cuda11.7
              resources:
                requests:
                  nvidia.com/gpu: "4"
                limits:
                  nvidia.com/gpu: "4"
```

#### 资源预留

```yaml
# 节点资源预留
apiVersion: v1
kind: Node
metadata:
  name: gpu-node-1
  labels:
    # 预留 GPU 给高优先级作业
    volcano.sh/gpu-reserved: "true"
    volcano.sh/reserved-gpu-count: "2"
---
# 使用预留 GPU 的作业
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: priority-gpu-job
spec:
  schedulerName: volcano
  priorityClassName: high-priority
  minAvailable: 1
  queue: gpu-queue
  tasks:
    - name: training
      replicas: 1
      template:
        spec:
          nodeSelector:
            volcano.sh/gpu-reserved: "true"
          containers:
            - name: training
              image: pytorch/pytorch:2.0-cuda11.7
              resources:
                requests:
                  nvidia.com/gpu: "2"
                limits:
                  nvidia.com/gpu: "2"
```

### 6.2 通信优化

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: nccl-optimized-job
spec:
  schedulerName: volcano
  minAvailable: 4
  queue: gpu-queue
  tasks:
    - name: training
      replicas: 4
      template:
        spec:
          hostNetwork: true  # 使用主机网络
          hostPID: true     # 共享 PID namespace
          containers:
            - name: training
              image: pytorch/pytorch:2.0-cuda11.7
              resources:
                requests:
                  nvidia.com/gpu: "4"
                  # 使用 huge pages
                  hugepages-2mi: "2Gi"
                limits:
                  nvidia.com/gpu: "4"
                  hugepages-2mi: "2Gi"
              # NCCL 环境变量优化
              env:
                # 基础配置
                - name: NCCL_DEBUG
                  value: INFO
                - name: NCCL_IB_DISABLE
                  value: "0"
                - name: NCCL_SOCKET_IFNAME
                  value: "eth0"
                # 性能优化
                - name: NCCL_IB_HCA
                  value: "mlx5_0:1,mlx5_1:1"
                - name: NCCL_NET_GDR_LEVEL
                  value: "5"
                - name: NCCL_IB_GID_INDEX
                  value: "3"
                # NVLink 优化
                - name: NCCL_NVLS_ENABLE
                  value: "1"
                # 超时配置
                - name: NCCL_BLOCKING_WAIT
                  value: "1"
                - name: NCCL_TIMEOUT
                  value: "1800"
              # 共享内存配置
              volumeMounts:
                - name: dshm
                  mountPath: /dev/shm
          volumes:
            - name: dshm
              emptyDir:
                medium: Memory
                sizeLimit: 16Gi
```

### 6.3 监控与调试

```yaml
# GPU 监控 Prometheus 配置
apiVersion: v1
kind: ConfigMap
metadata:
  name: gpu-monitoring
  namespace: monitoring
data:
  gpu-metrics.yml: |
    groups:
      - name: gpu_metrics
        rules:
          # GPU 利用率
          - record: job:gpu_utilization_percent
            expr: |
              sum(rate(nvidia_gpu_utilization_gpu{job=~".*"}[5m])) by (job)

          # GPU 内存使用
          - record: job:gpu_memory_usage_bytes
            expr: |
              sum(nvidia_gpu_memory_used_bytes{job=~".*"}) by (job)

          # GPU 温度
          - record: job:gpu_temperature_celsius
            expr: |
              avg(nvidia_gpu_temperature_gpu{job=~"."}) by (job)

          # GPU 功耗
          - record: job:gpu_power_usage_watts
            expr: |
              sum(nvidia_gpu_power_draw_watts{job=~"."}) by (job)
---
# NVIDIA DCGM Exporter
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: nvidia-dcgm-exporter
  namespace: monitoring
spec:
  selector:
    matchLabels:
      app: dcgm-exporter
  template:
    metadata:
      labels:
        app: dcgm-exporter
    spec:
      hostNetwork: true
      containers:
        - name: dcgm-exporter
          image: nvidia/dcgm-exporter:3.1.8-3.1.5-ubuntu20.04
          resources:
            requests:
              cpu: "100m"
              memory: "100Mi"
            limits:
              cpu: "500m"
              memory: "500Mi"
          volumeMounts:
            - name: gpu-stats
              mountPath: /var/lib/kubelet/pod-resources
              readOnly: true
      volumes:
        - name: gpu-stats
          hostPath:
            path: /var/lib/kubelet/pod-resources
```

### 6.4 故障排查清单

| 问题 | 检查项 | 解决方案 |
|------|--------|----------|
| Pod 无法调度 | 检查节点 GPU 资源 | `kubectl describe node` |
| GPU 不可用 | 检查 Device Plugin | `kubectl logs -n kube-system nvidia-device-plugin` |
| 性能下降 | 检查 NCCL 配置 | 启用 IB/NVLink，调整参数 |
| 内存不足 | 检查 GPU 内存使用 | `nvidia-smi`，减少 batch size |
| 通信超时 | 检查网络配置 | `NCCL_DEBUG=INFO` 分析日志 |

---

## 附录

### A. 常用命令

```bash
# GPU 状态查询
nvidia-smi
nvidia-smi dmon -s u -c 10
nvidia-smi pmon -c 10

# GPU 监控
watch -n 1 nvidia-smi

# CUDA 测试
nvidia-smi --query-gpu=name,driver_version,memory.total --format=csv

# 查看作业 GPU 分配
kubectl get pods -o json | jq '.items[] | select(.spec.containers[].resources.requests["nvidia.com/gpu"]) | {name: .metadata.name, gpu: .spec.containers[0].resources.requests["nvidia.com/gpu"]}'

# 查看节点 GPU 资源
kubectl get nodes -o custom-columns=NAME:.metadata.name,GPU:.status.allocatable.nvidia\.com/gpu
```

### B. 性能测试工具

```bash
# GPU 基准测试
git clone https://github.com/violator-Adjusted/DL-Benchmarks.git
cd DL-Benchmarks
python run_benchmarks.py --gpu

# NCCL 测试
git clone https://github.com/NVIDIA/nccl-tests.git
cd nccl-tests
make MPI=1
mpirun -np 8 ./all_reduce_perf -b 8 -e 128M -f 2 -g 8

# PyTorch 基准测试
python -c "import torch; torch.backends.cudnn.benchmark=True; x = torch.randn(1000, 1000).cuda(); %timeit y = torch.matmul(x, x)"
```

### C. 参考资料

- **Volcano 官方文档**: https://volcano.sh/docs/
- **Kubernetes GPU 管理**: https://kubernetes.io/docs/tasks/manage-gpus-in-containers/
- **NVIDIA Device Plugin**: https://github.com/NVIDIA/k8s-device-plugin
- **NCCL 文档**: https://docs.nvidia.com/deeplearning/nccl/

---

**文档版本**: v1.0
**最后更新**: 2025-01-20