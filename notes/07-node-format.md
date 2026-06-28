# 笔记 07：B+Tree 节点字节布局（正式契约）

> 这是 ch03 全部代码的**契约**。3.2~3.8 的每一行解析/写入都必须服从这份格式。
> 改这份文档 = 改数据库的磁盘格式，慎重。
>
> 设计推导见 [[06-search-structures-theory]] §5、§9；这里只钉"最终长什么样 + 为什么这样"。

---

## 0. 一句话

**一个 B+Tree 节点 = 一个 Pager page = 4096 字节**。节点内部是**变长** KV，
用一个**定长 offset 数组**做"间接层"恢复 O(1) 随机访问，从而支持节点内二分。

---

## 1. 评判这份布局的唯一尺子（成本模型）

来自 [[06-search-structures-theory]] §5.4：磁盘上"摸一页(IO/page fault)"≫"比一次大小"。
所以布局只为一件事服务——**最大化扇出**：

```
扇出 ≈ 4096 / (平均每个 key 占的字节数)
扇出越大 → 树越矮 (log_B n) → 摸页(IO)次数越少
```

由此派生两条铁律：

- **per-node 开销（header）随便花**：摊到一页几百个 key ≈ 0（4B/180 ≈ 0.02 B/key）。
- **per-key 开销（offset、klen、vlen）往死里抠**：每字节 × 扇出，直接进上面那个分母。

---

## 2. 整体结构（叶子 / 内部 **统一头部**）

```
byte 0
┌────────────────────────────────────────────────┐
│ HEADER  (固定 12 字节, per-node)                 │
│   type    uint16   叶子=1 / 内部=2               │
│   nkeys   uint16   本节点 key 数量               │
│   sibling uint64   叶子: 右兄弟叶子页号(0=无)    │
│                    内部: 不用, 填 0              │
├────────────────────────────────────────────────┤
│ OFFSET 数组  (nkeys × 2 字节, per-key)           │
│   offset[i] uint16  第 i 条 KV 的【终点】        │
│   (相对 KV 区起点; 定长 → 可二分)                │
├────────────────────────────────────────────────┤  ← KV 区起点 = 12 + nkeys*2
│ KV 区  (变长, 从前往后堆)                         │
│   每条:  klen uint16 | vlen uint16 | key | val   │
├────────────────────────────────────────────────┤
│ FREE SPACE  (剩余, 不要求清零, 但新建议清零)     │
└────────────────────────────────────────────────┘
byte 4096
```

**为什么统一头部**：内部节点白搭 8B sibling，但摊到 per-node ≈ 0；换来叶子/内部
**一套解析路径**（代码 + dbdump 都不分叉）。成本模型下这笔交易血赚。

---

## 3. OFFSET 数组语义：存"终点"

`offset[i]` = 第 i 条 KV 的**结束字节位置**（相对 KV 区起点）。约定 `offset[-1] = 0`。

一个数组同时是 **分隔符 + 长度表 + 空间计**：

```
第 i 条 KV 起点 = offset[i-1]           (i=0 时为 0)
第 i 条 KV 长度 = offset[i] - offset[i-1]
节点已用字节   = HEADER + nkeys*2 + offset[nkeys-1]
节点剩余空闲   = 4096 - 已用字节         ← 判断"该不该分裂"靠它
```

二分查找时：读 `offset[mid]`（定长数组，O(1)）→ 跳到对应 KV → 比 key → 砍半。
节点内 `log₂(nkeys)` 次比较因此被救回（变长本来丢了乘法寻址）。

---

## 4. KV 区每条的编码

```
┌────────┬────────┬───────────┬───────────┐
│ klen   │ vlen   │ key bytes │ val bytes │
│ uint16 │ uint16 │ (klen)    │ (vlen)    │
└────────┴────────┴───────────┴───────────┘
```

- **叶子**：val = 真实 value（用户数据）。
- **内部**：val = **子页号 (uint64, 8 字节)**，所以内部节点恒有 `vlen = 8`；
  key = 该子树的**最小 key**（见 §5）。

key 在节点内**按 key 升序排列**（这是能二分的前提）。

---

## 5. 内部节点：n 个 key ↔ n 个孩子（约定，细节在 3.5）

