import DefaultTheme from 'vitepress/theme'
import type { Theme } from 'vitepress'
import './custom.css'

// 魔法纪录服务端文档站主题：在 VitePress 默认主题之上叠加
// 与客户端文档站一致的品牌配色（主色 #D63384，辅色 #9C5BC2）。
const theme: Theme = {
  extends: DefaultTheme,
}

export default theme
