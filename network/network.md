# 階段一：夯實基礎與傳統協議棧 (The Fundamentals)

## 導論：雲原生工程師為何需要底層網路知識

對於雲原生工程師而言，Linux 網路是所有容器化、服務網格（Service Mesh）和網路策略（NetworkPolicy）的基石。雖然 Kubernetes 抽象了網路細節，但當遇到**網路延遲**、**數據包丟失**或 **CNI 插件故障**時，缺乏底層知識將使排錯工作陷入困境。理解 Linux 內核如何處理數據包，是從「使用者」轉變為「架構師」的關鍵一步。

本階段將聚焦於數據包在 Linux 內核中的生命週期，從網卡接收到應用程序處理的完整路徑。

## 1. 內核數據包路徑：從網卡到應用程序

數據包進入 Linux 系統的過程是一個高度優化的流程，旨在減少 CPU 負載並提高吞吐量。

### 1.1 Ingress Path (數據包進入)

數據包從物理網卡（NIC）進入系統，經歷以下主要步驟：

1. **硬體中斷 (Hardware Interrupt)**：網卡接收到數據包後，會向 CPU 發出硬體中斷，通知內核有新數據到達。
2. **NAPI (New API)**：為了解決高流量下中斷風暴（Interrupt Storm）導致的 CPU 資源耗盡問題，Linux 引入了 NAPI。NAPI 採用**輪詢（Polling）**與**中斷（Interrupt）**混合模式：
    - 當流量低時，使用中斷模式。
    - 當流量高時，網卡驅動會關閉中斷，轉為輪詢模式，由內核主動從網卡緩衝區批量拉取數據包，直到緩衝區清空或達到預設限制。
3. **Softirq (軟中斷)**：NAPI 處理完成後，會觸發一個軟中斷（通常是 `NET_RX_SOFTIRQ`）。軟中斷在內核線程中執行，將數據包從驅動層傳遞到協議棧。
4. **協議棧處理**：數據包被封裝成 `sk_buff` 結構，然後從 L2（乙太網幀）開始，逐層向上解封裝（L3 IP, L4 TCP/UDP），最終到達 Socket 層，等待應用程序讀取。

### 1.2 Egress Path (數據包發出)

應用程序發送數據包的過程與接收相反：

1. **應用程序調用**：應用程序通過 Socket 系統調用（如 `send()`）將數據寫入內核。
2. **協議棧封裝**：內核協議棧將數據封裝成 `sk_buff`，並逐層添加 L4 (TCP/UDP) 頭、L3 (IP) 頭。
3. **路由與排隊**：內核根據路由表決定出站介面，並將數據包放入該介面的**排隊規則 (Qdisc)** 中。
4. **驅動發送**：網卡驅動從 Qdisc 中取出數據包，並通過 DMA（直接記憶體存取）將數據傳輸到網卡硬體，最終發送到線路上。

## 2. 核心數據結構：`sk_buff`

`sk_buff` (Socket Buffer) 是 Linux 網路協議棧中**最核心**的數據結構。

> 定義：sk_buff 是一個 C 語言結構體，它不僅包含原始的網路數據包，還包含協議棧處理該數據包所需的所有元數據（Metadata）。
>

| 欄位類型 | 描述 | 雲原生關聯 |
| --- | --- | --- |
| **數據指針** | 指向實際數據包內容的指針，可以動態調整。 | 協議棧在解封裝時，只需移動指針，無需複製數據，提高效率。 |
| **控制資訊** | 包含數據包的來源/目的 IP、端口、協議類型、時間戳等。 | Netfilter/Iptables 和 eBPF 程式通過讀取這些元數據來做決策。 |
| **鏈表節點** | 允許 `sk_buff` 結構在各種隊列（如 NAPI 隊列、Qdisc 隊列）中快速移動。 | 實現高效的數據包隊列管理。 |

**核心價值**：`sk_buff` 結構的設計允許協議棧在處理數據包時，**最小化數據複製**。當數據包從 L2 傳遞到 L3，再到 L4 時，內核只需更新 `sk_buff` 結構中的指針和長度資訊，極大地提升了網路性能。

## 3. 基礎協議棧與工具

### 3.1 二層與三層轉發

- **二層 (L2)**：主要處理 MAC 地址。`ip neighbor` (或 `arp -n`) 用於查看 ARP 緩存，即 IP 地址與 MAC 地址的映射。
- **三層 (L3)**：主要處理 IP 地址。`ip route` 用於查看內核路由表，這是決定數據包下一跳的關鍵。
    - **雲原生關聯**：Pod 間的跨節點通訊，其路由規則（通常指向 VXLAN 隧道介面）就體現在這裡。

