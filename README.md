# Q_RP_DSH_plug-in

**简体中文** | [English](#english)

> `Q_RP_dsh` 的插件集合

[![License: CC BY-NC 4.0](https://img.shields.io/badge/License-CC%20BY--NC%204.0-lightgrey.svg)](./LICENSE)

---

## 这是什么

本仓库承载 [Q_RP_dsh](https://github.com/qiyuan2403/Q_RP_dsh) 的扩展面：向宿主**添加**能力与模式。

## 插件

## 插件形态

宿主主路径**零 JS 引擎**。插件因此分两类：

| 类型 | 形态 | 适用 |
|---|---|---|
| **纯 Go 插件** | 编译期注册进宿主 | 卡片处理、Git、画廊 —— 全部核心能力 |
| **外部工具** | 独立进程 / 标准协议（如 MCP） | 生图、联网 —— 语言无关 |

第三方 npm 插件需要 JavaScript 运行时，由宿主的**独立进程侧车**承载，带硬超时、内存上限、环境擦洗与按路径授权。**不在主路径内。**

## 许可证

[CC BY-NC 4.0](./LICENSE)

**明确禁止任何形式的商业化使用。**

---

## English

Plugins and modes for [Q_RP_dsh](https://github.com/qiyuan2403/Q_RP_dsh).

**Seven plugins**: card import, card export, card Git, image generation, gallery, hub sync, web search.

**Three modes**: card authoring, RP-Hub interop, and creative/brainstorming. A mode selects a system-prompt section and a tool allowlist.

**Compatibility is aligned to RP-Hub's own implementation**, not to a general spec: `chara` is read before `ccv3`, keywordless payloads import via the loose fallback, the chunk is written after IHDR, other keyword chunks are preserved, and the export is `{data:{...}}` with an `rp_hub_watermark`.

**Plugin shapes**: the main path holds zero JS. Core capabilities are compiled-in Go plugins; external tools (image generation, web search) run as separate processes behind a standard protocol. Third-party npm plugins need a JavaScript runtime and run in the host's isolated sidecar, never on the main path.

**License**: [CC BY-NC 4.0](./LICENSE) — non-commercial use only.
