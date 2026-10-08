# 缓存替换实验：LRU、Clock 与扫描负载

状态：未教学

## 主要内容大纲

- 实验输入：固定缓存容量、页访问序列与观察指标。
- LRU 与 Clock 的小型实现与状态观察。
- pin/unpin 与可淘汰条件。
- 热点访问、循环访问、大范围扫描与缓存污染。
- 命中、加载、淘汰与错误路径的断言。
- 运行命令、可重复的数据集与资源清理。
- 实验结果与 OS 页缓存的差异，结论的适用范围。

实验代码计划放在 `storage/experiments/`。前置：[页缓存与 buffer pool](07-page-cache-versus-buffer-pool.md)。返回 [本组目录](README.md)。
