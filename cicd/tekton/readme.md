# Tekton + GitCI 自动触发流水线设计

本文档描述一套完整的 **Git Commit → Tekton 自动构建** 的 CI 架构。当代码仓库存在 `.gitci` 文件时，每次分支提交都会自动触发 Tekton Pipeline 构建。

---

## 一、整体架构

```
Git Push
   ↓
Webhook (GitHub / GitLab / Gitea)
   ↓
Tekton Triggers (EventListener)
   ↓
TriggerBinding + TriggerTemplate
   ↓
PipelineRun
   ↓
TaskRun
   ↓
Pod
```

`.gitci` 负责描述构建逻辑，实现 **CI 与代码同库、同版本管理**。

---

## 二、环境准备

需要：

* kind 集群
* Tekton Pipelines
* Tekton Triggers
* kubectl / tkn

### 安装 Tekton Pipelines

```bash
kubectl apply -f https://storage.googleapis.com/tekton-releases/pipeline/latest/release.yaml
```

### 安装 Tekton Triggers

```bash
kubectl apply -f https://storage.googleapis.com/tekton-releases/triggers/latest/release.yaml
kubectl apply -f https://storage.googleapis.com/tekton-releases/triggers/latest/interceptors.yaml
```

等待组件运行：

```bash
kubectl get pods -n tekton-pipelines
```

---

## 三、定义 CI Pipeline

### Pipeline 示例

```yaml
apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: gitci-pipeline
spec:
  params:
  - name: repo_url
  - name: revision
  workspaces:
  - name: shared
  tasks:
  - name: clone
    taskSpec:
      workspaces:
      - name: output
      params:
      - name: url
      - name: revision
      steps:
      - image: alpine/git
        script: |
          git clone $(params.url) .
          git checkout $(params.revision)
    workspaces:
    - name: output
      workspace: shared
    params:
    - name: url
      value: $(params.repo_url)
    - name: revision
      value: $(params.revision)

  - name: run
    runAfter: [clone]
    taskSpec:
      workspaces:
      - name: src
      steps:
      - image: busybox
        workingDir: $(workspaces.src.path)
        script: |
          if [ ! -f .gitci ]; then
            echo "no .gitci, skip"
            exit 0
          fi
          echo "CI running"
          ls -la
    workspaces:
    - name: src
      workspace: shared
```

```bash
kubectl apply -f pipeline.yaml
```

---

## 四、.gitci 设计

仓库中增加：

```yaml
# .gitci
pipeline: gitci-pipeline
image: golang:1.22
script:
  - go test ./...
```

`.gitci` 作为 CI 的源码，可以被 Task 解析执行。

---

## 五、Tekton Triggers 配置

### TriggerBinding

```yaml
apiVersion: triggers.tekton.dev/v1beta1
kind: TriggerBinding
metadata:
  name: gitci-binding
spec:
  params:
  - name: repo_url
    value: $(body.repository.clone_url)
  - name: revision
    value: $(body.ref)
```

---

### TriggerTemplate

```yaml
apiVersion: triggers.tekton.dev/v1beta1
kind: TriggerTemplate
metadata:
  name: gitci-template
spec:
  params:
  - name: repo_url
  - name: revision
  resourcetemplates:
  - apiVersion: tekton.dev/v1
    kind: PipelineRun
    metadata:
      generateName: gitci-run-
    spec:
      pipelineRef:
        name: gitci-pipeline
      params:
      - name: repo_url
        value: $(tt.params.repo_url)
      - name: revision
        value: $(tt.params.revision)
      workspaces:
      - name: shared
        volumeClaimTemplate:
          spec:
            accessModes: ["ReadWriteOnce"]
            resources:
              requests:
                storage: 1Gi
```

---

### EventListener

```yaml
apiVersion: triggers.tekton.dev/v1beta1
kind: EventListener
metadata:
  name: gitci-listener
spec:
  serviceAccountName: tekton-triggers-sa
  triggers:
  - name: gitci-trigger
    bindings:
    - ref: gitci-binding
    template:
      ref: gitci-template
```

应用：

```bash
kubectl apply -f binding.yaml
kubectl apply -f template.yaml
kubectl apply -f listener.yaml
```

---

## 六、暴露 Webhook

```bash
kubectl expose eventlistener gitci-listener \
  --type=NodePort \
  --name=gitci-webhook
```

查看端口：

```bash
kubectl get svc gitci-webhook
```

---

## 七、连接 Git Webhook

Git Webhook URL：

```
http://<NodeIP>:<NodePort>
```

事件：

* push

Content-Type：

```
application/json
```

---

## 八、触发验证

```bash
tkn pr list
tkn pipelinerun logs <name> -f
```

---

## 九、触发链路总结

```
commit
 → webhook
 → triggers
 → pipelinerun
 → taskrun
 → pod
```

CI 流水线与代码一起演化。

---

## 十、进阶方向

* Branch / PR 过滤
* Secret 注入
* Kaniko 镜像构建
* Cache PVC
* Volcano 调度 CI
* Tekton Triggers Interceptor

---

## 结语

Tekton + GitCI 让 CI 不再是平台配置，而是代码的一部分。Commit 发生时，Kubernetes 直接把事件转化为工作负载执行，实现真正的云原生 CI 内核。