### 3.2 傳輸層機制

- **TCP/UDP**：`ss` (Socket Statistics) 是現代 Linux 系統中取代 `netstat` 的工具，用於查看 Socket 連接的詳細狀態。
    - **實踐**：使用 `ss -ntlp` 可以查看所有 TCP 連接、監聽端口、進程 ID (PID) 和進程名稱。這對於診斷服務端口衝突或連接狀態異常非常有用。

## 4. 實踐與觀測：內核活動

為了讓您對內核的網路活動有直觀的感受，我們將使用幾個工具來觀察數據包處理的痕跡。

### 4.1 觀察軟中斷 (Softirq)

軟中斷是協議棧處理數據包的主要場所。

- **命令**：`top` 或 `htop`
- **觀測點**：在 `top` 輸出中，觀察 `si` (Softirq) 佔用的 CPU 百分比。當網路流量大時，`si` 的值會顯著增加，這表明內核正在忙於處理 `NET_RX_SOFTIRQ`。

### 4.2 觀察網卡統計

`ethtool` 是一個用於查詢和控制網卡驅動和硬體設置的工具。

- **命令**：`sudo ethtool -S <interface_name>`
- **觀測點**：
    - `rx_missed_errors`：數據包被網卡丟棄的數量，通常是緩衝區滿了。
    - `rx_no_buffer_count`：內核沒有足夠的緩衝區來接收數據包。
    - `tx_timeout`：發送數據包時超時，通常與驅動或硬體問題有關。

### 4.3 觀察 Socket 狀態

- **命令**：`ss -s`
- **觀測點**：查看 Socket 摘要統計，特別是 `TCP: inuse` (已建立連接數) 和 `TCP: syn_recv` (正在建立連接數)。

---

## 總結與過渡

本階段我們理解了 Linux 內核處理數據包的**流程**（NAPI, Softirq）和**核心結構**（`sk_buff`）。這些基礎知識是理解下一階段 **網路虛擬化** 的前提。

在下一階段，我們將看到 Linux 如何利用 **Namespace**、**Veth** 和 **Bridge** 這些虛擬化工具，在同一個物理機上構建出多個隔離且互聯的網路環境，這正是容器網路的魔法所在。

---

# 階段二：網路虛擬化與容器基石 (Network Virtualization)

## 導論：容器網路的魔法

在雲原生環境中，每個 Pod 或容器都擁有一個獨立的網路堆棧，就像一台獨立的虛擬機一樣。這種隔離和互聯的「魔法」並非來自於虛擬化層（如 KVM），而是來自於 Linux 內核提供的 **Namespace** 機制。本階段將深入解析構成容器網路的三大核心組件：**Network Namespace**、**Veth Pair** 和 **Linux Bridge**。

## 1. 網路命名空間 (Network Namespace, Netns)

**Netns** 是 Linux 內核實現網路隔離的基礎。當一個進程被分配到一個新的 Netns 時，它將擁有一個完全獨立的網路環境。

> 核心概念：一個 Netns 擁有自己獨立的網路資源，包括：
>
> 1. **網路設備**：如 `lo` (Loopback)、`eth0` 等。
> 2. **IP 地址與 MAC 地址**。
> 3. **路由表**。
> 4. **Iptables/Netfilter 規則**。
> 5. **Socket 列表**。

| 屬性 | Host Netns | Container Netns |
| --- | --- | --- |
| **隔離性** | 共享所有網路資源 | 完全隔離，僅有 `lo` 設備 |
| **用途** | 運行系統服務、宿主機網路 | 運行容器應用，提供獨立 IP |
| **工具** | `ip netns exec <name> <command>` | `ip netns add <name>` |

**雲原生關聯**：當您在 Kubernetes 中創建一個 Pod 時，Kubelet 會為該 Pod 創建一個專屬的 Netns，並將 Pod 內的所有容器進程都加入到這個 Netns 中。

## 2. 虛擬設備對 (Veth Pair)

**Veth Pair** 是連接不同 Netns 的「虛擬網線」。它是一種特殊的虛擬網路設備，總是成對出現，數據從一端進入，會從另一端流出。

> 核心概念：Veth Pair 就像一條虛擬的管道，一端可以插入 Host 的 Netns，另一端可以插入 Container 的 Netns，從而實現 Host 與 Container 之間的二層（L2）通訊。
>

