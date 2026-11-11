# ADR-0007：算子只写 stdout，评论和标签由 runner 写回

- 状态：Accepted
- 日期：2026-04-27

## 背景

算子最初由模型自己调用 `clawflow` 工具去发评论、加标签。这样会因为回合顺序丢答案：最后一回合是纯工具调用时，结果文本为空，答案就没了（提交 `f6dab73b` 提到的 #38）。也容易产生重复评论。

## 决定

提交 `f6dab73b`（2026-04-27）建立 outcome marker 协议，之后成为算子的规范契约。

- 常规算子把回应正文写到 stdout，并在末尾留一行 `<!-- clawflow:outcome=<label> -->`。
- runner 取 Offload 的权威 `result` 文本，解析 marker（出现多次取最后一个），把 marker 行从正文剥掉，发评论，校验 label 在 `operator.outcomes` 白名单内，再加标签，最后释放本地锁。
- 进度事件只含脱敏元数据，不能用来恢复 outcome。
- 常规算子禁止调用任何会写 VCS 状态的工具。`implement` 和 `repair-ci` 因为要操作 Git 和 PR，有各自明确的例外。
- marker 只支持“加一个标签”，不支持移除或交换这类复杂状态机。

## 理由

- 作者只需关心分析内容和文本，不用管 VCS API。
- 评论和标签只由 runner 在一个地方写，不会因回合顺序丢答案，也不会重复评论。
- 白名单限制算子能产生的结束状态。

## 放弃的方案

- 让模型直接调用 `clawflow label/issue comment` 写回：除 `implement` 和 `repair-ci` 外禁止。
- 从事件流里捞历史回合里的 marker 作为后备：#75 提出过这个思路。当前规范要求只用权威 `result` 文本，不从进度恢复 outcome。
- 复杂的标签状态机（移除、交换）：第一版明确不做。

## 相关 issue

- 提交 `f6dab73b` 建立协议
- [#75](https://github.com/zhoushoujianwork/clawflow/issues/75) marker 出现在非最后一回合而被漏掉
- [#326](https://github.com/zhoushoujianwork/clawflow/issues/326) 正文里引用的 marker 占位符被当成真裁决
- 契约全文见 `CLAUDE.md` 的“Outcome marker 协议”
