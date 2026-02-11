# AI 训练/推理的 GPU 硬件与 CUDA 术语详解（工程视角）

本文目标：用“能落到性能/部署问题上的工程视角”解释 AI 训练/推理里常见的 GPU/CUDA 相关名词与概念（如 **SM、CUDA Core、Tensor Core、HBM、cuBLAS、cuDNN、NCCL、TensorRT、TF32、BF16、FP8、KV Cache** 等），帮助你：

- 读懂显卡规格与“为什么这张卡更适合训练/推理”
- 理解训练/推理性能瓶颈出现在哪里（算力/带宽/调度/通信）
- 面对问题时知道“该看哪个指标、该调哪一类参数”

> 说明：本文以 **NVIDIA CUDA** 生态为主（因为涉及 CUDA/Tensor Core/cuDNN 等）。不同 GPU 架构细节会变，但“概念与分析方法”相对稳定。

---

## 阅读导航（按问题找答案）

- 想搞懂 **GPU 为什么适合 AI**：看「2) GPU 计算模型（SIMT）」+「3) SM/warp」
- 想搞懂 **CUDA Core vs Tensor Core**：看「3.3」+「7) 精度与 Tensor Core」
- 想搞懂 **为什么推理 decode 慢 / KV cache 很吃显存**：看「9) 推理」+「4) 内存层级」
- 想搞懂 **为什么多卡训练扩展效率差**：看「10) 互连与并行」+「6.4/10.2 NCCL 通信」
- 想系统化 **定位瓶颈**：看「11) 指标与 Profiling」+「12) 排障清单」

---

## 0) 如何读 GPU 规格（AI 视角）

显卡规格里最容易误导人的两点：

1) **CUDA Core/Tensor Core 数量不等于性能**（跨架构不可直接对比）  
2) **峰值 TFLOPS/TOPS 只代表上限**（实际取决于是否命中高效 kernel、是否被带宽/通信卡住）

建议按下面的优先级读规格。

### 0.1 先判断：你更像“算力型”还是“带宽型”？

- **算力型（compute-bound）**：大矩阵乘/卷积、训练主干、prefill、离线大 batch 推理  
  更看重：Tensor Core 吞吐（FP16/BF16/FP8/INT8/TF32）、SM 规模与频率、算子实现质量。

- **带宽型（memory-bound）**：推理 decode（KV cache 访问）、embedding/softmax/layernorm、很多 elementwise/reduction、小 batch  
  更看重：HBM 带宽、L2 容量与带宽、访存模式（连续/复用/随机）、kernel fusion。

非常实用的经验：

- **如果换更强的 Tensor Core（或更低精度）却提速有限**，很可能已经不是算力瓶颈，而是带宽/访存/调度（launch）/通信瓶颈。

### 0.2 AI 视角最关键的规格清单

1) **显存容量（GB）**  
决定“能不能跑”：参数/梯度/优化器状态/激活/KV cache/batch/seq 都消耗显存。

2) **显存带宽（GB/s）**  
决定“很多场景能不能快”：尤其 decode 与各类内存受限算子。

3) **互连与拓扑（PCIe/NVLink/NVSwitch/IB）**  
决定多卡上限：DP 的 all-reduce、TP 的 all-gather/reduce-scatter 都吃通信。

4) **支持的精度与 Tensor Core 模式**  
例如 FP16/BF16/TF32/INT8/FP8/INT4（看硬件支持 + 软件栈是否成熟）。

5) **L2 与片上资源（cache/shared/register）**  
对推理与融合 kernel 非常关键；同样 HBM 带宽下，L2 更大/更快往往更“抗随机访问/小 batch”。

### 0.3 常见“规格误区”

- 只看 **FP32 TFLOPS**：深度学习多数核心算子用 Tensor Core（FP16/BF16/TF32/FP8/INT8），FP32 峰值不是主要指标。
- 只看 **显存容量**：容量够但带宽不够，decode 也会慢；容量不够则直接 OOM。
- 只看 **单卡**：实际训练/推理常受 CPU、PCIe、网络、存储、数据管道限制。

---

## 1) 从框架到硬件：一条链路理解“性能问题在哪层”

以 PyTorch 为例，路径通常是：