| 屬性 | Veth 設備 A | Veth 設備 B |
| --- | --- | --- |
| **類型** | 虛擬乙太網設備 | 虛擬乙太網設備 |
| **功能** | 數據從 A 進入，從 B 出 | 數據從 B 進入，從 A 出 |
| **MAC 地址** | 獨立的 MAC 地址 | 獨立的 MAC 地址 |
| **Netns** | 可位於不同 Netns | 可位於不同 Netns |

**雲原生關聯**：Veth Pair 是連接 Pod Netns 到 Host 網路的標準方式。在 Host 側，Veth 的一端通常會連接到一個 **Linux Bridge**。

## 3. 虛擬交換機 (Linux Bridge)

**Linux Bridge** (或稱 `bridge` 設備) 是一個工作在二層的虛擬交換機。它的作用是將多個網路設備（如 Host 上的物理網卡、多個 Veth 的 Host 端）連接起來，實現它們之間的二層轉發。

> 核心概念：Bridge 設備維護一個 MAC 地址表，通過學習連接在其上的設備的 MAC 地址，實現精確的數據幀轉發。
>
- **功能**：實現連接到它的所有設備之間的二層互通。
- **路由**：Bridge 本身可以配置 IP 地址，作為連接到它的子網的**網關**。

**雲原生關聯**：Docker 默認的 `bridge` 網路驅動就是使用 Linux Bridge 實現單機容器間的互通。Kubernetes 的 CNI 插件（如 Flannel 的 Host-Gateway 模式）也可能使用 Bridge 設備。

## 4. 跨主機通訊的基石：隧道技術 (VXLAN)

雖然 Netns、Veth 和 Bridge 解決了**單機**內的容器互通問題，但雲原生環境需要解決**跨主機**的容器互通。這通常通過 **Overlay Network** 實現，其中最常見的技術是 **VXLAN** (Virtual eXtensible LAN)。

> 核心概念：VXLAN 通過將 L2 乙太網幀封裝在 L4 UDP 數據包中，使其能夠在 L3 網路（即物理數據中心網路）上傳輸。
>

| 術語 | 描述 | 作用 |
| --- | --- | --- |
| **VTEP** | VXLAN Tunnel End Point，VXLAN 隧道的端點。 | 負責對數據包進行封裝和解封裝。每個 Host 都有一個 VTEP 設備。 |
| **VNI** | VXLAN Network Identifier，24 位標識符。 | 隔離不同的虛擬網路，類似於 VLAN ID。 |
| **封裝** | 原始乙太網幀 + VXLAN 頭 + UDP 頭 + IP 頭 + 乙太網頭。 | 讓 L2 數據包能夠通過 L3 網路傳輸。 |

**工作原理**：

1. 容器 A 發送數據包給容器 B (位於不同 Host)。
2. Host A 的 VTEP 設備接收到數據包。
3. VTEP 查詢路由表，確定目標 Host B 的 IP 地址。
4. VTEP 將原始數據包**封裝**成一個新的 UDP 數據包，目標 IP 是 Host B 的 IP。
5. Host A 將這個 UDP 數據包發送到物理網路。
6. Host B 接收到 UDP 數據包，其 VTEP 設備進行**解封裝**，將原始數據包轉發給容器 B。

**雲原生關聯**：Flannel、Calico 等 CNI 插件的 Overlay 模式都大量使用了 VXLAN 或類似的隧道技術（如 Geneve）來實現跨節點的 Pod 網路。

## 實踐任務：手動構建單機容器網路

為了將上述概念串聯起來，我們將使用 `ip` 命令手動構建一個包含兩個容器的隔離網路。

### 實驗步驟 (需要 `sudo` 權限)

