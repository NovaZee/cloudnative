# Volcano 指南

> 更新时间：2025-01-20

---

## 目录

1. [Volcano 概述](#1-volcano-概述)
2. [核心概念](#2-核心概念)
3. [安装部署](#3-安装部署)
4. [使用指南](#4-使用指南)
5. [实战案例](#5-实战案例)
6. [深入能力分析](#6-深入能力分析)

---

## 1. Volcano 概述

### 1.1 什么是 Volcano

Volcano 是一个基于 Kubernetes 的批处理系统，专为高性能计算（HPC）、AI/ML、大数据等场景设计。它起源于 Kubernetes 的批处理需求，解决了原生 Kubernetes 调度器在批处理场景下的局限性。

**核心定位**：
- Kubernetes 原生批处理调度器
- 支持大规模作业调度
- 提供 Gang Scheduling、公平调度、队列管理能力

### 1.2 为什么需要 Volcano

| 特性 | Kubernetes 原生调度器 | Volcano |
|------|---------------------|---------|
| Gang Scheduling | 不支持 | 原生支持 |
| 队列管理 | 无 | 完整的队列系统 |
| 作业优先级 | Limited Priority | 支持抢占、层级优先级 |
| 资源公平共享 | 不支持 | 支持多种公平策略 |
| 任务间亲和性 | Limited | 丰富的亲和/反亲和 |
| 作业生命周期管理 | 基础 | 完整（保留、重试、重启） |

### 1.3 架构设计

```
┌─────────────────────────────────────────────────────────────┐
│                        Volcano 架构                         │
├─────────────────────────────────────────────────────────────┤
│                                                               │
│  ┌──────────┐    ┌──────────┐    ┌──────────┐             │
│  │  kubectl │───▶│  API     │───▶│  Webhook │             │
│  │          │    │  Server  │    │          │             │
│  └──────────┘    └────┬─────┘    └──────────┘             │
│                      │                                       │
│                      ▼                                       │
│              ┌───────────────┐                              │
│              │    CRDs       │                              │
│              │ - Job         │                              │
│              │ - Queue       │                              │
│              │ - PodGroup    │                              │
│              └───────┬───────┘                              │
│                      │                                       │
│                      ▼                                       │
│              ┌─────────────────────┐                        │
│              │   Volcano Scheduler │                        │
│              ├─────────────────────┤                        │
│              │  - Session          │                        │
│              │  - Framework        │                        │
│              │  - Plugins          │                        │
│              │    * Gang           │                        │
│              │    * Priority       │                        │
│              │    * Fair Share     │                        │
│              │    * Task Topology  │                        │
│              └─────────────────────┘                        │
│                      │                                       │
│                      ▼                                       │
│              ┌─────────────────────┐                        │
│              │   Kubernetes API    │                        │
│              └─────────────────────┘                        │
└─────────────────────────────────────────────────────────────┘
```

---

## 2. 核心概念

### 2.1 CRD 资源

#### 2.1.1 Job

Volcano Job 是一个批处理作业的抽象，包含一个或多个 Task 组成的 DAG（有向无环图）。

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: hello-world
spec:
  minAvailable: 3           # 最小可用任务数（Gang Scheduling）
  schedulerName: volcano     # 使用 Volcano 调度器
  priorityClassName: high-priority
  queue: default             # 所属队列
  tasks:
    - name: task-1
      replicas: 2
      template:
        spec:
          containers:
            - name: main
              image: nginx
              resources:
                requests:
                  cpu: "1"
                  memory: "1Gi"
```

#### 2.1.2 PodGroup

PodGroup 是一组需要协同调度的 Pod 集合，是 Gang Scheduling 的基础单元。

```yaml
apiVersion: scheduling.volcano.sh/v1alpha1
kind: PodGroup
metadata:
  name: test-podgroup
spec:
  minMember: 3              # 最小成员数量
  minResources:             # 最小资源需求
    cpu: "4"
    memory: "8Gi"
  queue: default
  priorityClassName: high-priority
```

#### 2.1.3 Queue

Queue 是作业的逻辑分组，用于资源配额管理和公平调度。

```yaml
apiVersion: scheduling.volcano.sh/v1alpha1
kind: Queue
metadata:
  name: research-queue
spec:
  capability:
    cpu: "100"              # 队列最大 CPU 配额
    memory: "200Gi"         # 队列最大内存配额
  weight: 10                # 队列权重（用于公平调度）
  reclaimable: true         # 是否允许低优先级队列资源被抢占
```

### 2.2 调度流程

```
┌─────────────────────────────────────────────────────────────────┐
│                    Volcano 调度流程                              │
└─────────────────────────────────────────────────────────────────┘

  ┌─────────┐    ┌─────────┐    ┌─────────┐    ┌─────────┐
  │  未调度  │───▶│  队列中  │───▶│  调度中  │───▶│  运行中  │
  │ Pending │    │ Queuing │    │Pending  │    │Running  │
  └─────────┘    └─────────┘    └─────────┘    └─────────┘
       │              │              │              │
       │              │              │              │
       ▼              ▼              ▼              ▼
   ┌────────┐    ┌────────┐    ┌────────┐    ┌────────┐
   │ Inad-  │    │ Allo-  │    │ Assum- │    │ Com-   │
   │ missible│   │ cated │   │ ed     │   │ pleted │
   └────────┘    └────────┘    └────────┘    └────────┘

调度阶段详解:
1. UnschedulablePod: 检查 Pod 是否可调度
2. Queuing: 根据 Queue 配额和优先级排序
3. Pending: 执行调度算法
4. Running: Pod 实际运行
```

---

## 3. 安装部署

### 3.1 前置要求

- Kubernetes 1.16+
- kubectl 已配置
- 集群有足够的资源

### 3.2 安装方式

#### 方式一：Helm 安装（推荐）

```bash
# 添加 Volcano Helm 仓库
helm repo add volcano-sh https://volcano-sh.github.io/helm-charts

# 更新仓库
helm repo update

# 安装 Volcano
helm install volcano volcano-sh/volcano -n volcano-system --create-namespace

# 验证安装
kubectl get pods -n volcano-system
```

#### 方式二：kubectl 安装

```bash
# 下载安装 YAML
wget https://raw.githubusercontent.com/volcano-sh/volcano/master/installer/volcano-development.yaml

# 安装
kubectl apply -f volcano-development.yaml

# 验证
kubectl get pods -n volcano-system
```

### 3.3 验证安装

```bash
# 检查 CRDs
kubectl get crd | grep volcano

# 检查调度器
kubectl get deployment -n volcano-system

# 检查 Webhook
kubectl get validatingwebhookconfiguration
```

---

## 4. 使用指南

### 4.1 基础作业提交

#### 最小化作业示例

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: simple-job
spec:
  schedulerName: volcano
  minAvailable: 1
  tasks:
    - name: task
      replicas: 1
      template:
        spec:
          containers:
            - name: hello
              image: busybox
              command: ["echo", "Hello Volcano!"]
          restartPolicy: OnFailure
```

### 4.2 任务拓扑（DAG）

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: ml-pipeline
spec:
  schedulerName: volcano
  minAvailable: 3
  tasks:
    # 数据预处理
    - name: data-preprocess
      replicas: 1
      policies:
        - event: TaskCompleted
          action: CompleteJob
      template:
        spec:
          containers:
            - name: preprocess
              image: python:3.9
              command: ["python", "preprocess_data.py"]

    # 特征工程（依赖数据预处理）
    - name: feature-engineering
      replicas: 2
      dependsOn:
        - data-preprocess
      template:
        spec:
          containers:
            - name: features
              image: python:3.9
              command: ["python", "extract_features.py"]

    # 模型训练（依赖特征工程）
    - name: model-training
      replicas: 1
      dependsOn:
        - feature-engineering
      policies:
        - event: TaskCompleted
          action: CompleteJob
      template:
        spec:
          containers:
            - name: training
              image: pytorch/pytorch:2.0
              command: ["python", "train.py"]
              resources:
                requests:
                  cpu: "4"
                  memory: "16Gi"
                  nvidia.com/gpu: "1"
```

### 4.3 作业生命周期管理

#### 任务重试策略

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: retry-job
spec:
  schedulerName: volcano
  minAvailable: 2
  # 最大重试次数
  maxRetry: 3
  # 最大运行时间
  ttlSecondsAfterFinished: 3600
  tasks:
    - name: task
      replicas: 2
      # 任务级重试策略
      maxRetry: 5
      # 最小重试间隔（秒）
      retryDelaySeconds: 10
      template:
        spec:
          containers:
            - name: main
              image: python:3.9
              command: ["python", "task.py"]
          restartPolicy: OnFailure
```

#### 作业状态保留

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: cleanup-job
spec:
  schedulerName: volcano
  minAvailable: 1
  # 作业完成后保留时长（0 表示永久保留）
  ttlSecondsAfterFinished: 86400  # 24小时
  # 任务失败时的行为
  policies:
    - event: PodEvicted
      action: RestartJob
    - event: TaskFailed
      action: RestartTask
  tasks:
    - name: task
      replicas: 1
      template:
        spec:
          containers:
            - name: main
              image: nginx
```

### 4.4 队列管理

#### 创建队列

```yaml
apiVersion: scheduling.volcano.sh/v1alpha1
kind: Queue
metadata:
  name: production-queue
spec:
  capability:
    cpu: "200"
    memory: "400Gi"
    nvidia.com/gpu: "10"
  weight: 100              # 高优先级队列
  reclaimable: false       # 不允许被抢占
```

#### 提交作业到队列

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: prod-job
spec:
  schedulerName: volcano
  queue: production-queue  # 指定队列
  minAvailable: 4
  tasks:
    - name: task
      replicas: 4
      template:
        spec:
          containers:
            - name: main
              image: nginx
              resources:
                requests:
                  cpu: "2"
                  memory: "4Gi"
```

---

## 5. 实战案例

### 5.1 案例1：分布式 TensorFlow 训练

#### 场景描述
使用 TensorFlow 的 Parameter Server 模式进行分布式训练，需要同时启动多个 PS 和 Worker。

#### 实现

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: tensorflow-distributed
spec:
  schedulerName: volcano
  minAvailable: 5  # Gang Scheduling：所有 Pod 必须同时调度
  queue: ml-queue
  tasks:
    # Parameter Servers
    - name: ps
      replicas: 2
      labels:
        app: tensorflow
        role: ps
      template:
        spec:
          containers:
            - name: tensorflow
              image: tensorflow:2.12.0-gpu
              command:
                - python
                - -m
                - tensorflow.python.distribute.parameter_server_training
              env:
                - name: TF_CONFIG
                  value: |
                    {
                      "cluster": {
                        "ps": ["tensorflow-distributed-ps-0:2222", "tensorflow-distributed-ps-1:2222"],
                        "worker": ["tensorflow-distributed-worker-0:2222", "tensorflow-distributed-worker-1:2222", "tensorflow-distributed-worker-2:2222"]
                      },
                      "task": {"type": "ps", "index": ${TASK_INDEX}}
                    }
              resources:
                requests:
                  cpu: "2"
                  memory: "4Gi"

    # Workers
    - name: worker
      replicas: 3
      labels:
        app: tensorflow
        role: worker
      template:
        spec:
          containers:
            - name: tensorflow
              image: tensorflow:2.12.0-gpu
              command:
                - python
                - -m
                - tensorflow.python.distribute.parameter_server_training
              env:
                - name: TF_CONFIG
                  value: |
                    {
                      "cluster": {
                        "ps": ["tensorflow-distributed-ps-0:2222", "tensorflow-distributed-ps-1:2222"],
                        "worker": ["tensorflow-distributed-worker-0:2222", "tensorflow-distributed-worker-1:2222", "tensorflow-distributed-worker-2:2222"]
                      },
                      "task": {"type": "worker", "index": ${TASK_INDEX}}
                    }
              resources:
                requests:
                  cpu: "4"
                  memory: "8Gi"
                  nvidia.com/gpu: "1"
```

### 5.2 案例2：Spark on Volcano

#### 场景描述
在 Kubernetes 上运行 Spark 作业，利用 Volcano 的 Gang Scheduling 保证 Driver 和 Executor 同时调度。

#### 实现

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: spark-pi
spec:
  schedulerName: volcano
  minAvailable: 4  # 1 Driver + 3 Executors
  queue: data-queue
  tasks:
    # Spark Driver
    - name: spark-driver
      replicas: 1
      template:
        spec:
          containers:
            - name: spark-driver
              image: spark:3.5.0
              command:
                - /opt/spark/bin/spark-submit
                - --master
                - k8s://https://kubernetes.default.svc
                - --deploy-mode
                - cluster
                - --name
                - spark-pi
                - --num-executors
                - "3"
                - --executor-cores
                - "2"
                - --executor-memory
                - 4G
                - --conf
                - spark.executor.instances=3
                - --conf
                - spark.kubernetes.container.image=spark:3.5.0
                - --conf
                - spark.kubernetes.authenticate.driver.serviceAccountName=spark
                - local:///opt/spark/examples/src/main/python/pi.py
              resources:
                requests:
                  cpu: "1"
                  memory: "2Gi"
          serviceAccountName: spark

    # Spark Executors
    - name: spark-executor
      replicas: 3
      template:
        spec:
          containers:
            - name: spark-executor
              image: spark:3.5.0
              resources:
                requests:
                  cpu: "2"
                  memory: "4Gi"
```

### 5.3 案例3：MPI 并行计算

#### 场景描述
使用 MPI（Message Passing Interface）运行高性能科学计算应用。

#### 实现

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: mpi-job
spec:
  schedulerName: volcano
  minAvailable: 5  # 1 Master + 4 Workers
  queue: hpc-queue
  plugins:
    svc: []
  tasks:
    # MPI Master
    - name: master
      replicas: 1
      policies:
        - event: TaskCompleted
          action: CompleteJob
      template:
        spec:
          containers:
            - name: mpi-master
              image: mpi:latest
              command:
                - mpirun
                - -np
                - "4"
                - -hostfile
                - /etc/hosts
                - /app/simulation
              resources:
                requests:
                  cpu: "2"
                  memory: "4Gi"

    # MPI Workers
    - name: worker
      replicas: 4
      template:
        spec:
          containers:
            - name: mpi-worker
              image: mpi:latest
              command:
                - sleep
                - "3600"
              resources:
                requests:
                  cpu: "4"
                  memory: "8Gi"
```

### 5.4 案例4：ETL 数据处理流水线

#### 场景描述
构建一个完整的数据处理流水线：数据抽取 → 转换 → 加载 → 报告。

#### 实现

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: etl-pipeline
spec:
  schedulerName: volcano
  minAvailable: 1
  queue: etl-queue
  tasks:
    # 步骤1: 数据抽取
    - name: extract
      replicas: 1
      template:
        spec:
          containers:
            - name: extract
              image: python:3.9
              command: ["python", "/app/extract.py"]
              volumeMounts:
                - name: data
                  mountPath: /data
          volumes:
            - name: data
              emptyDir: {}

    # 步骤2: 数据转换（并行处理）
    - name: transform
      replicas: 4
      dependsOn:
        - extract
      template:
        spec:
          containers:
            - name: transform
              image: python:3.9
              command: ["python", "/app/transform.py"]
              volumeMounts:
                - name: data
                  mountPath: /data
          volumes:
            - name: data
              emptyDir: {}

    # 步骤3: 数据加载
    - name: load
      replicas: 2
      dependsOn:
        - transform
      template:
        spec:
          containers:
            - name: load
              image: python:3.9
              command: ["python", "/app/load.py"]
              volumeMounts:
                - name: data
                  mountPath: /data
          volumes:
            - name: data
              emptyDir: {}

    # 步骤4: 生成报告
    - name: report
      replicas: 1
      dependsOn:
        - load
      policies:
        - event: TaskCompleted
          action: CompleteJob
      template:
        spec:
          containers:
            - name: report
              image: python:3.9
              command: ["python", "/app/generate_report.py"]
```

### 5.5 案例5：批量模型推理

#### 场景描述
对大量数据进行批量模型推理，使用 GPU 加速。

#### 实现

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: batch-inference
spec:
  schedulerName: volcano
  minAvailable: 8  # 8个并行推理任务
  queue: inference-queue
  maxRetry: 2
  tasks:
    - name: inference
      replicas: 8
      template:
        spec:
          containers:
            - name: inference
              image: pytorch/pytorch:2.0-cuda11.7-cudnn8-runtime
              command:
                - python
                - /app/inference.py
                - --input-dir
                - /data/input
                - --output-dir
                - /data/output
                - --batch-size
                - "32"
              resources:
                requests:
                  cpu: "4"
                  memory: "8Gi"
                  nvidia.com/gpu: "1"
                limits:
                  cpu: "8"
                  memory: "16Gi"
                  nvidia.com/gpu: "1"
              volumeMounts:
                - name: data
                  mountPath: /data
          volumes:
            - name: data
              persistentVolumeClaim:
                claimName: model-data-pvc
          restartPolicy: OnFailure
```

---

## 6. 深入能力分析

### 6.1 Gang Scheduling 机制

#### 6.1.1 原理解析

Gang Scheduling 确保**要么所有任务都调度成功，要么都不调度**，防止部分资源分配导致的死锁问题。

```
传统调度问题（没有 Gang Scheduling）:

┌─────────────────────────────────────────────────────────────┐
│  节点资源: CPU=8, Memory=16Gi                               │
├─────────────────────────────────────────────────────────────┤
│                                                               │
│  作业A需要 6 CPUs                                             │
│  ┌────────────┐                                             │
│  │ 4 Pods 已  │  4 CPUs 已分配                              │
│  │   调度成功  │                                             │
│  └────────────┘                                             │
│                                                               │
│  剩余资源: CPU=4  (不足以调度剩余 2 Pods)                     │
│  结果: 作业A 永远无法完成！                                  │
└─────────────────────────────────────────────────────────────┘

Gang Scheduling 解决方案:

┌─────────────────────────────────────────────────────────────┐
│  调度器计算: 作业A 需要 6 CPUs                                │
│  当前可用: 4 CPUs < 6 CPUs                                   │
│  决策: 拒绝调度，等待足够资源                                │
│                                                               │
│  节点资源释放后 (CPU=8):                                     │
│  ┌────────────────────────────────────┐                     │
│  │  所有 6 Pods 同时调度成功          │                     │
│  └────────────────────────────────────┘                     │
└─────────────────────────────────────────────────────────────┘
```

#### 6.1.2 配置示例

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: gang-scheduling-demo
spec:
  schedulerName: volcano
  # 关键参数：最小可用任务数
  # 所有任务必须同时满足 minAvailable 才能调度
  minAvailable: 5
  tasks:
    - name: task
      replicas: 5  # 总共 5 个任务
      template:
        spec:
          containers:
            - name: main
              image: nginx
              resources:
                requests:
                  cpu: "2"
                  memory: "4Gi"
```

### 6.2 公平调度算法

#### 6.2.1 调度策略

Volcano 提供多种公平调度策略：

| 策略 | 说明 | 适用场景 |
|------|------|----------|
| **drf** (Dominant Resource Fairness) | 基于主导资源的公平分配 | 多资源异构环境 |
| **proportion** | 按队列权重比例分配 | 资源配额管理 |
| **priority** | 按作业优先级调度 | 需要优先级控制的场景 |

#### 6.2.2 DRF 配置示例

```yaml
apiVersion: scheduling.volcano.sh/v1alpha1
kind: Queue
metadata:
  name: team-a
spec:
  capability:
    cpu: "100"
    memory: "200Gi"
  weight: 10  # 权重 10
---
apiVersion: scheduling.volcano.sh/v1alpha1
kind: Queue
metadata:
  name: team-b
spec:
  capability:
    cpu: "100"
    memory: "200Gi"
  weight: 10  # 权重 10，与 team-a 平等
```

```yaml
# 启用 DRF 插件
apiVersion: config.volcano.sh/v1alpha1
kind: SchedulerConfiguration
metadata:
  name: volcano-scheduler-config
spec:
  actions: "enqueue, allocate, backfill"
  tiers:
    - plugins:
        - name: priority
        - name: gang
        - name: drf  # DRF 公平调度
          enablePredicate: true
          enableNodeGroup: true
      # 配置 DRF 参数
      configurations:
        - name: drf
          arguments:
            drf.resourceWeights: |
              {
                "cpu": 1,
                "memory": 1,
                "nvidia.com/gpu": 2
              }
```

### 6.3 任务亲和性

#### 6.3.1 Pod 组间亲和性

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: affinity-demo
spec:
  schedulerName: volcano
  minAvailable: 4
  tasks:
    - name: task-a
      replicas: 2
      # 任务级亲和性配置
      affinity:
        # 与其他 Job 的 Pod 亲和
        podAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            - labelSelector:
                matchExpressions:
                  - key: app
                    operator: In
                    values:
                      - database
              topologyKey: kubernetes.io/hostname
      template:
        spec:
          containers:
            - name: main
              image: nginx

    - name: task-b
      replicas: 2
      # 与同一 Job 的 task-a 任务亲和
      affinity:
        podAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            - labelSelector:
                matchExpressions:
                  - key: volcano.sh/job-name
                    operator: In
                    values:
                      - affinity-demo
                  - key: volcano.sh/task-name
                    operator: In
                    values:
                      - task-a
              topologyKey: kubernetes.io/hostname
      template:
        spec:
          containers:
            - name: main
              image: redis
```

### 6.4 资源预留与抢占

#### 6.4.1 优先级配置

```yaml
# 优先级类定义
apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: high-priority
value: 1000
globalDefault: false
description: "高优先级作业，可抢占低优先级作业"
---
apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: low-priority
value: 100
globalDefault: true
description: "低优先级作业，可被抢占"
```

```yaml
# 高优先级作业
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: critical-job
spec:
  schedulerName: volcano
  priorityClassName: high-priority  # 使用高优先级
  minAvailable: 4
  queue: production-queue
  tasks:
    - name: task
      replicas: 4
      template:
        spec:
          containers:
            - name: main
              image: nginx
              resources:
                requests:
                  cpu: "4"
                  memory: "8Gi"
```

#### 6.4.2 抢占策略

```yaml
apiVersion: config.volcano.sh/v1alpha1
kind: SchedulerConfiguration
metadata:
  name: volcano-scheduler-config
spec:
  # 启用抢占插件
  plugins:
    - name: preempt
      enabledPreemptors:
        - name: priority
          enablePreemptor: true
      # 抢占配置
      configurations:
        - name: preempt
          arguments:
            # 抢占超时时间（秒）
            preemptingTimeout: 30
            # 最小抢占 victims 数量
            minVictims: 1
```

### 6.5 任务拓扑管理

#### 6.5.1 复杂 DAG 示例

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: complex-pipeline
spec:
  schedulerName: volcano
  minAvailable: 1

  # 任务拓扑图
  #     data-fetch
  #         │
  #    ┌────┴────┐
  #    ▼         ▼
  # validate  clean
  #    │         │
  #    └────┬────┘
  #         ▼
  #    transform
  #         │
  #    ┌────┴────┐
  #    ▼         ▼
  # train-a   train-b
  #    │         │
  #    └────┬────┘
  #         ▼
  #    ensemble
  #         │
  #         ▼
  #    evaluate

  tasks:
    - name: data-fetch
      replicas: 1
      template:
        spec:
          containers:
            - name: fetch
              image: python:3.9
              command: ["python", "fetch.py"]

    - name: validate
      replicas: 1
      dependsOn:
        - data-fetch
      template:
        spec:
          containers:
            - name: validate
              image: python:3.9
              command: ["python", "validate.py"]

    - name: clean
      replicas: 1
      dependsOn:
        - data-fetch
      template:
        spec:
          containers:
            - name: clean
              image: python:3.9
              command: ["python", "clean.py"]

    - name: transform
      replicas: 1
      dependsOn:
        - validate
        - clean
      template:
        spec:
          containers:
            - name: transform
              image: python:3.9
              command: ["python", "transform.py"]

    - name: train-a
      replicas: 2
      dependsOn:
        - transform
      template:
        spec:
          containers:
            - name: train
              image: pytorch/pytorch:2.0
              command: ["python", "train_model_a.py"]
              resources:
                requests:
                  nvidia.com/gpu: "1"

    - name: train-b
      replicas: 2
      dependsOn:
        - transform
      template:
        spec:
          containers:
            - name: train
              image: pytorch/pytorch:2.0
              command: ["python", "train_model_b.py"]
              resources:
                requests:
                  nvidia.com/gpu: "1"

    - name: ensemble
      replicas: 1
      dependsOn:
        - train-a
        - train-b
      template:
        spec:
          containers:
            - name: ensemble
              image: python:3.9
              command: ["python", "ensemble.py"]

    - name: evaluate
      replicas: 1
      dependsOn:
        - ensemble
      policies:
        - event: TaskCompleted
          action: CompleteJob
      template:
        spec:
          containers:
            - name: evaluate
              image: python:3.9
              command: ["python", "evaluate.py"]
```

### 6.6 监控与可观测性

#### 6.6.1 作业状态查询

```bash
# 查看作业列表
kubectl get vcjobs

# 查看作业详情
kubectl describe vcjob job-name

# 查看 PodGroup 状态
kubectl get podgroups

# 查看队列状态
kubectl get queue
```

#### 6.6.2 作业状态说明

| 状态 | 说明 | 转换条件 |
|------|------|----------|
| **Pending** | 作业已创建，等待调度 | 资源可用 → Running |
| **Running** | 作业正在运行 | 任务完成 → Completed<br>任务失败 → Failed |
| **Completed** | 作业成功完成 | - |
| **Failed** | 作业失败 | 达到最大重试次数 |
| **Restarting** | 作业正在重启 | 任务失败，重试中 |
| **Terminating** | 作业正在终止 | 用户删除或超时 |

#### 6.6.3 Prometheus 集成

```yaml
# Prometheus ServiceMonitor 配置
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: volcano-scheduler
  namespace: volcano-system
spec:
  selector:
    matchLabels:
      app: volcano-scheduler
  endpoints:
    - port: http-metrics
      interval: 30s
```

**关键指标**：

```yaml
# 作业指标
volcano_job_pending_total          # 待处理作业总数
volcano_job_running_total          # 运行中作业总数
volcano_job_completed_total        # 已完成作业总数
volcano_job_failed_total           # 失败作业总数

# 队列指标
volcano_queue_pending_pods         # 队列中待调度 Pod 数
volcano_queue_running_pods         # 队列中运行中 Pod 数
volcano_queue_used_cpu             # 队列已用 CPU
volcano_queue_used_memory          # 队列已用内存

# 调度器指标
volcano_schedule_attempt_total     # 调度尝试总数
volcano_schedule_success_total     # 调度成功总数
volcano_schedule_failure_total     # 调度失败总数
volcano_schedule_duration_seconds  # 调度耗时
```

### 6.7 高级特性

#### 6.7.1 任务重试与容错

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: resilient-job
spec:
  schedulerName: volcano
  minAvailable: 3

  # 作业级别容错配置
  maxRetry: 5                  # 作业最大重试次数
  retryDelaySeconds: 30        # 重试延迟

  # 任务失败策略
  policies:
    - event: PodEvicted        # Pod 被驱逐时
      action: RestartJob       # 重启整个作业
    - event: TaskFailed        # 任务失败时
      action: RestartTask      # 仅重启失败的任务

  tasks:
    - name: task
      replicas: 3
      # 任务级别容错配置
      maxRetry: 3
      retryDelaySeconds: 10
      template:
        spec:
          containers:
            - name: main
              image: python:3.9
              command: ["python", "task.py"]
          # 使用节点反亲和提高容错性
          affinity:
            podAntiAffinity:
              preferredDuringSchedulingIgnoredDuringExecution:
                - weight: 100
                  podAffinityTerm:
                    labelSelector:
                      matchExpressions:
                        - key: volcano.sh/job-name
                          operator: In
                          values:
                            - resilient-job
                    topologyKey: kubernetes.io/hostname
```

#### 6.7.2 资源配额管理

```yaml
# 创建资源配额
apiVersion: scheduling.volcano.sh/v1alpha1
kind: Queue
metadata:
  name: shared-queue
spec:
  # 硬限制：队列最大可用资源
  capability:
    cpu: "100"
    memory: "200Gi"
    nvidia.com/gpu: "10"
  # 权重：用于公平调度
  weight: 50
  # 是否允许被高优先级队列抢占
  reclaimable: true
```

```yaml
# 使用配额的作业
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: quota-job
spec:
  schedulerName: volcano
  queue: shared-queue
  minAvailable: 10
  tasks:
    - name: task
      replicas: 10
      template:
        spec:
          containers:
            - name: main
              image: nginx
              resources:
                requests:
                  cpu: "2"
                  memory: "4Gi"
```

#### 6.7.3 任务超时管理

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: timeout-job
spec:
  schedulerName: volcano
  minAvailable: 2

  # 作业超时配置
  activeDeadlineSeconds: 7200    # 作业最大运行时间（2小时）

  # 完成后清理策略
  ttlSecondsAfterFinished: 3600  # 完成后1小时删除

  tasks:
    - name: task
      replicas: 2
      # 任务级别超时
      template:
        spec:
          activeDeadlineSeconds: 1800  # 单个任务30分钟超时
          containers:
            - name: main
              image: python:3.9
              command: ["python", "long_running_task.py"]
```

---

## 附录

### A. 快速参考

#### 常用命令

```bash
# 安装 Volcano
helm install volcano volcano-sh/volcano -n volcano-system --create-namespace

# 提交作业
kubectl apply -f job.yaml

# 查看作业状态
kubectl get vcjobs
kubectl describe vcjob job-name
kubectl get pods -l volcano.sh/job-name=job-name

# 查看日志
kubectl logs -l volcano.sh/job-name=job-name --tail=-1

# 删除作业
kubectl delete vcjob job-name

# 查看队列
kubectl get queue
kubectl describe queue queue-name

# 查看调度器日志
kubectl logs -n volcano-system deployment/volcano-scheduler
```

### B. 故障排查

#### 常见问题

1. **作业一直 Pending**
   ```bash
   # 检查调度事件
   kubectl describe vcjob job-name | grep -A 20 Events

   # 检查资源是否足够
   kubectl describe node | grep -A 5 "Allocated resources"

   # 检查队列配额
   kubectl get queue
   kubectl describe queue queue-name
   ```

2. **Gang Scheduling 失败**
   ```bash
   # 检查 minAvailable 设置
   kubectl get vcjob job-name -o yaml | grep minAvailable

   # 检查 PodGroup 状态
   kubectl get podgroups
   kubectl describe podgroup job-name
   ```

3. **任务反复重启**
   ```bash
   # 查看任务退出码
   kubectl get pods -l volcano.sh/job-name=job-name

   # 查看任务日志
   kubectl logs pod-name --previous
   ```

### C. 资源链接

- **官方文档**: https://volcano.sh/docs/
- **GitHub 仓库**: https://github.com/volcano-sh/volcano
- **社区 Slack**: https://volcano-sh.slack.com/
- **邮件列表**: volcano-dev@googlegroups.com

---

**文档版本**: v1.0
**最后更新**: 2025-01-20