1. Python/模型代码（算子调用）
2. 框架算子实现（ATen / Triton / XLA 等）
3. GPU 库（cuBLAS/cuDNN/NCCL/TensorRT…）或自定义 CUDA kernel
4. CUDA Runtime/Driver（stream、内存、kernel launch、JIT）
5. GPU 硬件（SM、Tensor Core、HBM、NVLink…）

**性能问题**常见来源：

- 算子形状不友好（导致走不到高效 kernel）
- dtype/布局不匹配（Tensor Core 没吃满）
- 访存模式差（带宽打满、L2 miss 多）
- kernel 过碎（launch/sync 开销大）
- 多卡通信拖后腿（NCCL/网络/拓扑问题）
- 数据管道（CPU 解码/预处理）供给不足

### 1.1 术语对齐：算子、kernel、fusion、library kernel

- **算子（operator/op）**：框架的计算单元（matmul/softmax/layernorm…）
- **kernel（CUDA kernel）**：最终在 GPU 上执行的函数；一个算子可能对应多个 kernel
- **fusion（融合）**：把多个算子合并成更少 kernel，减少：
  - kernel launch 次数（CPU 侧开销）
  - 中间张量写回 HBM 的带宽消耗
- **library kernel**：来自 cuBLAS/cuDNN 等成熟库，通常对常见形状极致优化  
  **custom kernel**：自写 CUDA/Triton，用于融合、特殊布局或新算子

---

## 2) GPU 计算模型（SIMT）：吞吐优先、靠并行隐藏延迟

### 2.1 GPU vs CPU：根本差异

- CPU：核心少但强，擅长复杂控制流、低延迟、分支密集任务
- GPU：核心很多但简单，擅长数据并行、高吞吐，依靠大规模并行隐藏内存延迟

### 2.2 SIMT 与 Warp：GPU 不是“每线程独立跑”

CUDA 的执行模型更准确地说是 **SIMT（Single Instruction, Multiple Threads）**：

- 线程被打包为 **warp**（NVIDIA 上 warp 通常是 32 线程）
- 硬件按 warp 调度：一个 warp 同一时刻执行同一条指令（对不同线程的数据执行）

这带来两个关键现象：

1) **分支发散（warp divergence）**  
同 warp 内线程走不同分支时，GPU 通常需要把分支“轮流执行”（用 mask 关闭不相关线程），等价于串行化。

2) **延迟隐藏（latency hiding）**  
当某个 warp 等待 HBM 数据（高延迟）时，SM 切换到另一个 ready warp 执行。  
因此：SM 上能同时驻留足够多 warp（occupancy）常常很重要（但 occupancy 高也不必然更快）。

### 2.3 吞吐 vs 延迟：训练与推理为什么感觉不一样？

- **训练**：通常追求吞吐（samples/sec），batch 大、GEMM 大，更容易把 Tensor Core 吃满
- **推理**：
  - 离线批量推理：更像训练（吞吐导向）
  - 在线生成（decode）：小 batch、频繁调度、KV cache 访存重，常对带宽/延迟更敏感

---

## 3) GPU 硬件核心术语：SM / CUDA Core / Tensor Core / Occupancy

### 3.1 SM（Streaming Multiprocessor）是什么？

你可以把 SM 看成 GPU 的“并行计算工厂”。一个 SM 里通常包含：

- warp 调度器（挑选 ready warp 发射指令）
- 寄存器文件（每线程的最快存储）
- shared memory（片上共享内存，block 内共享）
- L1 cache（与 shared 关系随架构变化）
- CUDA Core（常规 FP/INT 执行单元）
- Tensor Core（矩阵乘加 MMA 单元）
- Load/Store 单元、特殊函数单元（SFU）、原子等

#### 3.1.1 SM 在做什么：轮转多个 warp “填满流水线”

一个 SM 同时驻留多个 block/warp。调度器不断从 ready 的 warp 里选一个发射指令：

- 某个 warp 等内存/依赖 → 换另一个 warp 跑
- 这就是 GPU 隐藏内存延迟的核心机制之一

因此 SM 性能同时受：

- 算术吞吐（CUDA/Tensor Core 理论能力）
- 访存效率（coalescing、cache hit、复用）
- 资源占用（寄存器/shared 占用导致并发 block/warp 变少）
- 同步/原子/分支发散（导致 warp stall）