| 步驟 | 命令 | 目的 |
| --- | --- | --- |
| **1. 創建 Netns** | `ip netns add netnsA` <br> `ip netns add netnsB` | 創建兩個隔離的網路環境 A 和 B。 |
| **2. 創建 Bridge** | `ip link add name br0 type bridge` <br> `ip link set br0 up` | 創建虛擬交換機 `br0` 並啟用。 |
| **3. 設置 Bridge IP** | `ip addr add 192.168.1.1/24 dev br0` | 將 `br0` 設置為子網的網關。 |
| **4. 創建 Veth Pair** | `ip link add vethA type veth peer name vethA_br` <br> `ip link add vethB type veth peer name vethB_br` | 創建兩對 Veth 設備。 |
| **5. 連接 Veth 到 Netns** | `ip link set vethA netns netnsA` <br> `ip link set vethB netns netnsB` | 將 Veth 的一端放入各自的 Netns 中。 |
| **6. 連接 Veth 到 Bridge** | `ip link set vethA_br master br0` <br> `ip link set vethB_br master br0` | 將 Veth 的另一端連接到虛擬交換機 `br0`。 |
| **7. 啟用 Veth 設備** | `ip netns exec netnsA ip link set vethA up` <br> `ip netns exec netnsB ip link set vethB up` <br> `ip link set vethA_br up` <br> `ip link set vethB_br up` | 啟用所有相關設備。 |
| **8. 配置 Netns IP** | `ip netns exec netnsA ip addr add 192.168.1.10/24 dev vethA` <br> `ip netns exec netnsB ip addr add 192.168.1.20/24 dev vethB` | 為容器 A 和 B 配置 IP 地址。 |
| **9. 測試連通性** | `ip netns exec netnsA ping 192.168.1.20` | 測試兩個隔離環境是否能互相通訊。 |

---
# 階段三：數據包過濾與流量控制 (Packet Filtering & TC)

## 導論：Linux 網路的「交通警察」

在第二階段，我們學會了如何構建網路（Netns, Veth, Bridge）。本階段的目標是學習如何**控制**這些網路中的數據流。Linux 內核的 **Netfilter** 框架是實現防火牆、網路地址轉換（NAT）和服務負載均衡的基礎。對於雲原生工程師來說，理解 Netfilter/Iptables 是掌握 Kube-proxy 和 Service Mesh 流量劫持機制的關鍵。

## 1. Netfilter 框架與 Iptables 體系結構

**Netfilter** 是 Linux 內核中用於處理網路數據包的框架。它在數據包流經協議棧的特定點（稱為 **Hooks**）提供攔截和處理的能力。**Iptables** 則是操作 Netfilter 規則的用戶空間工具。

### 1.1 Netfilter 的五個鉤子 (Hooks)

數據包在內核中流動時，會在以下五個預設點被 Netfilter 攔截：

| 鉤子名稱 (Hook) | 數據包類型 | 描述 |
| :--- | :--- | :--- |
| **`NF_IP_PRE_ROUTING`** | 所有數據包 | 數據包剛進入協議棧，尚未進行路由判斷。**NAT 轉換**通常發生在這裡。 |
| **`NF_IP_LOCAL_IN`** | 目標為本機 | 數據包經過路由判斷，確認目標是本機進程。 |
| **`NF_IP_FORWARD`** | 轉發數據包 | 數據包經過路由判斷，確認需要轉發到其他介面。 |
| **`NF_IP_LOCAL_OUT`** | 源自本機 | 本機進程發出的數據包，尚未進行路由判斷。 |
| **`NF_IP_POST_ROUTING`** | 所有數據包 | 數據包即將離開協議棧，發送到網路介面。**SNAT 轉換**通常發生在這裡。 |

### 1.2 Iptables 的四個表 (Tables)

Iptables 將規則組織在四個主要的「表」中，每個表負責不同類型的處理：

| 表名 | 目的 | 鉤子關聯 | 雲原生應用 |
| :--- | :--- | :--- | :--- |
| **`raw`** | 處理連接追蹤（Conntrack）例外。 | `PREROUTING`, `OUTPUT` | 快速標記數據包，跳過 Conntrack。 |
| **`mangle`** | 修改數據包頭部（如 TTL, TOS）。 | 所有五個鉤子 | 實現 QoS 或 Service Mesh 的流量標記。 |
| **`nat`** | 網路地址轉換（NAT）。 | `PREROUTING`, `POSTROUTING`, `OUTPUT` | **Kube-proxy** 實現 Service 負載均衡的核心。 |
| **`filter`** | 數據包過濾（防火牆）。 | `INPUT`, `FORWARD`, `OUTPUT` | **NetworkPolicy** 的底層實現。 |

### 1.3 Iptables 的五個內建鏈 (Chains)

每個表都包含一組內建的「鏈」，這些鏈對應於 Netfilter 的鉤子：

*   **`PREROUTING`** (對應 `NF_IP_PRE_ROUTING`)
*   **`INPUT`** (對應 `NF_IP_LOCAL_IN`)
*   **`FORWARD`** (對應 `NF_IP_FORWARD`)
*   **`OUTPUT`** (對應 `NF_IP_LOCAL_OUT`)
*   **`POSTROUTING`** (對應 `NF_IP_POST_ROUTING`)

