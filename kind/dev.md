# Mac 上使用 kind 搭建 Kubernetes 实验环境

本文档用于在 macOS 上使用 **kind (Kubernetes in Docker)** 构建一套适合学习与实验的 Kubernetes 环境，适合进行 **CNI / CSI Demo 开发、Volcano 调度器实验、多节点网络测试** 等。

---

## 一、环境准备

### 1. 安装 Docker Desktop

检查是否已安装：

```bash
docker version
```

未安装：

```bash
brew install --cask docker
open /Applications/Docker.app
```

等待 Docker 运行完成（图标变绿）。

---

### 2. 安装 kubectl

```bash
brew install kubectl
```

验证：

```bash
kubectl version --client
```

---

### 3. 安装 kind

```bash
brew install kind
```

验证：

```bash
kind version
```

---

## 二、创建多节点 kind 集群

### 1. 编写集群配置

```bash
cat <<EOF > kind.yaml
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
name: lab
nodes:
- role: control-plane
- role: worker
- role: worker
- role: worker
EOF
```

---

### 2. 创建集群

```bash
kind create cluster --config kind.yaml
```

---

### 3. 验证集群

```bash
kubectl get nodes -o wide
```

---

## 三、基础网络验证

```bash
kubectl get pods -A
```

测试 Pod：

```bash
kubectl run test --image=busybox -it --rm -- sh
```

容器内：

```sh
ip a
ping kubernetes.default
```

---

## 四、进入 Node（CNI / CSI 调试用）

查看 node：

```bash
docker ps | grep kind
```

进入 worker：

```bash
docker exec -it lab-worker bash
```

可查看：

```bash
ip a
iptables -L
ls /var/lib/kubelet
```

---

## 五、禁用默认 CNI（用于自定义开发）

### 1. 无 CNI 配置

```bash
cat <<EOF > kind-nocni.yaml
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
name: lab
networking:
  disableDefaultCNI: true
nodes:
- role: control-plane
- role: worker
- role: worker
- role: worker
EOF
```

---

### 2. 删除旧集群

```bash
kind delete cluster --name lab
```

---

### 3. 创建新集群

```bash
kind create cluster --config kind-nocni.yaml
```

---

### 4. 安装示例 CNI（Calico）

```bash
kubectl apply -f https://docs.projectcalico.org/manifests/calico.yaml
```

---

## 六、加载本地镜像

### 1. 构建镜像

```bash
docker build -t my-cni:dev .
```

---

### 2. 加载进 kind

```bash
kind load docker-image my-cni:dev --name lab
```

---

### 3. Pod 使用

```yaml
image: my-cni:dev
imagePullPolicy: IfNotPresent
```

---

## 七、安装 Helm

```bash
brew install helm
```

验证：

```bash
helm version
```

---

## 八、安装 Volcano

### 1. 添加仓库

```bash
helm repo add volcano-sh https://volcano-sh.github.io/helm-charts
helm repo update
```

---

### 2. 安装 Volcano

```bash
helm install volcano volcano-sh/volcano -n volcano-system --create-namespace
```

---

### 3. 验证

```bash
kubectl get pods -n volcano-system
kubectl get schedulers
```

---

### 4. Gang 调度 Demo

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: vc-demo
spec:
  minAvailable: 3
  schedulerName: volcano
  tasks:
  - replicas: 3
    name: task
    template:
      spec:
        containers:
        - name: c
          image: busybox
          command: ["sh","-c","sleep 1000"]
        restartPolicy: Never
```

---

## 九、CSI 开发辅助路径

进入 node：

```bash
docker exec -it lab-worker bash
```

关键目录：

```text
/var/lib/kubelet/plugins
/var/lib/kubelet/plugins_registry
/var/lib/kubelet/pods
```

---

## 十、常用调试命令

```bash
kubectl get pod -o wide
kubectl describe pod xxx
kubectl logs xxx
kubectl exec -it xxx -- sh
docker exec -it lab-worker bash
crictl ps
ip a
mount | grep kubelet
```

---

## 十一、性能建议

Docker Desktop 建议：

* CPU >= 6
* Memory >= 8G
* Swap >= 2G

---

## 十二、实验目录结构建议

```text
k8s-lab/
├── kind.yaml
├── cni/
├── csi/
├── volcano/
├── images/
└── hack/
```

---

## 结语

kind 提供的是一套“可拆解”的 Kubernetes 实验环境，非常适合学习网络、存储与调度的内部机制。它比 minikube 更轻量、更可控，也更适合工程与研究型实验。