### 3.2 Thread / Warp / Block / Grid（CUDA 编程抽象）

- **thread**：最小编程单元
- **warp**：硬件调度单元（通常 32 thread）
- **block**：一组 thread 的集合，可用 shared memory 并可同步
- **grid**：一次 kernel launch 的所有 blocks 集合

一个关键事实：**block 不跨 SM 迁移**。block 被分配给某个 SM 后会在那里执行到结束，因为它需要 shared memory 与 block 内同步语义。

### 3.3 CUDA Core vs Tensor Core：到底区别在哪？

**CUDA Core（常规执行单元）**：

- 执行标量/向量运算：FP/INT、FMA、部分逻辑/地址计算等
- 常见于：elementwise、reduction、部分归一化/激活、控制相关代码

**Tensor Core（矩阵乘加单元）**：

- 执行 MMA（Matrix Multiply-Accumulate）：`D = A×B + C`
- 优势：在支持的 dtype/形状上，吞吐远高于用 CUDA Core 做同等矩阵乘
- 常见于：GEMM、conv（可转为 GEMM）、attention 的 QK^T / PV、MLP 线性层

工程含义：

- 模型里的“大头 GEMM”希望走 Tensor Core
- 但模型里仍有大量非 GEMM 的算子（softmax、layernorm、采样）更依赖带宽、CUDA Core 与融合

### 3.4 Occupancy（占用率）怎么理解更靠谱？

occupancy 直觉：**一个 SM 上能同时驻留多少 warp**。它受很多硬件资源限制：

- 每线程寄存器使用量（寄存器文件有限）
- 每 block shared memory 使用量（shared 容量有限）
- 每 SM 最大 block/warp 数限制

但要记住两点：

1) occupancy 高不一定更快（compute-bound 时可能更依赖数据复用/指令级并行）  
2) occupancy 太低常会很慢（memory-bound 时隐藏不了内存延迟）

> 常见坑：寄存器使用过多导致 spill 到“local memory”（本质在 HBM），性能会断崖式下降。

---

## 4) 内存层级与数据搬运：为什么“带宽”常是第一瓶颈

AI workload 经常被内存相关因素限制，尤其是推理 decode 与大量融合/归一化/softmax 等算子。

### 4.1 GPU 内存层级（从快到慢）的“工程直觉表”

不同架构绝对值不同，但层级关系非常稳定：

| 层级 | 位置 | 作用域 | 典型用途 | 典型问题 |
|---|---|---|---|---|
| Register | SM 内 | thread | 临时变量、fragment | 用多 → occupancy 降；spill → 变慢 |
| Shared Memory | SM 内 | block | tile 缓存、数据复用（flash-attn 等） | bank conflict；用多 → 并发 block 降 |
| L1 Cache | SM 内 | SM | 自动缓存部分 global 访问 | stride/随机访问容易 miss |
| L2 Cache | 全 GPU | device | 跨 SM 复用、减少 HBM 流量 | miss 多时回到 HBM；多租户分摊 |
| HBM/GDDR（显存） | 板载 | device | 参数/激活/KV cache | 延迟高、带宽有限，随机访问很贵 |
| Host DRAM（主存） | CPU | host | 数据加载、offload | PCIe 传输慢；非 pinned 难 overlap |

### 4.2 memory coalescing（合并访存）

warp 内线程访问内存时，硬件会尽量把访问合并为更少的事务。访问越连续、越对齐，带宽利用率越高。

工程上常见表现：

- layout（NCHW/NHWC）不同，conv/attention 性能差距明显
- 维度顺序、stride、对齐改变，kernel 性能可能成倍变化

### 4.3 shared memory 的两大关键：复用与 bank conflict

shared memory 主要价值是：**把会重复使用的数据放到片上**，减少 HBM 访问。  
但 shared memory 也有“并行访问冲突”的问题（bank conflict）：多个线程同时访问同一个 bank 会串行化。

高性能 kernel（如 GEMM/flash-attn）的核心工作就是：

- 把 A/B tile 搬到 shared（或用更高级的片上机制）
- 用 Tensor Core/向量单元做计算
- 尽量减少 shared/HBM 的冲突与冗余访问

