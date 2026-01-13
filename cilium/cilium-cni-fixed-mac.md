# Cilium Daemon ConfigMap 使用与 CNI 部署机制

本文档详细说明 cilium-daemon 如何使用 cilium-config ConfigMap、生成 cilium-cni 配置文件以及将 CNI 二进制部署到宿主机。

---

## 目录

1. [ConfigMap 配置加载机制](#1-configmap-配置加载机制)
2. [CNI 配置文件生成机制](#2-cni-配置文件生成机制)
3. [CNI 二进制部署机制](#3-cni-二进制部署机制)
4. [配置流程图](#4-配置流程图)
5. [关键代码位置](#5-关键代码位置)

---

## 1. ConfigMap 配置加载机制

### 1.1 整体流程

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        ConfigMap 配置加载流程                                  │
└─────────────────────────────────────────────────────────────────────────────┘

    Helm values.yaml
           │
           ▼
    cilium-config ConfigMap (Kubernetes)
           │
           ▼
    ┌──────────────────────────────────────┐
    │  Pod ConfigMap Volume Mount          │
    │  /tmp/cilium/config-map/             │
    └──────────────────────────────────────┘
           │
           ▼
    ┌──────────────────────────────────────┐
    │  Viper Configuration Loading         │
    │  - Read files from mounted directory │
    │  - Parse key-value pairs             │
    └──────────────────────────────────────┘
           │
           ▼
    ┌──────────────────────────────────────┐
    │  DaemonConfig Population             │
    │  (pkg/option/config.go)              │
    │  - Struct tags map to keys           │
    │  - Default values                    │
    │  - Validation                        │
    └──────────────────────────────────────┘
           │
           ▼
    ┌──────────────────────────────────────┐
    │  Hive Framework Dependency Injection │
    │  - Cell.Provide()                    │
    │  - Type-safe configuration passing   │
    └──────────────────────────────────────┘
           │
           ▼
    Application Components (cniConfigManager, etc.)
```

### 1.2 ConfigMap 挂载

**Pod 定义中的 ConfigMap 挂载** (`install/kubernetes/cilium/templates/cilium-daemonset.yaml`):

```yaml
# ConfigMap Volume
volumes:
  - name: cilium-config-path
    configMap:
      name: cilium-config
      items:
        - key: cilium-config.yaml
          path: cilium-config.yaml

# Volume Mount in Container
volumeMounts:
  - name: cilium-config-path
    mountPath: /tmp/cilium/config-map/
    readOnly: true
```

### 1.3 Viper 配置加载

**配置初始化代码** (`pkg/option/config.go`):

```go
// DaemonConfig 结构体定义所有配置项
type DaemonConfig struct {
    // CNI 配置
    CNIMacAddrMode string `mapstructure:"cni-mac-addr-mode"`
    CNIFixedMAC    string `mapstructure:"cni-fixed-mac"`
    CNIPrefixMACMap string `mapstructure:"cni-prefix-mac-map"`

    // ... 其他配置项
}

// Viper 初始化
func initViper(vp *viper.Viper) {
    vp.SetConfigType("yaml")
    vp.AddConfigPath("/tmp/cilium/config-map/")
    vp.SetConfigName("cilium-config")
}
```

### 1.4 配置键名映射

**配置常量定义** (`pkg/option/config.go`):

```go
const (
    // CNILogFile 是 CNI 插件日志文件路径
    CNILogFile = "cni-log-file"

    // CNIMACAddrMode 指定 Pod 的 MAC 地址生成模式
    CNIMACAddrMode = "cni-mac-addr-mode"

    // CNIFixedMAC 指定所有 Pod 使用的全局固定 MAC 地址
    CNIFixedMAC = "cni-fixed-mac"

    // CNIPrefixMACMap 指定 Pod 名称前缀到固定 MAC 地址的 JSON 映射
    CNIPrefixMACMap = "cni-prefix-mac-map"
)
```

**ConfigMap 中的实际配置**:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: cilium-config
data:
  cilium-config.yaml: |
    # CNI 配置文件路径
    cni-log-file: "/var/run/cilium/cilium-cni.log"

    # MAC 地址配置
    cni-mac-addr-mode: "random"          # random | deterministic
    cni-fixed-mac: ""                     # 全局固定 MAC (可选)
    cni-prefix-mac-map: '{"sts-": "AA:BB:CC:00:00:02"}'  # 前缀映射 (可选)
```

---

## 2. CNI 配置文件生成机制

### 2.1 整体流程

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                      CNI 配置文件生成流程                                      │
└─────────────────────────────────────────────────────────────────────────────┘

    DaemonConfig (Viper 已加载)
           │
           ▼
    ┌──────────────────────────────────────┐
    │  Hive Cell: cni.Cell                 │
    │  (daemon/cmd/cni/cell.go)            │
    │  - Provides cni.Config               │
    │  - Provides CNIConfigManager         │
    └──────────────────────────────────────┘
           │
           ▼
    ┌──────────────────────────────────────┐
    │  cniConfigManager                    │
    │  (daemon/cmd/cni/config.go)          │
    │  - Watches ConfigMap changes         │
    │  - Triggers regeneration             │
    └──────────────────────────────────────┘
           │
           ▼
    ┌──────────────────────────────────────┐
    │  renderCNIConf()                     │
    │  - Select template based on mode     │
    │  - renderCNITemplate()               │
    └──────────────────────────────────────┘
           │
           ▼
    ┌──────────────────────────────────────┐
    │  Go Template Processing              │
    │  - Inject values into template       │
    │  - Generate JSON output              │
    └──────────────────────────────────────┘
           │
           ▼
    ┌──────────────────────────────────────┐
    │  Write to Host                       │
    │  /hostetc/cni/net.d/05-cilium.conflist│
    │  (via hostPath mount)                │
    └──────────────────────────────────────┘
```

### 2.2 CNI 模板配置

**CNI 配置模板** (`daemon/cmd/cni/config.go` 第 98-179 行):

```go
var cniConfigs map[string]string = map[string]string{
    "none": `
{
  "cniVersion": "0.3.1",
  "name": "cilium",
  "plugins": [
    {
       "type": "cilium-cni",
       "enable-debug": {{.Debug | js}},
       "log-file": "{{.LogFile | js}}{{if .MACAddrMode }},
       "macAddrMode": "{{.MACAddrMode}}"{{end}}{{if .FixedMAC }},
       "fixedMac": "{{.FixedMAC}}"{{end}}{{if .PrefixMACMap }},
       "prefixMacMap": {{.PrefixMACMap}}{{end}}
    }
  ]
}`,

    "flannel": `...`,
    "portmap": `...`,
}
```

### 2.3 模板渲染函数

**renderCNITemplate** (`daemon/cmd/cni/config.go` 第 443-469 行):

```go
func (c *cniConfigManager) renderCNITemplate(in string) []byte {
    data := struct {
        Debug        bool
        LogFile      string
        ChainingMode string
        MACAddrMode  string
        FixedMAC     string
        PrefixMACMap string
    }{
        Debug:        c.debug,
        LogFile:      c.config.CNILogFile,
        ChainingMode: c.config.CNIChainingMode,
        MACAddrMode:  c.config.MACAddrMode,
        FixedMAC:     c.config.FixedMAC,
        PrefixMACMap: c.config.PrefixMACMap,
    }

    t := template.Must(template.New("cni").Parse(in))
    out := bytes.Buffer{}
    if err := t.Execute(&out, data); err != nil {
        panic(err)
    }
    return out.Bytes()
}
```

### 2.4 CNI 配置写入

**setupCNIConfFile** (`daemon/cmd/cni/config.go` 第 305-377 行):

```go
func (c *cniConfigManager) setupCNIConfFile() (err error) {
    var contents []byte
    dest := path.Join(c.cniConfDir, c.cniConfFile)

    // 生成 CNI 配置
    contents, err = c.renderCNIConf()

    // 确保目录存在
    err = ensureDirExists(c.cniConfDir)

    // 检查文件是否已存在且相同
    existingContents, err := os.ReadFile(dest)
    if err == nil && bytes.Equal(existingContents, contents) {
        return nil // 无变化
    }

    // 原子写入文件
    if err := renameio.WriteFile(dest, contents, 0600); err != nil {
        return fmt.Errorf("failed to write CNI config: %w", err)
    }

    return nil
}
```

### 2.5 生成的 CNI 配置文件示例

**宿主机上实际生成的文件** (`/etc/cni/net.d/05-cilium.conflist`):

```json
{
  "cniVersion": "0.3.1",
  "name": "cilium",
  "plugins": [
    {
      "type": "cilium-cni",
      "enable-debug": false,
      "log-file": "/var/run/cilium/cilium-cni.log",
      "macAddrMode": "random",
      "fixedMac": "",
      "prefixMacMap": {}
    }
  ]
}
```

---

## 3. CNI 二进制部署机制

### 3.1 整体流程

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                       CNI 二进制部署流程                                        │
└─────────────────────────────────────────────────────────────────────────────┘

    Cilium DaemonSet Pod 启动
           │
           ▼
    ┌──────────────────────────────────────┐
    │  InitContainer: cni-install          │
    │  Image: quay.io/cilium/cilium:v1.x.x │
    │  Command: /install-plugin.sh         │
    └──────────────────────────────────────┘
           │
           ▼
    ┌──────────────────────────────────────┐
    │  install-plugin.sh 执行               │
    │  - Copy cilium-cni binary            │
    │  - From: /usr/bin/cilium-cni          │
    │  - To: /host/opt/cni/bin/             │
    │  - Set permissions (755)              │
    └──────────────────────────────────────┘
           │
           ▼
    ┌──────────────────────────────────────┐
    │  hostPath Volume Mount               │
    │  Host: /opt/cni/bin/                 │
    │  Container: /host/opt/cni/bin/       │
    └──────────────────────────────────────┘
           │
           ▼
    宿主机 /opt/cni/bin/cilium-cni
    (Kubelet 可执行路径)
```

### 3.2 InitContainer 配置

**DaemonSet 中的 InitContainer 定义** (`install/kubernetes/cilium/templates/cilium-daemonset.yaml`):

```yaml
initContainers:
  - name: cni-install
    image: {{ .Values.image.repository }}:{{ .Values.image.tag }}
    command: ["/install-plugin.sh"]
    volumeMounts:
      - name: cni-path
        mountPath: /host/opt/cni/bin/
        mountPropagation: Bidirectional
    env:
      - name: CNI_BIN_PATH
        value: /host/opt/cni/bin/
    securityContext:
      privileged: true
```

### 3.3 install-plugin.sh 脚本

**安装脚本** (`install/kubernetes/cilium/templates/cilium-daemonset.yaml` 中的 ConfigMap):

```bash
#!/bin/sh

# CNI 二进制安装路径
CNI_BIN_DIR=${CNI_BIN_PATH:-/host/opt/cni/bin/}

# 确保目录存在
mkdir -p "${CNI_BIN_DIR}"

# 复制 CNI 二进制文件
cp /usr/bin/cilium-cni "${CNI_BIN_DIR}"

# 设置可执行权限
chmod 755 "${CNI_BIN_DIR}/cilium-cni"

echo "Cilium CNI plugin installed successfully to ${CNI_BIN_DIR}"
```

### 3.4 Volume 挂载配置

**hostPath 卷定义**:

```yaml
volumes:
  # CNI 二进制挂载
  - name: cni-path
    hostPath:
      path: /opt/cni/bin/
      type: Directory

  # CNI 配置文件挂载
  - name: cni-conf-dir
    hostPath:
      path: /etc/cni/net.d/
      type: DirectoryOrCreate

  # CNI 日志目录挂载
  - name: cni-log-dir
    hostPath:
      path: /var/run/cilium/
      type: DirectoryOrCreate
```

### 3.5 宿主机文件布局

```
宿主机文件系统
├── /opt/cni/bin/
│   ├── cilium-cni          # CNI 二进制文件
│   └── ...                 # 其他 CNI 插件
│
├── /etc/cni/net.d/
│   ├── 05-cilium.conflist  # Cilium CNI 配置
│   └── ...                 # 其他 CNI 配置
│
└── /var/run/cilium/
    ├── cilium-cni.log      # CNI 插件日志
    └── ...                 # 其他运行时文件
```

---

## 4. 配置流程图

### 4.1 完整配置流程

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                    完整配置流程: Helm 到宿主机                                  │
└─────���───────────────────────────────────────────────────────────────────────┘

    ┌──────────────────┐
    │ values.yaml      │
    │ (Helm Chart)     │
    └────────┬─────────┘
             │
             ▼
    ┌──────────────────────────────────────────────────────────────┐
    │ Helm Template Processing                                     │
    │ - values.yaml → ConfigMap                                    │
    │ - values.yaml → DaemonSet                                    │
    └──────────────────────────────────────────────────────────────┘
             │
    ┌────────┴──────────────────────────────────────────────────┐
    │                                                          │
    ▼                                                          ▼
┌──────────────────────┐                              ┌──────────────────────┐
│ cilium-config        │                              │ cilium DaemonSet     │
│ ConfigMap            │                              │ Pod                  │
└──────────┬───────────┘                              └──────────┬───────────┘
           │                                                     │
           │ Mount to Pod                                        │ InitContainer
           │                                                     │
           ▼                                                     ▼
    ┌─────────────────────┐                          ┌──────────────────────┐
    │ /tmp/cilium/        │                          │ /install-plugin.sh   │
    │ config-map/         │                          │ Copies binary to     │
    │ (Pod 内)            │                          │ /host/opt/cni/bin/   │
    └──────────┬──────────┘                          └──────────────────────┘
               │
               ▼
    ┌─────────────────────┐
    │ Viper 读取配置      │
    │ 构建 DaemonConfig   │
    └──────────┬──────────┘
               │
               ▼
    ┌──────────────────────────────────────────────────────┐
    │ Hive DI                                             │
    │ - cni.Config 提供 CNI 配置                           │
    │ - cniConfigManager 监听并生成 CNI 文件               │
    └──────────┬───────────────────────────────────────────┘
               │
               ▼
    ┌──────────────────────────────────────────────────────┐
    │ Go Template 渲染                                     │
    │ cniConfigs[mode] + values → JSON                    │
    └──────────┬───────────────────────────────────────────┘
               │
               ▼
    ┌──────────────────────────────────────────────────────┐
    │ 写入宿主机                                           │
    │ /hostetc/cni/net.d/05-cilium.conflist               │
    │ = /etc/cni/net.d/05-cilium.conflist                 │
    └──────────────────────────────────────────────────────┘

    ┌──────────────────────────────────────────────────────┐
    │ Kubelet 调用 CNI                                     │
    │ /opt/cni/bin/cilium-cni add ...                      │
    │ 使用 /etc/cni/net.d/05-cilium.conflist              │
    └──────────────────────────────────────────────────────┘
```

### 4.2 MAC 配置专有流程

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                      MAC 配置流程 (自定义功能)                                │
└─────────────────────────────────────────────────────────────────────────────┘

    ┌──────────────────────────────┐
    │ values.yaml                  │
    │ cni:                         │
    │   macAddrMode: "random"      │
    │   fixedMac: ""               │
    │   prefixMacMap: {}           │
    └───────────┬──────────────────┘
                │
                ▼
    ┌──────────────────────────────────────────────────────────┐
    │ cilium-config ConfigMap                                  │
    │ cni-mac-addr-mode: "random"                              │
    │ cni-fixed-mac: ""                                        │
    │ cni-prefix-mac-map: '{}'                                 │
    └──────────────────────────────┬───────────────────────────┘
                                   │
                                   ▼
    ┌──────────────────────────────────────────────────────────┐
    │ DaemonConfig (Viper)                                     │
    │ CNIMacAddrMode = "random"                                │
    │ CNIFixedMAC = ""                                         │
    │ CNIPrefixMACMap = "{}"                                   │
    └──────────────────────────────┬───────────────────────────┘
                                   │
                                   ▼
    ┌──────────────────────────────────────────────────────────┐
    │ cni.Config (cell.go)                                     │
    │ MACAddrMode = "random"                                   │
    │ FixedMAC = ""                                            │
    │ PrefixMACMap = "{}"                                      │
    └──────────────────────────────┬───────────────────────────┘
                                   │
                                   ▼
    ┌──────────────────────────────────────────────────────────┐
    │ renderCNITemplate (config.go)                            │
    │ 注入到 Go 模板                                           │
    └──────────────────────────────┬───────────────────────────┘
                                   │
                                   ▼
    ┌──────────────────────────────────────────────────────────┐
    │ /etc/cni/net.d/05-cilium.conflist                        │
    │ {                                                        │
    │   "macAddrMode": "random",                               │
    │   "fixedMac": "",                                        │
    │   "prefixMacMap": {}                                     │
    │ }                                                        │
    └──────────────────────────────┬───────────────────────────┘
                                   │
                                   ▼
    ┌──────────────────────────────────────────────────────────┐
    │ cilium-cni add (cmd/cmd.go)                              │
    │ 解析 NetConf                                             │
    │ 构建 LinkConfig                                          │
    │   - FixedMAC                                             │
    │   - PrefixMACMap                                         │
    │   - MACAddrMode                                          │
    │   - PodNamespace (从 CNI_ARGS 获取)                      │
    │   - PodName (从 CNI_ARGS 获取)                           │
    └──────────────────────────────┬───────────────────────────┘
                                   │
                                   ▼
    ┌──────────────────────────────────────────────────────────┐
    │ connector.SetupVethWithNames (veth.go)                   │
    │ 调用 mac.GenerateMACWithConfig                            │
    │   1. 检查 FixedMAC                                       │
    │   2. 检查 PrefixMACMap 匹配                              │
    │   3. 检查 MACAddrMode == "deterministic"                 │
    │   4. 默认随机生成                                         │
    └──────────────────────────────┬───────────────────────────┘
                                   │
                                   ▼
    ┌──────────────────────────────────────────────────────────┐
    │ Pod veth 设备创建                                        │
    │ 使用配置的 MAC 地址                                       │
    └──────────────────────────────────────────────────────────┘
```

---

## 5. 关键代码位置

### 5.1 ConfigMap 相关

| 文件 | 行号 | 功能 |
|------|------|------|
| `pkg/option/config.go` | 1-2000+ | DaemonConfig 定义、配置常量 |
| `pkg/option/config.go` | 1024-1040 | CNI MAC 配置常量 |
| `daemon/cmd/cni/cell.go` | 29-131 | CNI Config 结构定义 |
| `daemon/cmd/cni/config.go` | 99-131 | cniConfigManager 初始化 |

### 5.2 CNI 配置生成相关

| 文件 | 行号 | 功能 |
|------|------|------|
| `daemon/cmd/cni/config.go` | 97-179 | CNI 配置模板 (cniConfigs) |
| `daemon/cmd/cni/config.go` | 168-179 | Chained CNI 入口模板 |
| `daemon/cmd/cni/config.go` | 443-469 | renderCNITemplate 函数 |
| `daemon/cmd/cni/config.go` | 305-377 | setupCNIConfFile 函数 |
| `daemon/cmd/cni/config.go` | 379-403 | renderCNIConf 函数 |

### 5.3 CNI 二进制部署相关

| 文件 | 行号 | 功能 |
|------|------|------|
| `install/kubernetes/cilium/templates/cilium-daemonset.yaml` | - | InitContainer 定义 |
| `install/kubernetes/cilium/templates/cilium-configmap.yaml` | 962-975 | CNI MAC 配置映射 |
| `install/kubernetes/cilium/values.yaml` | 805-819 | Helm CNI 配置值 |

### 5.4 MAC 生成逻辑相关

| 文件 | 行号 | 功能 |
|------|------|------|
| `plugins/cilium-cni/types/types.go` | 21-48 | NetConf 结构定义 |
| `plugins/cilium-cni/cmd/cmd.go` | 670-685 | LinkConfig 构建 |
| `pkg/datapath/connector/config.go` | 20-46 | LinkConfig 结构定义 |
| `pkg/mac/mac.go` | 169-214 | GenerateMACWithConfig 函数 |
| `pkg/datapath/connector/veth.go` | 35-81 | SetupVethWithNames 函数 |

---

## 6. 配置变更与热更新

### 6.1 ConfigMap 监听

**cniConfigManager 支持监听配置目录变化** (`daemon/cmd/cni/config.go`):

```go
// Start 方法中设置 fsnotify watcher
if c.config.CNIExclusive {
    var err error
    c.watcher, err = fsnotify.NewWatcher()
    if err != nil {
        c.logger.Warn("Failed to create watcher", logfields.Error, err)
    } else {
        if err := c.watcher.Add(c.cniConfDir); err != nil {
            c.logger.Warn("Failed to watch CNI directory")
            c.watcher = nil
        }
    }
}

// watchForDirectoryChanges goroutine
go c.watchForDirectoryChanges()
```

### 6.2 配置更新触发

```go
func (c *cniConfigManager) watchForDirectoryChanges() {
    for {
        select {
        case _, ok := <-c.watcher.Events:
            if !ok {
                return
            }
            c.logger.Info("Activity in CNI directory")
            c.controller.TriggerController(cniControllerName)
        case err, ok := <-c.watcher.Errors:
            // 处理错误
        case <-c.ctx.Done():
            return
        }
    }
}
```

**注意**: ConfigMap 更新后需要重启 Cilium DaemonSet Pod 才能生效。

---

## 7. 故障排查

### 7.1 常见问题

| 问题 | 可能原因 | 排查方法 |
|------|----------|----------|
| CNI 配置未生成 | ConfigMap 挂载失败 | 检查 `/tmp/cilium/config-map/` |
| CNI 二进制不可执行 | InitContainer 失败 | 检查 InitContainer 日志 |
| Pod 无法获取 IP | CNI 配置错误 | 检查 `/etc/cni/net.d/` |
| MAC 地址不符合预期 | 配置未传递 | 检查生成的 `.conflist` 文件 |

### 7.2 调试命令

```bash
# 查看 CNI 配置文件
cat /etc/cni/net.d/05-cilium.conflist

# 查看 CNI 二进制
ls -la /opt/cni/bin/cilium-cni

# 查看 CNI 日志
tail -f /var/run/cilium/cilium-cni.log

# 查看 ConfigMap
kubectl get configmap cilium-config -o yaml

# 查看 DaemonSet Pod
kubectl get pods -n kube-system -l k8s-app=cilium
```

---

## 8. 总结

1. **配置加载**: Helm → ConfigMap → Viper → DaemonConfig → Hive
2. **CNI 配置生成**: cniConfigManager → Go Template → JSON → 宿主机
3. **二进制部署**: InitContainer → install-plugin.sh → hostPath → 宿主机
4. **MAC 配置流程**: values → ConfigMap → DaemonConfig → CNI 文件 → CNI 插件 → veth 创建

整个流程采用**声明式配置**和**模板化生成**的设计，确保配置的一致性和可维护性。
