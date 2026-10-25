# Fenno 品牌素材

`fenno.svg` 为 Fenno Portal 现有的蓝色狐狸标志，原样复用到 Fenno Air 的 README。保留图形、配色和 viewBox，不添加独立 Air 图标。

- 来源仓库：`qbox/codeagent`。
- 原始路径：`web/src/assets/fenno.svg`，由 Portal 登录页与侧边栏引用。
- 来源版本：最后变更于 `88d5c0bb`（统一 Fenno Logo 并区分页面与 favicon 展示）。
- SHA-256：`48c43fcd5ba71eb93a482fc1d9db4a9eff9f38cabd5e30d67723b20f1068d5f1`。

资源随本仓库分发，渲染不需要访问相邻的 CodeAgent 工作区，也不依赖在线图片服务。

`fenno-air-wordmark.svg` 和 `fenno-air-wordmark-dark.svg` 是 README 使用的浅色/深色主题字标，保留可读取的 `Fenno Air` 文本和替代文字，以系统字体渲染。README 使用 GitHub 的 `#gh-light-mode-only` / `#gh-dark-mode-only` 主题标记选择对应版本，避免 h1 自带的底边框。两份字标仅文字颜色不同，狐狸图形保持原样。

字标使用 224×40 画布和 32px 字号，收紧画布上下留白；README 将狐狸与字标放在同一段落内换行，避免叠加段落间距。