### 4.4 pinned memory（页锁定内存）与 H2D/D2H

GPU 与 CPU 之间通过 DMA 传输。想要高吞吐并与计算重叠，常需要 pinned memory：

- pinned：页不会被 OS 移动，DMA 更高效，也更容易异步
- pageable：可能触发额外拷贝与同步，吞吐更差

在 PyTorch 中常见对应：

- DataLoader `pin_memory=True`
- 预取、异步拷贝与 stream（见第 5 节）

### 4.5 Unified Memory（统一内存）的一句话理解

Unified Memory 让你像用一块“统一地址空间”一样访问 CPU/GPU 内存，但背后可能发生 **页面迁移（page migration）**。  
它对易用性很友好，但对性能可预测性不友好：迁移触发时可能带来明显的停顿。

---

## 5) CUDA Runtime/Driver：Stream/Event/Graph、异步与“发射开销”

很多“看起来 GPU 很闲/很忙”的误判，来自对 CUDA 异步语义理解不清。

### 5.1 CUDA Driver vs CUDA Runtime

- NVIDIA Driver：管理 GPU、内存映射、调度、上下文等（“驱动太老”会导致新 CUDA/新库不可用）
- CUDA Runtime（libcudart）：更易用的 API（`cudaMalloc/cudaMemcpy`、kernel launch），底层调用 Driver API
- CUDA Driver API（libcuda）：更底层更显式（context/module/stream）

### 5.2 stream：GPU 上的“任务队列”

可以把 stream 当作 GPU 上的一条队列：

- 同一 stream 内：按顺序执行
- 不同 stream 间：可能并发（取决于依赖与硬件资源）

常见结论：

- 想 overlap “拷贝与计算”，通常要用不同 stream，并且数据在 pinned memory
- 很多框架默认帮你管理 stream，但性能调优时必须理解它在做什么

### 5.3 event：标记与同步

event 常用于：

- 精确计时（GPU 侧时间）
- 跨 stream 同步（例如 stream A 跑完再让 stream B 继续）

### 5.4 kernel launch（发射）开销与“kernel 过碎”

每个 kernel 都需要 CPU 发射，涉及参数准备、调度、驱动交互等。  
当你的 workload 由大量小 kernel 组成时（常见于推理 decode、很多 elementwise），launch 开销可能成为瓶颈。

常见优化方向：

- 算子融合（fusion）
- 使用 fused kernel（flash-attn、fused MLP、fused LN）
- CUDA Graphs（下面）

### 5.5 CUDA Graphs：减少重复发射成本

CUDA Graphs 可以把“一段固定的 GPU 工作负载”捕获成图并重复执行：

- 优点：减少 CPU 参与，降低 launch 开销，稳定延迟
- 限制：对动态 shape、动态控制流不友好（需要重 capture 或多图）

工程直觉：

- 大训练任务（大 kernel）收益可能不大
- 小 batch 推理 / decode 可能收益明显

### 5.6 PTX / SASS / Compute Capability：兼容性与 JIT

- PTX：类似“虚拟 ISA”，可能在运行时 JIT 成目标 GPU 的机器码
- SASS：实际机器码（架构相关）
- Compute Capability：GPU 架构能力标识（决定支持哪些指令/精度模式）

工程建议：

- 发布时要考虑多架构（fatbin）或在目标环境构建，避免架构不匹配

---

## 6) CUDA 生态库：cuBLAS/cuDNN/NCCL/TensorRT（它们各自解决什么）

### 6.1 cuBLAS / cuBLASLt：GEMM 的“底座”

- cuBLAS：经典线性代数库，提供高性能 GEMM
- cuBLASLt：更灵活，支持更多 layout、epilogue（bias/activation 等）、更丰富的融合与启发式选择

为什么 GEMM 是 AI 的基本盘？

- 线性层、attention、MLP 本质都在做矩阵乘
- 只要 GEMM 跑得好（并减少中间张量读写），整体性能通常显著提升

### 6.2 cuDNN：深度学习算子库（尤其卷积）

cuDNN 覆盖：

- Conv（不同算法路径：直接/隐式 GEMM/FFT/Winograd 等，随版本变化）
- 归一化/激活等常见模式（覆盖范围随版本演进）
- 部分 transformer 相关加速（随版本变化）

