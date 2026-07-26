---
layout: home

hero:
  name: 魔法纪录复兴计划
  text: 服务端文档
  tagline: 魔法纪录复刻计划的 Go 服务端 —— 架构、安全机制与贡献者指南
  actions:
    - theme: brand
      text: 我只想把它跑起来
      link: /self-host/
    - theme: alt
      text: 理解架构
      link: /architecture/
    - theme: alt
      text: 参与开发
      link: /contributing/

features:
  - icon: 🗄️
    title: 一套代码,三种数据库
    details: PostgreSQL / MySQL / SQLite 通过方言抽象统一支持;迁移内嵌、启动自动执行、幂等可重复运行。
  - icon: 🛡️
    title: 纵深防御
    details: 版本化 scrypt 口令哈希、滑动会话、按 IP/会话限流、受信任代理、PoW 人机验证、客户端完整性闸门。
  - icon: 🌐
    title: 面板 + 多节点
    details: 业务节点持有数据库与全部 API,边缘节点就近分发资源;面板经 WebSocket 管控连接监控各节点,客户端凭签名目录安全发现节点。
  - icon: 📦
    title: 按需流式资产
    details: 场景包是清单与调度单位、文件是传输与缓存单位;客户端与本地缓存做差集,只拉缺失的文件。
---

<div class="audience-grid">
  <a class="audience-card" href="/server/self-host/">
    <span class="tag">自托管者</span>
    <h3>我只想跑一个自己的服务器 →</h3>
    <p>不关心源码,只想把节点(业务/边缘)与面板部署起来、配好数据库与域名、登录管理后台。从「快速部署」开始,照抄即可。</p>
  </a>
  <a class="audience-card" href="/server/contributing/">
    <span class="tag">初级贡献者</span>
    <h3>我想读懂代码并提交改动 →</h3>
    <p>第一次接触这个仓库?这里有开发环境搭建、代码库导览、如何跑测试,以及一个「从零新增一个接口」的完整动手示例。</p>
  </a>
  <a class="audience-card" href="/server/contributing/store-dialects">
    <span class="tag">资深贡献者</span>
    <h3>我要改动核心子系统 →</h3>
    <p>多方言存储抽象、客户端协议保真的硬约束、调度器、打包器、发布流程 —— 改这些之前先读懂背后的设计权衡。</p>
  </a>
</div>

## 这套文档怎么读

文档按**读者想做的事**而非按模块划分,四条主线相互独立,各取所需:

| 你是… | 从哪里开始 | 会得到什么 |
|---|---|---|
| **自托管者** | [自托管指南](/self-host/) | 部署、配置、运维一条龙,不需要读源码 |
| **想理解系统的人** | [架构总览](/architecture/) | 系统如何运作、请求怎么流转、节点如何协作 |
| **关注安全的人** | [安全机制](/security/) | 每一道防线的威胁模型、实现与配置要点 |
| **贡献者** | [贡献者指南](/contributing/) | 从搭环境到改核心子系统,按深度递进 |

::: tip 一句话理解这个项目
**客户端**是一个改造过的安卓 APK;**服务端**(本仓库)告诉客户端:你这个版本能不能玩、资源去哪下、是否被封禁、要不要更新,并为玩家提供账号与云存档。绝大多数"防作弊/防改包"的判断都发生在服务端,因为客户端在玩家手里、不可信。
:::

::: warning 使用边界
本项目用于**授权范围内**的私服复兴与学习研究。请勿用于侵犯他人权益或违反当地法律法规的用途。
:::
