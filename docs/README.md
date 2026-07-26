# 服务端文档站

基于 [VitePress](https://vitepress.dev/) 的项目文档,按受众分四条主线:

- **自托管指南**(`self-host/`)—— 只想部署运行服务器的人
- **架构**(`architecture/`)—— 理解系统如何运作
- **安全机制**(`security/`)—— 每道防线的威胁模型与实现
- **贡献者指南**(`contributing/`)—— 初级到资深,按深度递进

## 本地预览

```bash
cd docs
npm install
npm run docs:dev      # 本地开发服务器(热更新)
```

## 构建

```bash
npm run docs:build    # 产物在 .vitepress/dist/
npm run docs:preview  # 预览构建产物
```

## 结构

```
docs/
  .vitepress/
    config.mts        导航、侧边栏、Mermaid、本地搜索
    theme/            默认主题 + 品牌色覆盖
  index.md            首页
  self-host/          自托管(11 页)
  architecture/       架构(6 页)
  security/           安全机制(8 页)
  contributing/       贡献者指南(11 页)
```

## 写作约定

- 用 Mermaid 画架构/流程/时序图(已装 `vitepress-plugin-mermaid`)。
- 自定义容器:`::: tip` / `::: warning` / `::: danger`。
- 跨页引用用相对链接,代码引用尽量带文件名。
- 内容与源码保持同步 —— 改了对应子系统记得更新文档。