工程上常见规律：

- workspace 有时能换速度（但不是绝对）
- “确定性（deterministic）”设置可能让你放弃更快算法

### 6.3 NCCL：多 GPU 通信库

NCCL 为多卡提供高性能集合通信：

- all-reduce / all-gather / reduce-scatter / broadcast / send/recv
- 底层利用 PCIe/NVLink/NVSwitch/IB（看机器）

通信瓶颈非常常见：当通信占比高时，单卡算力再强也无济于事。

### 6.4 TensorRT：推理编译/优化引擎

TensorRT 面向推理：

- 图优化、算子选择、精度降级（FP16/INT8/FP8…）、融合
- 生成 engine（类似编译产物），在固定/受控 shape 下可获得很高性能

工程权衡：

- 性能强，但对动态 shape、算子覆盖、以及构建流程更敏感（可能需要 plugins）

### 6.5 你还会常见到的工具/库（知道它们做什么即可）

- CUTLASS：NVIDIA 的模板化 CUDA kernel 库（很多 GEMM/attention kernel 的基础）
- Triton：更高层的 kernel 编写语言，常用于融合与快速迭代
- cuSPARSE / cuFFT：稀疏与 FFT（部分模型/场景会用）

---

## 7) 精度体系与 Tensor Core：FP16/BF16/TF32/FP8/INT8/INT4

### 7.1 FP16 vs BF16：为什么 BF16 往往更“好训”

两者都是 16-bit 浮点，但表示能力不同：

- FP16（IEEE half）：指数位少 → 动态范围较小；尾数相对更细
- BF16（bfloat16）：指数位接近 FP32 → 动态范围大；尾数更粗

工程含义：

- BF16 更不容易溢出/下溢，训练稳定性通常更好，很多场景不必依赖 loss scaling
- FP16 生态成熟，某些 kernel 支持也更广，但更依赖混合精度策略与数值技巧

### 7.2 TF32：用 Tensor Core 加速“看起来是 FP32”的 matmul

TF32 是一种折中路径：

- 输入有效精度降低（近似），但累加精度更高
- 常用于加速 FP32 matmul（具体行为由框架与硬件决定）

这解释了为什么很多时候“FP32 也挺快”：实际 matmul 可能走了 TF32 的 Tensor Core 路径。

### 7.3 FP8：更激进的低精度（训练/推理都可能用）

FP8 常见模式（例如不同 exponent/mantissa 配置）用于在更低带宽与更高吞吐下训练/推理，但通常需要更复杂的 scaling/校准策略来保证数值稳定。

工程直觉：

- FP8 能显著降低显存带宽压力并提高矩阵乘吞吐
- 但更依赖成熟软件栈与合适的缩放策略（否则精度/收敛风险更高）

### 7.4 INT8/INT4：量化在工程上到底在省什么？

量化的核心价值：

- 更小的模型与更少的带宽消耗（权重/激活更小）
- 在支持的硬件/算子上，整数路径更快

常见分类：

- weight-only quant：只量化权重，激活保持 FP16/BF16（落地更容易）
- activation quant：激活也量化（更难，但潜力更大）
- scale 粒度：per-tensor / per-channel / per-group（粒度越细精度越好，但实现更复杂）

### 7.5 为什么“Tensor Core 用不上 / 用不满”？（详细版）

常见原因（按出现频率）：

1) **dtype 不匹配**：没有走到支持 Tensor Core 的路径（例如某些算子仍是 FP32）
2) **形状不友好**：矩阵维度过小、或对齐差（很多高效 kernel 假设某些维度能被 8/16 等整除，具体随 dtype/实现变化）
3) **layout/stride 不合适**：非连续内存导致额外 packing/transpose，收益被吃掉
4) **batch 太小（尤其 decode）**：launch 开销与访存占比更大
5) **算子链路不平衡**：GEMM 很快但前后 memory-bound 算子很慢，总体仍受限

---

## 8) 训练（Training）：硬件视角下的瓶颈与并行

训练通常由：

- forward
- backward
- optimizer step

组成。

### 8.1 训练显存花在什么地方？

训练显存通常由几部分组成：

- 参数（weights）
- 梯度（grads）
- 优化器状态（例如 Adam 的 m/v）
- 激活（activations，用于反向）
- 临时 workspace（cuDNN/cuBLAS）