教科书 B+Tree 内部节点是"n 个 key + n+1 个孩子"。我们用一个更顺手的约定消掉那个 +1：

> **第 i 个 key = 第 i 个孩子子树里的最小 key。** → n 个 key 对 n 个孩子。

- 下降规则：找**最大的 `key[i] ≤ X`**，进 `child[i]`。
- 成立条件：`key[0]` = 整棵子树最小 key，任何能到达本节点的 X 都 ≥ key[0]。

这样内部节点完全复用 §4 的 KV 机制（value = 8B 子页号），无需单独的指针数组。
搜索 / 分裂 / 第一个 key 是否要哨兵，留到 **3.5** 详细设计。

---

## 6. 常量表（代码里照抄）

```go
const (
    PageSize = 4096

    // 节点类型
    NodeLeaf     = 1
    NodeInternal = 2

    // HEADER 字段偏移
    offType    = 0  // uint16
    offNKeys   = 2  // uint16
    offSibling = 4  // uint64
    headerSize = 12 // = 2 + 2 + 8

    // 派生
    // offsetArrayStart = headerSize
    // kvAreaStart      = headerSize + nkeys*2
)
```

宽度选择理由：

| 字段 | 宽度 | 频率 | 为什么这个宽度 |
|---|---|---|---|
| type | uint16 | per-node | 对齐好看 + 留未来类型 |
| nkeys | uint16 | per-node | 一页最多 ~580 key < 65535 |
| sibling | uint64 | per-node | = Pager page id 类型 |
| offset[i] | uint16 | **per-key** | 最大偏移 4096 < 65536，最省 |
| klen/vlen | uint16 | **per-key** | 长度 < 4096 < 65536 |

哨兵：sibling = **0** 表示"无右兄弟"（page 0 永远是 meta，绝不会是叶子，故 0 安全）。

---

## 7. 一个真实叶子的 hex（cat→meow, dog→woof, sibling=页7）

```
       00 01 02 03 04 05 06 07 08 09 0A 0B 0C 0D 0E 0F
0x00 │ 01 00 02 00 07 00 00 00 00 00 00 00 0B 00 16 00 │ ................
0x10 │ 03 00 04 00 63 61 74 6D 65 6F 77 03 00 04 00 64 │ ....catmeow....d
0x20 │ 6F 67 77 6F 6F 66 00 00 ...(free, 全 0)          │ ogwoof..........
       └─┬─┘ └─┬─┘ └──────┬──────┘ └─┬─┘ └─┬─┘
        type nkeys     sibling=7     off0  off1
        =1   =2       (uint64 LE)    =11   =22
```

逐字段对：
- `01 00` type=1（叶子，LE）
- `02 00` nkeys=2
- `07 00 00 00 00 00 00 00` sibling=7（uint64 LE）
- `0B 00`=11（KV0 终点）、`16 00`=22（KV1 终点）→ KV1 长 = 22-11 = 11 ✓
- KV 区从 byte 16：`03 00`klen=3 `04 00`vlen=4 `63 61 74`="cat" `6D 65 6F 77`="meow"
- 已用 = 12 + 2*2 + 22 = 38 字节；空闲 = 4096 - 38

所有字节都是你 ch02 看 hexdump 时认的那些数。**没有新魔法，只是 4096 字节按规矩摆好。**

---

## 关联

- [[06-search-structures-theory]] — 为什么变长 + offset 数组 + 这套成本模型
- [[03-paging-and-mmap]] — "一个节点 = 一个 page" 的物理基础
- [[00-roadmap]] — Layer 2 (B+Tree)
- [[production-gaps]] — 我们的节点格式 vs InnoDB / BoltDB / LMDB（待补）

---

## 一页速记

1. 节点 = page = 4096B；变长 KV + 定长 offset 数组（间接层救回二分）
2. 头部 12B 统一：type(2) nkeys(2) sibling(8)；per-node 不心疼
3. offset 存**终点** → 顺手得长度 + 已用字节（判分裂）
4. KV：klen(2) vlen(2) key val；叶子 val=数据，内部 val=8B 子页号
5. per-key 字段(offset/klen/vlen)往死里抠 2B，header 随便花
