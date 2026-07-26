import { defineConfig } from 'vitepress'
import { withMermaid } from 'vitepress-plugin-mermaid'

// 文档站配置。按"受众"组织侧边栏:
//   /self-host/      —— 只想把服务器跑起来的人
//   /architecture/   —— 想理解系统如何运作(初级贡献者 / 进阶运维)
//   /security/       —— 安全机制深挖
//   /contributing/   —— 初级 + 资深贡献者
export default withMermaid(
  defineConfig({
    lang: 'zh-CN',
    title: '魔法纪录复兴计划 · 服务端',
    description:
      '为「魔法纪录 CNV 客户端」配套的 Go 服务端 —— 架构、安全机制与贡献者指南。',
    base: '/server/',
    lastUpdated: true,
    cleanUrls: true,
    ignoreDeadLinks: true,

    head: [
      ['meta', { name: 'theme-color', content: '#D63384' }],
      ['meta', { property: 'og:type', content: 'website' }],
      ['meta', { property: 'og:title', content: '魔法纪录 CNV 服务端文档' }],
      ['meta', { property: 'og:description', content: '自托管指南 · 系统架构 · 贡献者手册' }],
    ],

    themeConfig: {
      outline: { level: [2, 3], label: '本页目录' },

      editLink: {
        pattern:
          'https://github.com/MagirecoCN-Revival-Project/magireco-cnv-server/edit/main/docs/:path',
        text: '在 GitHub 上编辑此页',
      },

      nav: [
        { text: '自托管', link: '/self-host/', activeMatch: '/self-host/' },
        { text: '架构', link: '/architecture/', activeMatch: '/architecture/' },
        { text: '安全机制', link: '/security/', activeMatch: '/security/' },
        {
          text: '贡献者指南',
          link: '/contributing/',
          activeMatch: '/contributing/',
        },
        {
          text: '速查',
          items: [
            { text: '环境变量参考', link: '/self-host/configuration' },
            { text: '客户端协议', link: '/architecture/client-protocol' },
            { text: '数据模型', link: '/architecture/data-model' },
            { text: '安全加固清单', link: '/self-host/security-checklist' },
          ],
        },
      ],

      sidebar: {
        '/self-host/': [
          {
            text: '自托管指南',
            collapsed: false,
            items: [
              { text: '概述:这是什么', link: '/self-host/' },
              { text: '部署前准备', link: '/self-host/prerequisites' },
              { text: '快速部署', link: '/self-host/quick-start' },
              { text: '选择数据库', link: '/self-host/database' },
              { text: '环境变量参考', link: '/self-host/configuration' },
              { text: '节点与面板', link: '/self-host/nodes' },
              { text: '反向代理与域名', link: '/self-host/reverse-proxy' },
              { text: '管理后台使用', link: '/self-host/admin-panel' },
              { text: '安全加固清单', link: '/self-host/security-checklist' },
              { text: '日常运维', link: '/self-host/operations' },
              { text: '常见问题', link: '/self-host/faq' },
            ],
          },
        ],
        '/architecture/': [
          {
            text: '架构',
            collapsed: false,
            items: [
              { text: '系统总览', link: '/architecture/' },
              { text: '请求生命周期', link: '/architecture/request-lifecycle' },
              { text: '客户端握手协议', link: '/architecture/client-protocol' },
              { text: '多节点协调', link: '/architecture/multi-node' },
              { text: '三套会话体系', link: '/architecture/sessions' },
              { text: '数据模型', link: '/architecture/data-model' },
            ],
          },
        ],
        '/security/': [
          {
            text: '安全机制',
            collapsed: false,
            items: [
              { text: '威胁模型与总览', link: '/security/' },
              { text: '防改包闸门', link: '/security/anti-tamper' },
              { text: '口令哈希(scrypt)', link: '/security/password-hashing' },
              { text: '会话与令牌', link: '/security/sessions-tokens' },
              { text: '限流与防爆破', link: '/security/rate-limiting' },
              { text: '受信任代理与来源 IP', link: '/security/trust-proxy' },
              { text: 'PoW 人机验证', link: '/security/captcha-pow' },
              { text: '协议版本协商', link: '/security/version-gates' },
            ],
          },
        ],
        '/contributing/': [
          {
            text: '入门(初级贡献者)',
            collapsed: false,
            items: [
              { text: '从这里开始', link: '/contributing/' },
              { text: '开发环境搭建', link: '/contributing/dev-setup' },
              { text: '代码库导览', link: '/contributing/codebase-tour' },
              { text: '运行与编写测试', link: '/contributing/testing' },
              { text: '动手:新增一个接口', link: '/contributing/adding-endpoint' },
              { text: '代码规范', link: '/contributing/conventions' },
            ],
          },
          {
            text: '深入(资深贡献者)',
            collapsed: false,
            items: [
              { text: '存储层与多方言抽象', link: '/contributing/store-dialects' },
              { text: '协议保真原则', link: '/contributing/protocol-fidelity' },
              { text: '契约纪律与文档绑定', link: '/contributing/discipline' },
              { text: '调度器与后台任务', link: '/contributing/scheduler' },
              { text: '离线整包打包器', link: '/contributing/packer' },
              { text: '发布流程与 CI', link: '/contributing/release-ci' },
            ],
          },
        ],
      },

      socialLinks: [
        {
          icon: 'github',
          link: 'https://github.com/magirecocn-revival-project/magireco-cnv-server',
        },
      ],

      search: {
        provider: 'local',
        options: {
          translations: {
            button: { buttonText: '搜索文档', buttonAriaLabel: '搜索文档' },
            modal: {
              noResultsText: '无法找到相关结果',
              resetButtonTitle: '清除查询条件',
              footer: {
                selectText: '选择',
                navigateText: '切换',
                closeText: '关闭',
              },
            },
          },
        },
      },

      docFooter: { prev: '上一页', next: '下一页' },
      darkModeSwitchLabel: '主题',
      lightModeSwitchTitle: '切换到浅色模式',
      darkModeSwitchTitle: '切换到深色模式',
      sidebarMenuLabel: '菜单',
      returnToTopLabel: '回到顶部',
      lastUpdated: {
        text: '最后更新于',
        formatOptions: { dateStyle: 'short', timeStyle: 'short' },
      },

      footer: {
        message: '本项目仅作学习研究使用 · 与版权方无任何关联',
        copyright: '© 魔法纪录国服复刻计划 (MagirecoCN-Revival-Project)',
      },
    },

    mermaid: {
      theme: 'default',
    },
  }),
)