因此“同样 7B 模型”，不同策略（BF16/FP16/FP8、是否 activation checkpointing、是否 ZeRO/FSDP）显存会差很多。

### 8.2 为什么训练更容易吃满 Tensor Core？

训练的 GEMM/conv 通常更大（更高算强比），更容易：

- 让 Tensor Core 长时间工作
- amortize（摊薄）kernel launch 开销

### 8.3 分布式训练的三种并行（概念级）

你会经常看到：

- **DP（Data Parallel）**：各卡算完整模型的不同 batch → 反向做梯度 all-reduce
- **TP（Tensor Parallel）**：把矩阵乘切分到多卡 → 每层需要 all-gather/reduce-scatter（更频繁、延迟敏感）
- **PP（Pipeline Parallel）**：把层切成 stage → stage 间 send/recv 激活（依赖调度与重叠）

工程结论：并行方式不同，通信模式不同，对互连带宽/延迟的敏感度也不同。

---

## 9) 推理（Inference）：prefill/decode、KV cache、带宽与延迟

LLM 自回归推理常分两段：

- **prefill**：把 prompt 跑一遍（大 GEMM/attention，吞吐导向）
- **decode**：逐 token 生成（小 batch、频繁调度、带宽/延迟敏感）

### 9.1 KV Cache：为什么它又吃显存又吃带宽？

注意力在 decode 时需要访问历史 token 的 K/V，因此：

- KV cache 占用随 seq 增长线性增加（显存压力）
- 每步都要读写 KV（带宽压力）

一个用于量级感知的估算：

```text
KV bytes 约 ≈ 2(K+V) * num_layers * batch * seq * hidden_size * bytes_per_element
```

（不同实现会有 head/head_dim 的拆分，但量级一致）

### 9.2 在线推理为什么经常被“很多小 kernel”拖慢？

decode 的每步计算量相对有限，且包含采样、softmax、归一化等多种算子，容易出现：

- kernel 很碎 → launch 开销占比上升
- 访存占比高 → Tensor Core 很强也跑不快

常见优化方向：

- 融合与高性能 attention（flash-attn、paged attention 等）
- CUDA Graphs（稳定 shape 时非常有用）
- 更合理的 batching（continuous batching）与 KV cache 管理

### 9.3 量化在推理中的常见价值

推理中量化常用于：

- 减少权重体积（显存占用下降）
- 降低带宽压力
- 在支持的 kernel 上提升吞吐/降低延迟

但要权衡：

- 精度损失与校准成本
- 算子覆盖与部署复杂度（动态 shape、plugins 等）

---

## 10) 互连与系统：PCIe/NVLink/NVSwitch/IB、NUMA、MIG

### 10.1 互连决定多卡上限

多卡训练/推理都需要卡间通信：

- DP：梯度 all-reduce（大带宽需求）
- TP：层内 all-gather/reduce-scatter（频繁、延迟敏感）
- PP：stage 间 send/recv（依赖调度与重叠）

这些通信路径可能是：

- 同机：PCIe 或 NVLink（以及 NVSwitch）
- 跨机：IB/RoCE +（可能的）GPUDirect RDMA

### 10.2 NUMA：CPU/PCIe 拓扑也会影响 GPU

即使是同一台机器，不同 GPU 可能挂在不同 CPU socket/NUMA 节点上：

- 跨 NUMA 的 CPU↔GPU 通信会更慢
- NCCL 的性能也受拓扑影响

工程上常见现象：同样 8 卡，有的机器 all-reduce 很快，有的很慢，原因可能就是拓扑与绑定策略。

### 10.3 MIG（Multi-Instance GPU）

MIG 可把一张 GPU 切成多个“逻辑 GPU”，常用于多租户推理隔离：

- 好处：隔离与配额更清晰、可控
- 代价：每个实例可用的 SM/L2/HBM 带宽都被分割；对大模型/高吞吐场景可能不合适

---

## 11) 指标与 Profiling：怎么判断是算力/带宽/通信/发射瓶颈？

### 11.1 Roofline：一条公式理解“为什么提精度/提 TFLOPS 没用”

一个非常实用的近似模型：

