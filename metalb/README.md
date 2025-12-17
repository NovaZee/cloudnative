# MetalLB 工作原理简述

## 架构组成
- **controller**：监听 `Service`（type=LoadBalancer）与 MetalLB CRDs（如 `IPAddressPool`），分配可用的外部 IP。
- **speaker**：运行在每个节点，负责对外通告 IP（L2/BGP）并维护和负载均衡 IP 与节点的映射。

## 关键 CRD
- `IPAddressPool`：可分配的外部 IP 段。
- `L2Advertisement`：指定哪些池以 L2 方式通告（ARP/NDP），以及参与节点选择。
- `BGPPeer`：定义与外部路由器的对等参数（ASN、地址、密码等）。
- `BGPAdvertisement`：为 BGP 通告设置前缀、社区属性和本地优先级。

## 工作流程
1) 管理员创建 `IPAddressPool` 及对应的广告 CRD（`L2Advertisement` 或 `BGPPeer` + `BGPAdvertisement`）。  
2) 应用创建 `Service`（type=LoadBalancer），controller 从池中分配一个 VIP。  
3) speaker 在节点上接管 VIP 并通告：  
   - **L2 模式**：选出一个 speaker 通过 ARP/NDP 响应，把 VIP 解析到自身节点 MAC。故障时由其他 speaker 接管。  
   - **BGP 模式**：每个 speaker 与外部路由器建立 BGP session，通告 VIP 前缀；路由器按权重/ECMP 将流量转发到节点。  
4) 流量抵达拥有 VIP 的节点后，经 kube-proxy/IPVS（或 eBPF LB）转发到后端 Pod。
   Layer 2 中的 Speaker 工作负载是 DeamonSet 类型，在每台节点上都调度一个 Pod。首先，几个 Pod 会先进行选举，选举出 Leader。Leader 获取所有 LoadBalancer 类型的 Service，将已分配的 IP 地址绑定到当前主机到网卡上。也就是说，所有 LoadBalancer 类型的 Service 的 IP 同一时间都是绑定在同一台节点的网卡上。
当外部主机有请求要发往集群内的某个 Service，需要先确定目标主机网卡的 mac 地址（至于为什么，参考维基百科）。这是通过发送 ARP 请求，Leader 节点的会以其 mac 地址作为响应。外部主机会在本地 ARP 表中缓存下来，下次会直接从 ARP 表中获取。
请求到达节点后，节点再通过 kube-proxy 将请求负载均衡目标 Pod。所以说，假如Service 是多 Pod 这里有可能会再跳去另一台主机。
## 模式对比与注意事项
- **L2 模式**：部署简单、无需外部路由器；单节点活跃，切换依赖节点存活探测。适合小规模或无权配置 BGP 的环境。  
- **BGP 模式**：利用现有路由器做 Anycast/ECMP，多节点同时通告，收敛快、吞吐高；需路由器支持与配置。  
- 预留独立网段，避免与 DHCP/现有网段冲突；确保节点间与上游网络的 ARP/NDP/BGP 连通性和防火墙放行。  
- 建议为 BGP 对等配置密码、限制邻居、监控 BGP session 与 VIP 可达性。
