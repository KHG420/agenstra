---
name: Agenstra 开发者控制台
description: 独立 /admin 管理界面的清爽配置台视觉系统
colors:
  ink: "#18313b"
  muted: "#51636a"
  ground: "#f4f6f3"
  paper: "#fff"
  line: "#d8e0dc"
  accent: "#116b64"
  accent-dark: "#09524d"
  danger: "#a5352c"
  sidebar: "#eef3f0"
  sidebar-active: "#dcece3"
typography:
  body:
    fontFamily: "-apple-system, BlinkMacSystemFont, Segoe UI, Noto Sans SC, sans-serif"
    fontSize: "14px"
    lineHeight: 1.5
  heading:
    fontFamily: "-apple-system, BlinkMacSystemFont, Segoe UI, Noto Sans SC, sans-serif"
    fontSize: "1.7rem"
    lineHeight: 1.3
  label:
    fontFamily: "-apple-system, BlinkMacSystemFont, Segoe UI, Noto Sans SC, sans-serif"
    fontSize: ".82rem"
    fontWeight: 700
rounded:
  control: "5px"
  container: "6px"
  feedback: "8px"
spacing:
  compact: "8px"
  field: "16px"
  section: "28px"
components:
  button-primary:
    backgroundColor: "{colors.accent}"
    textColor: "{colors.paper}"
    rounded: "{rounded.control}"
    padding: "7px 13px"
  button-secondary:
    backgroundColor: "{colors.paper}"
    rounded: "{rounded.control}"
    padding: "7px 13px"
  input:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.ink}"
    rounded: "{rounded.control}"
    padding: "9px 11px"
  sidebar-active:
    backgroundColor: "{colors.sidebar-active}"
    textColor: "{colors.accent-dark}"
    rounded: "{rounded.control}"
    padding: "12px 14px"
---

# Design System: Agenstra 开发者控制台

## Overview

**Creative North Star: “清爽配置台”**

这个系统只描述框架直接提供的独立 `/admin` 开发者控制台。它服务于配置模型、能力包、连接与授权的开发者；不延伸到无 UI Web SDK、可选聊天组件或宿主应用界面。宿主可以保留自己的视觉设计。

界面以白色工作区、白色顶栏、浅绿侧栏和克制的青绿色状态为骨架。配置表单保持平整，标题、说明、字段和当前状态按操作顺序排列。一次只展示一个管理模块；模型配置里的“模型服务”和“用途分配”再用两个页签分开。浅色分隔线与留白承担层次，视觉重点留给当前导航、选中页签和主要保存动作。

**Key Characteristics:**

- 六个管理模块组成固定侧栏：版本目录、模型配置、能力草稿、连接与授权、试运行、变更记录。
- 独立的开发者管理密钥入口和连接状态留在顶栏；连接表单可展开。
- 表单使用紧凑原生控件，较复杂的参数通过原生 `details` 按需展开。
- 模型和草稿编辑区在底部保留保存动作及状态反馈。

## Colors

青绿色只承担主要动作和当前状态；其余信息以深色文字、灰绿说明和浅色分隔线呈现。上方 frontmatter 的颜色值是规范来源。

### Primary

- **配置青绿**（`accent`）：主要按钮、选中页签下划线、步骤指示和输入光标。
- **深青绿**（`accent-dark`）：当前侧栏文字、页签文字和次要交互强调。

### Neutral

- **深墨色**（`ink`）：正文、标题和字段内容。
- **灰绿**（`muted`）：辅助说明与未选中的导航。
- **白纸**（`paper`）：顶栏、内容区和表单底面。
- **浅底色**（`ground`）：页面背景；**浅绿侧栏**（`sidebar`）划定模块导航区域。
- **细分隔线**（`line`）：区域、表格和粘性保存栏的边界。
- **浅绿选中面**（`sidebar-active`）：当前模块的侧栏背景。
- **危险红**（`danger`）：移除、停用等危险动作的文字。