```text
实际性能 ≈ min(峰值算力, 内存带宽 × 算强比)
```

- 算强比（Operational Intensity）：每搬 1 byte 能做多少 FLOPs
  - GEMM：算强比高，偏 compute-bound
  - elementwise：算强比低，偏 memory-bound

这解释了为什么：

- GEMM 很快，但 layernorm/softmax 仍慢（带宽/访存）
- decode 阶段 Tensor Core 再强也可能被 KV 带宽压住

### 11.2 “GPU utilization 100% 但还是慢”怎么理解？

`nvidia-smi` utilization 很粗：它更像“GPU 有没有在执行 kernel”，不告诉你“在等内存/在做有效计算”。

更有区分度的指标（通常需要 profiler/counter）：

- Tensor Core utilization（是否真的在跑 MMA）
- DRAM throughput、L2 hit rate（是否被带宽/缓存 miss 卡住）
- warp stall reasons（为什么 warp 在停：memory dependency、sync、execution dependency 等）
- NCCL 通信时间占比（是否 comm-bound）

### 11.3 常用工具（知道用途即可）

- Nsight Systems：看时间线（CPU↔GPU、kernel 顺序、拷贝、同步、NCCL）
- Nsight Compute：看单 kernel 的算力/带宽/占用率/指令统计
- 框架 profiler：定位到算子级别（哪几个 op 最耗时）

---

## 12) 常见问题排障清单（从现象到方向）

### 12.1 Tensor Core 利用率低

优先检查：

- dtype 是否支持（FP16/BF16/TF32/FP8/INT8）
- 形状是否过小/对齐差（是否命中高效 kernel）
- layout/stride 是否导致额外 transpose/packing
- 是否被前后 memory-bound 算子拖慢（整体看起来 Tensor Core 不忙）

### 12.2 GPU util 很高但吞吐很差

可能原因：

- 内存带宽打满（memory-bound）
- kernel 很碎（launch/sync 开销高）
- 大量同步（隐式 sync）

### 12.3 训练 OOM（显存不够）

优先拆解显存组成：

- 参数/梯度/优化器状态/激活/workspace 哪个最大？
- 是否可用：混合精度、activation checkpointing、ZeRO/FSDP、offload、缩短 seq/减小 batch

### 12.4 多卡扩展效率差

优先判断：

- DP 还是 TP/PP？
- NCCL 时间占比是否过高？
- 拓扑（PCIe/NVLink/NUMA）是否不理想？

---

## 13) 典型 AI 算子如何映射到 GPU（从“模型结构”理解硬件瓶颈）

这一节的目标是把前面的“硬件/库/精度概念”落回到你每天看到的模型算子上：哪些主要吃 Tensor Core，哪些主要吃带宽，为什么某些优化（flash-attn、融合、量化）会有效。

### 13.1 Linear/GEMM（线性层）= Tensor Core 的主战场

以 `Y = XW^T` 为例（batch 维度省略）：

- 计算是典型 GEMM：`(m×k) × (k×n) -> (m×n)`
- 当 `m/n/k` 足够大、dtype 合适、对齐良好时，最容易吃满 Tensor Core

你经常会看到的“提升 GEMM 性能”的手段，本质上是在做：

- **tile 化**：把矩阵切成块，让块能在 shared/L2 复用，减少 HBM 访问
- **选择更合适的 layout**：减少转置/packing
- **epilogue 融合**：把 `bias + activation + dropout` 等后处理融合进 GEMM 的输出阶段，减少一次写回 + 再读

这也是为什么很多高性能实现会偏向 `cuBLASLt`：它更容易表达 layout 与 epilogue/fusion。

### 13.2 Attention（prefill）：既吃 Tensor Core，也容易被带宽/中间张量拖慢

标准 attention（简化）：

1) `Q = XWq, K = XWk, V = XWv`（三次 GEMM）
2) `S = QK^T`（GEMM）
3) `P = softmax(S)`（reduction + elementwise，偏带宽/延迟敏感）
4) `O = PV`（GEMM）

这里最常见的性能问题是：

- `S`（注意力分数矩阵）非常大，如果把它完整写回 HBM，再读回来做 softmax，会产生巨大的带宽压力

因此 flash attention 的核心思想是：

