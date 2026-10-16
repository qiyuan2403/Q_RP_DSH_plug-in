# Q_RP_DSH_plug-in

**简体中文** | [English](#english)

> `Q_RP_dsh` 的插件集合 —— 角色卡生态的七个子插件与三种模式。

[![License: CC BY-NC 4.0](https://img.shields.io/badge/License-CC%20BY--NC%204.0-lightgrey.svg)](./LICENSE)

---

## 这是什么

本仓库承载 [Q_RP_dsh](https://github.com/qiyuan2403/Q_RP_dsh) 的扩展面：向宿主**添加**能力与模式。

## 插件（七项）

| 插件 | 职责 |
|---|---|
| **导入角色卡** | 读取 PNG 中的卡片载荷，解包到工作区（原件保留于 `.original/`） |
| **导出角色卡** | 从工作区重新打包为可直接导入游玩的 PNG |
| **角色卡 Git** | 版本管理：保存、对比、回滚。得益于解包工作区，diff 是纯文本的 |
| **生图** | 独立插件，不内置内核。经标准接口对接外部绘图后端 |
| **画廊** | 管理差分立绘、表情、`assets/` 内的图片与索引 |
| **主站接入** | 与 RP-Hub 站点互通：按 ID 拉取、成品推送 |
| **联网** | 背景资料查证 |

## 模式（三种）

模式决定**系统提示词分段**与**工具白名单**：

| 模式 | 提示词侧重 | 工具范围 |
|---|---|---|
| **一键制作角色卡** | 卡片结构规范、占位符语法（`{{char}}` / `{{user}}`）、人称一致性 | 读写、解包、打包、联网 |
| **互通模式** | 与 RP-Hub 数据模型对齐、字段映射、格式往返 | 导入导出、字段校验 |
| **创意 / 创作模式** | 设定发散、世界观构建、外貌与立绘提示词反推 | 生图、画廊、世界书生成 |

## 兼容性标准

角色卡格式**逐条对齐** [RP-Hub](https://github.com/STA1N156/RP-Hub) 的实现，而非通用规范。关键差异：

- 读取优先级是 `chara` **优先**、`ccv3` 回退
- 无关键字但仍像 base64 JSON 的载荷走**宽松回退**读取
- 卡片 chunk 写入位置在 **IHDR 之后**
- 其他关键字 chunk **保留**，不删除
- 导出为 `{data:{...}}` + `extensions.rp_hub_watermark`，**不写**顶层 `spec`

## 插件形态

宿主主路径**零 JS 引擎**。插件因此分两类：

| 类型 | 形态 | 适用 |
|---|---|---|
| **纯 Go 插件** | 编译期注册进宿主 | 卡片处理、Git、画廊 —— 全部核心能力 |
| **外部工具** | 独立进程 / 标准协议（如 MCP） | 生图、联网 —— 语言无关 |

第三方 npm 插件需要 JavaScript 运行时，由宿主的**独立进程侧车**承载，带硬超时、内存上限、环境擦洗与按路径授权。**不在主路径内。**

## 许可证

[CC BY-NC 4.0](./LICENSE) —— 与 [RP-Hub](https://github.com/STA1N156/RP-Hub) 相同。

**明确禁止任何形式的商业化使用。**

## 标准

**永远以 [STA1N156/RP-Hub](https://github.com/STA1N156/RP-Hub) 的官方最新版为唯一标准。**

当前钉版：commit [`3e7c706`](https://github.com/STA1N156/RP-Hub/commit/3e7c7064e2c72085d18e729fb2c0260c1894ae2c)（2026-10-15）。

---

## English

Plugins and modes for [Q_RP_dsh](https://github.com/qiyuan2403/Q_RP_dsh).

**Seven plugins**: card import, card export, card Git, image generation, gallery, hub sync, web search.

**Three modes**: card authoring, RP-Hub interop, and creative/brainstorming. A mode selects a system-prompt section and a tool allowlist.

**Compatibility is aligned to RP-Hub's own implementation**, not to a general spec: `chara` is read before `ccv3`, keywordless payloads import via the loose fallback, the chunk is written after IHDR, other keyword chunks are preserved, and the export is `{data:{...}}` with an `rp_hub_watermark`.

**Plugin shapes**: the main path holds zero JS. Core capabilities are compiled-in Go plugins; external tools (image generation, web search) run as separate processes behind a standard protocol. Third-party npm plugins need a JavaScript runtime and run in the host's isolated sidecar, never on the main path.

**License**: [CC BY-NC 4.0](./LICENSE) — non-commercial use only.