**The Active State Rule.** 侧栏选中项与当前页签同时使用青绿文字或浅绿背景，并保留清楚的当前位置指示。

## Typography

**Display Font:** 系统无衬线栈，中文回退到 Noto Sans SC。
**Body Font:** 同一字体栈；技术标识、哈希和 JSON 使用系统等宽字体。

字体层级服务于操作定位：模块标题明显高于正文；标签较小但加粗；说明和反馈采用可读的灰绿语气。`h2` 是可见模块主标题，页面 `h1` 为屏幕阅读器保留。

## Layout

桌面采用固定顶栏与两列外壳：顶栏最低 64px，侧栏宽 200px，主内容宽度上限 1180px。主区以 38px 左右内边距放置单个活动模块；表单中相关字段采用两列网格，间距 16px。数据表保留横向滚动，不压缩列内容。

在 1100px 以下，侧栏缩至 168px，主区和双列辅助布局收紧。760px 以下，侧栏变成可横向滚动的模块导航，字段网格转单列，顶栏按需要换行。这是现有样式中的响应式行为；初始验收场景是桌面浏览器。

**The One Module Rule.** 主工作区同一时间只呈现一个一级管理模块；二级模型页签和草稿步骤也只呈现其当前内容。顶栏管理密钥区独立于模块导航。

## Elevation & Depth

常态布局不使用投影。白色工作区、浅绿侧栏、淡色反馈面和细边界形成层次；粘性顶栏与粘性保存栏通过位置和分隔线保持可见。

## Shapes

控件采用轻微圆角（`rounded.control`），表格容器略宽（`rounded.container`），反馈和选中列表项采用更柔和的圆角（`rounded.feedback`）。输入框、表格与折叠设置保持薄边线；大块表单不套装饰性卡片，以免增加配置流程的视觉噪声。

## Components

### Buttons

主要按钮为实心青绿底与白字，用于连接、保存、发布和创建任务；悬停时转深青绿。次要按钮是白底细边，用于刷新、检查和非主要动作。危险按钮用红字与浅红边。所有按钮保持紧凑高度，键盘焦点有清晰外轮廓，禁用状态降低不透明度。

### Inputs and disclosure

文本框、数字框、选择器和文本域保留浏览器原生行为，统一浅边线、白底、深色文字和紧凑内边距。标签位于字段上方，帮助文字紧随字段。高级参数使用可展开的原生 `details`，避免常用字段被长配置淹没。

### Navigation and tabs

侧栏以浅绿底承载六个一级模块；当前项有独立浅绿背景，链接文字变为深青绿。模型服务与用途分配使用一条细基线和青绿下划线标示选中项；页签支持键盘左右箭头与 Home/End 切换。草稿步骤沿用下划线式当前状态。

### Save state and feedback

模型配置和能力草稿的保存区贴近可视区域底部，用上边线与表单区分，同时展示待保存或已保存状态。反馈信息使用淡色底面；成功为浅绿，错误为浅红。保存动作与状态文字要一起出现，使操作结果可见。

### Data containers

版本目录以带细边框的表格承载，长表格允许横向滚动；连接列表与变更记录使用平整的行式信息。技术 ID 与内容哈希采用等宽字体，状态用文字与颜色共同表达。

## Do's and Don'ts

### Do:

- **Do** 让页面保持一个清楚的活动模块，使用模块标题、简短说明、字段和保存状态建立阅读顺序。
- **Do** 对主要提交使用配置青绿，对危险动作使用现有危险红，并在结果区写出明确状态。
- **Do** 把高级参数收进现有折叠区，保留原生表单标签、焦点指示与键盘页签操作。

### Don't:

- **Don't** 把开发者控制台的视觉规则套用到无 UI SDK、可选聊天组件或宿主业务页面。
- **Don't** 用浮层、重阴影或装饰性卡片替代现有平整表单与细线分隔。
- **Don't** 让保存结果只靠颜色表达，或让保存按钮离开其状态说明。