- **不显式 materialize 大矩阵 S**（或尽量减少写回）
- 用更小的 tile 在片上完成 `QK^T -> softmax -> PV` 的流水化计算
- 让中间结果尽量停留在寄存器/shared/L2，避免 HBM 往返

你可以把它理解为：把 attention 从“算力问题”变成“数据搬运问题”，并用更聪明的 tile/融合把搬运压到最低。

### 13.3 Attention（decode）：KV cache 访问常让它变成带宽瓶颈

decode 阶段每步生成一个 token：

- Q 通常只对应 1 个 token（或很小的 m）
- 但 K/V 要覆盖历史全部 token（seq 很长）

这导致：

- 矩阵规模小、kernel 更碎 → launch 开销占比上升
- KV cache 访问量大 → 更容易 memory-bound

因此你经常会看到推理优化围绕：

- KV cache 的 layout（更好 coalescing）
- KV cache 的分块/分页（paged attention，减少碎片与无效拷贝）
- 更合理的 batching（continuous batching，提升有效矩阵规模）
- 量化（降低 KV/权重带宽与容量压力）

### 13.4 LayerNorm/RMSNorm：典型 memory-bound，靠融合获益最大

LayerNorm/RMSNorm 通常包含：

- 对特征维做 mean/var 或平方和（reduction）
- 再做归一化与缩放（elementwise）

特点：

- 计算量相对小，但要读写大量激活 → 很容易被 HBM 带宽限制
- 单独一个 LN kernel 往往不如“把 LN 和前后 elementwise 融合”来得有效

这解释了为什么推理/训练中常见：

- fused layernorm / fused add+norm / fused bias+gelu 等优化

### 13.5 Softmax：reduction + exp + normalize，常见瓶颈在带宽与数值稳定

softmax 的核心是：

- reduction（max/sum）
- exp 与归一化（elementwise）

工程上常见优化：

- 使用稳定版本（减 max）
- 将多个步骤融合（减少中间写回）
- 在 attention 中与前后步骤融合（flash-attn 思路）

### 13.6 Embedding/Gather：更像“内存随机访问”问题

embedding lookup 往往是：

- 根据 token id 去表里 gather 行
- 访问模式可能很随机 → cache miss 多 → 带宽/延迟敏感

常见工程策略：

- 提升 batch/合并请求（提高 locality）
- 量化/压缩 embedding（降低带宽）
- 更合理的表分片与缓存（分布式场景）

### 13.7 Convolution：cuDNN 为什么有这么多算法？

conv 可以用多种方式实现（概念层）：

- 直接卷积
- 转成 GEMM（im2col/implicit GEMM）
- FFT/Winograd（某些形状下更快）

cuDNN 会根据：

- 输入形状、stride、group、dtype
- workspace 限制
- 是否要求 deterministic

选择不同算法路径。你看到的“换个 batch/换个 layout 就快很多”，很多时候就是算法选择发生了变化。

---

## 14) 术语速查（按类别）

### 14.1 硬件与执行模型

| 名词 | 是什么 | 为什么重要 |
|---|---|---|
| SM | GPU 并行计算单元 | 决定并行度、资源限制与调度 |
| Warp | 硬件调度单位（通常 32 线程） | 决定分支发散、访存合并 |
| Block | 一组线程，含 shared/sync 语义 | 影响 shared/寄存器与 occupancy |
| CUDA Core | 常规 FP/INT 单元 | 许多非 GEMM 算子依赖它 |
| Tensor Core | MMA 矩阵乘加单元 | GEMM/attention/MLP 的关键加速 |
| HBM/GDDR | 显存 | 容量与带宽决定很多上限 |
| L2/L1/Shared/Register | 缓存/片上存储层级 | 决定数据复用与带宽效率 |

### 14.2 软件栈与库

| 名词 | 是什么 | 常见场景 |
|---|---|---|
| CUDA Runtime/Driver | 管理 kernel/内存/stream | 所有 CUDA 程序底座 |
| cuBLAS/cuBLASLt | GEMM/线代库 | 线性层、attention、MLP |
| cuDNN | DNN 算子库 | 卷积与相关算子 |
| NCCL | 多卡通信库 | all-reduce/all-gather 等 |
| TensorRT | 推理优化/编译 | 部署高性能推理引擎 |