> **核心流程**：一個數據包進入內核後，會按照固定的路徑依次經過不同的鉤子，並在每個鉤子上依次檢查相關表中的規則。例如，一個轉發的數據包會依次經過 `raw/PREROUTING` -> `mangle/PREROUTING` -> `nat/PREROUTING` -> 路由判斷 -> `mangle/FORWARD` -> `filter/FORWARD` -> `mangle/POSTROUTING` -> `nat/POSTROUTING`。

## 2. 連接追蹤 (Conntrack)

**Conntrack** 是 Netfilter 框架的另一個核心組件，它負責追蹤所有網路連接的狀態。

> **核心概念**：Conntrack 允許防火牆和 NAT 規則基於連接的**狀態**（如 `NEW`, `ESTABLISHED`, `RELATED`, `INVALID`）來做出決策，從而實現**有狀態防火牆**。

*   **重要性**：
    *   **NAT 實現**：NAT 必須依賴 Conntrack 來確保同一個連接的雙向數據包都進行正確的地址轉換。
    *   **性能問題**：Conntrack 表的大小是有限的。在雲原生環境中，如果 Pod 數量過多或連接頻繁，可能導致 Conntrack 表滿，引發**隨機連接丟失**或 **DNS 查詢失敗**等問題。
*   **觀測工具**：`conntrack -L` (列出所有連接) 和 `cat /proc/sys/net/netfilter/nf_conntrack_count` (查看當前連接數)。

## 3. 流量控制 (Traffic Control, TC)

**TC** 是 Linux 內核中用於管理數據包隊列和流量整形（QoS）的子系統。

> **核心概念**：TC 通過在網路設備的出隊列上應用 **排隊規則 (Qdisc)** 來控制數據包的發送順序、速率和延遲。

*   **Qdisc (Queueing Discipline)**：定義了數據包如何從內核發送到網卡。常見的 Qdisc 包括：
    *   **`pfifo_fast`**：默認的簡單 FIFO 隊列。
    *   **`htb` (Hierarchical Token Bucket)**：用於複雜的帶寬分配和優先級控制。
    *   **`tbf` (Token Bucket Filter)**：用於簡單的速率限制。
*   **雲原生關聯**：
    *   **頻寬限制**：CNI 插件（如 Calico 或 Cilium）在實現 Pod 的頻寬限制時，通常會在 Pod 的 Veth 設備上應用 TC 規則。
    *   **服務質量**：Service Mesh 可以利用 TC 標記數據包，實現不同優先級的流量控制。

## 4. 雲原生實踐：Kube-proxy 與 Iptables

Kube-proxy 在 Iptables 模式下，利用 Netfilter 框架實現 Kubernetes Service 的負載均衡。

| Kube-proxy 行為 | Netfilter/Iptables 機制 | 目的 |
| :--- | :--- | :--- |
| **流量劫持** | 在 `nat` 表的 `PREROUTING` 鏈中添加規則。 | 攔截進入 Node 的 Service Cluster IP 流量。 |
| **地址轉換** | 使用 `DNAT` (Destination NAT) 動作。 | 將 Service Cluster IP 和 Port 轉換為後端 Pod 的 IP 和 Port。 |
| **負載均衡** | 使用 Iptables 的隨機匹配擴展（`--statistic mode random`）。 | 在多個後端 Pod 之間隨機選擇一個進行 DNAT。 |

> **Service Mesh 流量劫持**：Service Mesh (如 Istio) 的 Sidecar 容器（Envoy）通過在 Pod 的 Netns 內設置 Iptables 規則，將所有進出 Pod 的流量重定向到 Sidecar 代理，從而實現 L7 流量控制。

---

## 實踐任務：分析 Iptables 規則

請在一個運行中的 Kubernetes Node 上執行以下命令，並嘗試理解其輸出：

1.  **查看 NAT 表規則**：`sudo iptables -t nat -L -n`
    *   **觀察點**：尋找 `KUBE-SERVICES` 和 `KUBE-SVC-` 開頭的鏈。這些是 Kube-proxy 創建的規則，用於 Service 的地址轉換。
2.  **查看 Filter 表規則**：`sudo iptables -t filter -L -n`
    *   **觀察點**：尋找 `KUBE-FIREWALL` 或 `KUBE-NETWORK-POLICY` 相關的鏈，這些是網路策略的實現。
3.  **查看 Conntrack 狀態**：`cat /proc/sys/net/netfilter/nf_conntrack_max` 和 `cat /proc/sys/net/netfilter/nf_conntrack_count`
    *   **觀察點**：比較最大值和當前值，評估 Conntrack 表的壓力。

---
