// The component gallery: every piece of UI/Kit with the mockup's example
// text, a page per group, for looking at them in light and dark and at
// large text sizes, and for screenshots to compare with
// docs/design/client-v38.html (as Android's V7Gallery and V8Gallery do
// under androidTest). Debug builds only.
//
//   -gallery            opens the list of pages
//   -gallery <page>     opens one page (a GalleryPage's raw value)
//   -galleryEnd         starts a page scrolled to its end
//
// Signal (see App/Launch.swift): gallery.save presses 保存 of the dialog on
// the page `saving`, whose save always fails after a moment.
#if DEBUG
import RACore
import SwiftUI

enum GalleryPage: String, CaseIterable, Identifiable {
    case tokens, icons, buttons, notices, lists, choices, fields, states, nav, sheet, dialog, input, saving, menu, cover

    var id: String { rawValue }

    var title: String {
        switch self {
        case .tokens: "颜色与字号"
        case .icons: "图标与标记"
        case .buttons: "按钮"
        case .notices: "提示条"
        case .lists: "分组列表"
        case .choices: "选项与切换"
        case .fields: "输入"
        case .states: "空、读取中、失败"
        case .nav: "导航栏"
        case .sheet: "底部弹层"
        case .dialog: "确认对话框"
        case .input: "输入对话框"
        case .saving: "输入对话框 · 保存失败"
        case .menu: "更多菜单"
        case .cover: "隐私遮罩"
        }
    }
}

enum Gallery {
    enum Request: Equatable {
        case index
        case page(GalleryPage)
    }

    /// What the launch arguments ask for, nil when the app starts as usual.
    static let requested: Request? = {
        let args = ProcessInfo.processInfo.arguments
        guard let i = args.firstIndex(of: "-gallery") else { return nil }
        if i + 1 < args.count, let page = GalleryPage(rawValue: args[i + 1]) { return .page(page) }
        return .index
    }()

    static let startsAtEnd = ProcessInfo.processInfo.arguments.contains("-galleryEnd")
}

struct GalleryRoot: View {
    let page: Gallery.Request

    var body: some View {
        NavigationStack {
            switch page {
            case .index:
                GalleryIndex()
            case .page(let page):
                GalleryPageView(page: page)
            }
        }
        .tint(Theme.blue)
    }
}

private struct GalleryIndex: View {
    var body: some View {
        Page(gray: true) {
            ScrollView {
                LargeTitle("组件")
                GroupCard {
                    for page in GalleryPage.allCases {
                        NavigationLink(value: page) {
                            GRow(page.title, value: page.rawValue, chevron: true)
                        }
                        .buttonStyle(RowPressStyle())
                    }
                }
            }
        }
        .navigationDestination(for: GalleryPage.self) { GalleryPageView(page: $0) }
        .toolbar(.hidden, for: .navigationBar)
    }
}

struct GalleryPageView: View {
    let page: GalleryPage

    var body: some View {
        switch page {
        case .tokens: scroll { TokensPage() }
        case .icons: scroll { IconsPage() }
        case .buttons: scroll { ButtonsPage() }
        case .notices: scroll { NoticesPage() }
        case .lists: scroll(gray: true) { ListsPage() }
        case .choices: scroll { ChoicesPage() }
        case .fields: scroll { FieldsPage() }
        case .states: scroll { StatesPage() }
        case .nav: NavPage()
        case .sheet: SheetPage()
        case .dialog: DialogPage()
        case .input: InputPage()
        case .saving: SavingPage()
        case .menu: MenuPage()
        case .cover: PrivacyCover().toolbar(.hidden, for: .navigationBar)
        }
    }

    private func scroll<Content: View>(gray: Bool = false, @ViewBuilder content: () -> Content) -> some View {
        Page(gray: gray) {
            ScrollView {
                VStack(alignment: .leading, spacing: 0) { content() }
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.bottom, 24)
            }
            .defaultScrollAnchor(Gallery.startsAtEnd ? .bottom : .top)
        }
        .navBar(page.title)
    }
}

// MARK: Pages

private struct TokensPage: View {
    private let colors: [(String, Color)] = [
        ("bg", Theme.bg), ("card", Theme.card), ("soft", Theme.soft), ("chip", Theme.chip), ("seg", Theme.seg),
        ("track", Theme.track), ("line", Theme.line), ("fg", Theme.fg), ("fg2", Theme.fg2), ("muted", Theme.muted),
        ("faint", Theme.faint), ("blue", Theme.blue), ("blue-bg", Theme.blueBg), ("blue-soft", Theme.blueSoft),
        ("orange", Theme.orange), ("orange-ink", Theme.orangeInk), ("orange-soft", Theme.orangeSoft),
        ("green", Theme.green), ("green-ink", Theme.greenInk), ("red", Theme.red), ("red-bg", Theme.redBg),
        ("red-soft", Theme.redSoft), ("gray", Theme.gray),
    ]
    private let styles: [(String, TypeStyle)] = [
        ("30 大标题", T.title30), ("22 范围标题", T.h22), ("17 弹层标题", T.h17), ("15 正文", T.body),
        ("15 按钮", T.bodyB), ("13 第二行", T.sm), ("13 分组标题", T.smB), ("12 元信息", T.meta),
        ("11 标签栏", T.tab), ("12 mono ~/Code/foo", T.mono), ("13 mono go test ./...", T.mono13),
    ]

    var body: some View {
        SectionLabel("颜色", count: colors.count)
        LazyVGrid(columns: Array(repeating: GridItem(.flexible(), spacing: 8), count: 4), spacing: 8) {
            ForEach(colors, id: \.0) { name, color in
                VStack(spacing: 3) {
                    RoundedRectangle(cornerRadius: Theme.Radius.xs, style: .continuous).fill(color)
                        .frame(height: 30)
                        .overlay(RoundedRectangle(cornerRadius: Theme.Radius.xs, style: .continuous).strokeBorder(Theme.line, lineWidth: 1))
                    Text(name).type(T.tab, Theme.muted).lineLimit(1).minimumScaleFactor(0.7)
                }
            }
        }
        .padding(.horizontal, 16)
        SectionLabel("字号")
        VStack(alignment: .leading, spacing: 4) {
            ForEach(styles, id: \.0) { name, style in Text(name).type(style, Theme.fg) }
        }
        .padding(.horizontal, 16)
        SectionLabel("圆角")
        HStack(spacing: 10) {
            ForEach([("4", Theme.Radius.code), ("6", Theme.Radius.xs), ("10", Theme.Radius.sm), ("14", Theme.Radius.md), ("20", Theme.Radius.lg)], id: \.0) { name, r in
                Text(name).type(T.meta, Theme.fg2).frame(width: 52, height: 44)
                    .background(Theme.chip, in: RoundedRectangle(cornerRadius: r, style: .continuous))
            }
        }
        .padding(.horizontal, 16)
    }
}

private struct IconsPage: View {
    var body: some View {
        SectionLabel("Lucide", count: Lucide.all.count)
        LazyVGrid(columns: Array(repeating: GridItem(.flexible(), spacing: 4), count: 4), spacing: 10) {
            ForEach(Lucide.all.indices, id: \.self) { i in
                let icon = Lucide.all[i]
                VStack(spacing: 4) {
                    Ic(icon, size: 22, color: Theme.fg)
                    Text((icon.filled ? "■ " : "") + icon.name).type(T.tab, Theme.muted).lineLimit(1).minimumScaleFactor(0.6)
                }
            }
        }
        .padding(.horizontal, 12)
        SectionLabel("状态")
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 14) {
                StatusLabel("运行中", tone: .run)
                StatusLabel("等待审批", tone: .wait)
                StatusLabel("空闲", tone: .idle)
                StatusLabel("已结束", tone: .off)
            }
            HStack(spacing: 14) {
                IconText(Lucide.loaderCircle, "正在连接…")
                IconText(Lucide.check, "完成", color: Theme.greenInk)
                IconText(Lucide.x, "失败", color: Theme.red)
                IconText(Lucide.minus, "已取消")
            }
            HStack(spacing: 10) {
                ArrowText("管理")
                ArrowText("处理", direction: .down)
                ArrowText("收起", direction: .up)
            }
        }
        .padding(.horizontal, 16)
        SectionLabel("头像与标签")
        HStack(spacing: 10) {
            Avatar("/Users/me/Code/foo", size: 52)
            Avatar("/Users/me/Code/remote-agent")
            Avatar("/Users/me/Code/keduly")
            Avatar("/Users/me/notes")
            Avatar.all()
            Avatar("/Users/me/Code/foo", size: 28)
            Avatar.all(size: 28)
            Avatar("/Users/me/Code/foo", size: 22)
            Avatar("/Users/me/Code/foo", size: 16)
            IconBadge(Lucide.laptop, color: Theme.blueBg)
        }
        .padding(.horizontal, 16)
        HStack(spacing: 5) {
            Chip("Codex")
            Chip("上下文 86%", tone: .warn)
            Chip("自动", tone: .auto)
            Chip("完全放行", tone: .danger)
            Chip("可点") {}
        }
        .padding(.horizontal, 16).padding(.top, 8)
        HStack(spacing: 16) {
            AppMark.view(size: 32, color: Theme.blueBg)
            AppMark.view(size: 20, color: Theme.muted)
        }
        .padding(.horizontal, 16).padding(.top, 8)
    }
}

private struct ButtonsPage: View {
    var body: some View {
        SectionLabel("主按钮与次按钮")
        VStack(spacing: 10) {
            Btn("加入", kind: .primary) {}
            Btn("再试一次") {}
            Btn("加入", kind: .primary, enabled: false) {}
            Btns {
                Btn("不允许", kind: .negative) {}
                Btn("允许", kind: .primary) {}
            }
            Btns {
                Btn("取消") {}
                Btn("移除", kind: .danger) {}
            }
            Btns {
                Btn("拒绝", kind: .negative) {}
                Btn("图案一致，允许", kind: .primary) {}
            }
        }
        .padding(.horizontal, 16)
        SectionLabel("灰底卡片里")
        VStack(spacing: 8) {
            Btns {
                Btn("去终端", background: Theme.card) {}
                Btn("恢复会话", kind: .primary) {}
            }
        }
        .padding(12)
        .background(Theme.soft, in: RoundedRectangle(cornerRadius: Theme.Radius.md, style: .continuous))
        .padding(.horizontal, 12)
        SectionLabel("文字操作")
        HStack(spacing: 20) {
            TextAction("重试") {}
            TextAction("中断", color: Theme.red) {}
            TextAction("撤销", enabled: false) {}
            Spacer(minLength: 0)
            NewSessionButton {}
            IconButton(Lucide.ellipsis, label: "更多") {}
        }
        .padding(.horizontal, 16)
        TextAction("全部模型", style: T.body, arrow: true) {}.padding(.horizontal, 16)
    }
}

private struct NoticesPage: View {
    private func state(_ link: RACore.Link, loaded: Bool = true, synced: Bool = true, error: String = "") -> RemoteState {
        var s = RemoteState()
        s.link = link
        s.loaded = loaded
        s.synced = synced
        s.error = error
        return s
    }

    var body: some View {
        SectionLabel("连接提示条")
        VStack(spacing: 6) {
            ConnBanner(state(.noNetwork))
            ConnBanner(state(.noNetwork, loaded: false))
            ConnBanner(state(.waiting))
            ConnBanner(state(.waiting, synced: false))
            ConnBanner(state(.noServer)) {}
            ConnBanner(state(.noServer, loaded: false, error: "服务器没有回应")) {}
        }
        SectionLabel("提示条")
        VStack(spacing: 8) {
            Banner("已停止等待这次配对")
            Banner("通知没有开启，等你处理时收不到提醒", tone: .warn, action: "开启") {}
            Banner("没能读取模型列表：MacBook 不在线", tone: .error, action: "重试") {}
            Banner("这个项目已从列表移除", action: "恢复") {}
            Banner("正在保存…", icon: Lucide.loaderCircle)
        }
        .padding(.horizontal, 16)
        SectionLabel("代码")
        VStack(spacing: 8) {
            Code("rm -rf node_modules && npm ci\ngo test ./... -run TestRelay")
            Code("/Users/me/Code/remote-agent/internal/relay/server_test.go", maxLines: 1, border: true)
        }
        .padding(.horizontal, 16)
    }
}

private struct ListsPage: View {
    @State private var attention = true
    @State private var content = false

    var body: some View {
        LargeTitle("设置")
        GroupLabel("电脑", action: "管理") {}
        GroupCard {
            GRow("MacBook", sub: "Codex · 5 小时已用 62%", value: "在线", chevron: true, onTap: {})
                .lead { IconBadge(Lucide.laptop, color: Theme.blueBg) }
                .valueLead { Dot(Theme.green) }
            GRow("VPS-dev", value: "2 小时前在线", chevron: true, onTap: {})
                .lead { IconBadge(Lucide.laptop, color: Theme.gray) }
            GRow("添加设备", action: true, icon: Lucide.plus, onTap: {})
        }
        GroupLabel("通知")
        GroupCard {
            ToggleRow("需要你处理时", isOn: $attention)
            ToggleRow("在通知里显示内容并直接审批", sub: "手机解锁时才显示会话标题、命令和按钮", isOn: $content)
        }
        GroupLabel("本机")
        GroupCard {
            GRow("外观", value: "自动", chevron: true, onTap: {})
            GRow("图片缓存", value: "24 / 150 MiB")
            GRow("清理查看缓存", action: true, onTap: {})
            GRow("版本", value: AppInfo.versionLine)
            GRow("重新读取", action: true, enabled: false)
        }
        GroupCard {
            GRow("退出账户", danger: true, onTap: {})
        }
        .contextMenu {
            Button(role: .destructive) {} label: { Label { Text("退出账户") } icon: { Lucide.x.image() } }
        }
        // Rows handed over together: a `ForEach` and an array each give a row per element, with the lines between.
        GroupLabel("成组给出的行")
        GroupCard {
            ForEach(["Codex", "Claude Code"], id: \.self) { GRow($0, value: "已安装") }
            ["~/Code/foo", "~/Code/remote-agent"].map { GRow($0, chevron: true, onTap: {}) }
        }
        // What the mockup's short examples never show: the text has the room first, the value at least two fifths.
        GroupLabel("长文字：一个很长的分组标题，看右边的操作会不会被挤成一列", action: "全部设备") {}
        GroupCard {
            GRow("ip-10-0-12-231.ap-southeast-1.compute.internal", value: "在线", chevron: true, onTap: {})
                .lead { IconBadge(Lucide.laptop, color: Theme.blueBg) }
            GRow("一个名字特别长的客户端，用来看一行放不下的时候怎么办", sub: "App · iOS 26.0 · iPhone", value: "当前")
            GRow("我的手机", sub: "App · Android 16 · Pixel 9", value: "40 分钟前使用", chevron: true, onTap: {})
            GRow("服务器", value: "relay.a-very-long-subdomain.example-company.internal:8443")
            GRow("alex-macbook-pro-16-inch-2026-office", value: "10月8日 周四 14:10 使用", chevron: true, onTap: {})
            GRow("没有值的长标题，后面只有一个箭头，看它在哪里换行、在哪里省略", chevron: true, onTap: {})
            GRow("清理这台手机上所有已经不再需要的查看缓存和预览", action: true, onTap: {})
        }
    }
}

private struct ChoicesPage: View {
    @State private var tab = 0
    @State private var darkTab = 1
    @State private var seg = 0
    @State private var effort: String? = "medium"
    @State private var route = "home"

    var body: some View {
        SectionLabel("视图切换")
        ViewTabs(["聊天", "终端"], selected: $tab, dot: 1)
        ViewTabs(["聊天", "终端", "并排"], selected: $tab, enabled: { $0 < 2 })
        ViewTabs(["聊天", "终端"], selected: $darkTab, dark: true)
            .padding(.top, 4).background(Theme.Term.bg)
        SectionLabel("分段按钮")
        ViewThatFits(in: .horizontal) {
            HStack {
                Text("外观").type(T.body, Theme.fg)
                Spacer()
                SegPick(["自动", "亮色", "暗色"], selected: $seg)
            }
            VStack(alignment: .leading, spacing: 4) {
                Text("外观").type(T.body, Theme.fg)
                SegPick(["自动", "亮色", "暗色"], selected: $seg)
            }
        }
        .padding(.horizontal, 16)
        SectionLabel("推理强度")
        ChoiceGrid(["minimal", "low", "medium", "high", "xhigh"], selected: effort) { effort = $0 }
            .padding(.horizontal, 16)
        SectionLabel("上下文")
        VStack(spacing: 8) {
            ContextBox(percent: 32, detail: "上下文 243k / 760k", action: "压缩") {}
            ContextBox(percent: 86, detail: "上下文 654k / 760k", action: "正在压缩…", enabled: false) {}
        }
        .padding(.horizontal, 16)
        SectionLabel("选项")
        VStack(spacing: 0) {
            ListItem("询问", desc: "需要审批时由你确认，超时拒绝", checked: true, onTap: {})
            ListItem("自动", desc: "由 Codex 自己判断，拿不准的在对话里问你", onTap: {})
            ListItem("完全放行", desc: "本会话启动时未启用；需要时新建会话选择", enabled: false)
            ListItem("gpt-6-astra", small: "最高 xhigh", icon: Lucide.check, onTap: {})
            ListItem("claude-opus-5-5-preview-20261001-extended", checked: true, small: "1M 上下文 · 无推理强度", last: true, onTap: {})
        }
        .padding(.horizontal, 16)
        SectionLabel("标签栏")
        TabBar(
            [TabBarItem(id: "home", label: "会话", icon: Lucide.messageSquare, badge: 2),
             TabBarItem(id: "settings", label: "设置", icon: Lucide.settings)],
            selected: $route)
    }
}

private struct FieldsPage: View {
    @State private var code = "4829 1756"
    @State private var empty = ""
    @State private var server = "https://ra.example.com"
    @State private var secret = ""
    @State private var reason = "先别动 main 分支，在 feature 分支上改"
    @State private var filter = ""
    @State private var filled = "relay"
    @State private var message = ""
    @State private var long = "把 relay 的重连逻辑改成指数退避，上限 30 秒；\n顺便补上断线期间事件不丢的集成测试"
    @FocusState private var typing: Bool
    @FocusState private var typingLong: Bool

    var body: some View {
        LargeTitle("加入账户")
        Text("在已加入的设备上打开「设置 › 添加设备」，扫二维码，或把 8 位配对码填在这里。")
            .type(T.sm, Theme.muted).padding(.horizontal, 16).padding(.bottom, 14)
        VStack(spacing: 10) {
            InputField(text: $code, placeholder: "输入 8 位配对码", look: .code, keyboard: .numberPad)
            InputField(text: $empty, placeholder: "输入 8 位配对码", look: .code, keyboard: .numberPad)
            InputField("服务器", text: $server, placeholder: "https://", keyboard: .URL)
            InputField("账户密钥", text: $secret, placeholder: "ra1.…", look: .mono, lines: 3...3)
            InputField(text: $reason, placeholder: "告诉它为什么不允许（可不填）", lines: 1...4)
        }
        .padding(.horizontal, 16)
        // A field on a grey card takes the surface above it.
        VStack(spacing: 8) {
            InputField(text: $reason, placeholder: "拒绝理由（可选）", lines: 1...4, background: Theme.card)
            Btns {
                Btn("取消", background: Theme.card) {}
                Btn("不允许", kind: .negative, background: Theme.card) {}
            }
        }
        .padding(12)
        .background(Theme.soft, in: RoundedRectangle(cornerRadius: Theme.Radius.md, style: .continuous))
        .padding(.horizontal, 12).padding(.top, 10)
        SectionLabel("筛选框")
        FilterBox(text: $filter)
        FilterBox(text: $filled)
        // In a sheet, which has its own margin, it keeps none.
        FilterBox(text: $filter, placeholder: "搜索项目", inset: 0).padding(.horizontal, 24)
        SectionLabel("消息输入框")
        ComposerPill(text: $message, placeholder: "描述任务…", label: "描述任务…", plus: .dimmed, focus: $typing, onPlus: {}) {
            Button {} label: { ComposerCircle.send(enabled: false) }.buttonStyle(PressStyle()).disabled(true)
        }
        ComposerPill(text: $long, placeholder: "发消息…", label: "消息", focus: $typingLong, onPlus: {}) {
            Button {} label: { ComposerCircle.send(enabled: true) }.buttonStyle(PressStyle())
        }
        ComposerPill(text: $message, placeholder: "插入这一轮…", label: "消息", plus: .off, focus: $typing, onPlus: {}) {
            Button {} label: {
                ComposerCircle(Theme.chip, enabled: true) { Ic(Lucide.square, size: 14, color: Theme.red) }
            }
            .buttonStyle(PressStyle())
        }
        SectionLabel("说明")
        VStack(alignment: .leading, spacing: 0) {
            FieldLabel("模型")
            Hint("仅显示当前模型支持的档位；空闲时点选提交，等电脑确认后更新实际值。")
            Hint("没能切换：MacBook 不在线", color: Theme.red)
        }
        .padding(.horizontal, 16)
    }
}

private struct StatesPage: View {
    var body: some View {
        SectionLabel("读取中")
        LoadingRow()
        LoadingRow("正在读取更早的会话…")
        // In a sheet, and under a group's label.
        LoadingRow("读取 MacBook 的设置…", inset: 0).padding(.horizontal, 24)
        LoadingRow(inset: 30)
        Skeleton()
        Skeleton(inset: 12)
        SectionLabel("失败")
        ErrorRow("没能读取会话：连不上服务器") {}
        ErrorRow("这段记录打不开，历史到这里为止")
        SectionLabel("空")
        PageNote("这个项目还没有会话")
        EmptyState("还没有会话", text: "")
        EmptyState("还没有会话", text: "MacBook 在线，可以直接开始", action: "开始第一个会话", onAction: {})
        Rectangle().fill(Theme.line).frame(height: 1).padding(.vertical, 8)
        EmptyState(
            "这台设备已被移出账户",
            text: "服务器不再接受它的登录：它在另一台设备上被移除了，或者服务器的数据被重置了。电脑上的会话不受影响。",
            action: "重新加入账户", onAction: {},
            note: "会先删除这台设备上的账户密钥和缓存，再用配对码加入。",
            link: "再试一次", onLink: {})
    }
}

private struct NavPage: View {
    @State private var tab = 0

    var body: some View {
        Page {
            VStack(spacing: 0) {
                ConnBanner({
                    var s = RemoteState()
                    s.link = .noServer
                    s.loaded = true
                    s.synced = true
                    return s
                }()) {}
                ViewTabs(["聊天", "终端"], selected: $tab, dot: 1)
                PageNote("导航栏是系统的：返回、边缘右滑和大字号都交给它。")
                Row44(action: {}) {
                    Dot(Theme.dot(.wait))
                    Text("修复 relay 重连后丢事件的问题").type(T.bodyB, Theme.fg).lineLimit(1)
                    Spacer(minLength: 0)
                    Text("14:02").type(T.meta, Theme.muted)
                }
                Bordered { Text("边框卡片：r-sm、1 pt line").type(T.sm, Theme.fg2) }.padding(16)
                Spacer()
            }
        }
        // The second line's last part in red: a session on full access.
        .navBar(
            "修复 relay 重连后丢事件的问题，并补上集成测试", agent: "Claude Code", subtitle: "等待审批", danger: "完全放行",
            dot: Theme.dot(.wait), moreDot: true, onMore: {})
    }
}

private struct SheetPage: View {
    @State private var shown = true
    @State private var mode = 0

    var body: some View {
        Page {
            Btn("打开弹层", fill: false) { shown = true }.padding(24)
        }
        .navBar(GalleryPage.sheet.title)
        .kitSheet(isPresented: $shown) {
            SheetHead("审批模式", action: "完成") { shown = false }
            ListItem("询问", desc: "需要审批时由你确认，超时拒绝", checked: mode == 0, onTap: { mode = 0 })
            ListItem("自动", desc: "由 Codex 自己判断，拿不准的在对话里问你", checked: mode == 1, onTap: { mode = 1 })
            ListItem("计划", desc: "只读和出计划，你批准后才开始改", checked: mode == 2, onTap: { mode = 2 })
            ListItem("完全放行", desc: "所有操作都不审批", checked: mode == 3, danger: true, last: true, onTap: { mode = 3 })
            Hint("只改当前会话")
        }
    }
}

private struct DialogPage: View {
    @State private var shown = true
    @State private var plain = false

    var body: some View {
        Page {
            VStack(spacing: 10) {
                Btn("移除…", kind: .negative) { shown = true }
                Btn("切换…") { plain = true }
            }
            .padding(24)
        }
        .navBar(GalleryPage.dialog.title)
        .confirm(
            isPresented: $shown, title: "移除 MacBook？", text: "它的会话记录会从服务器上删除，它要重新加入账户才能再连上。",
            confirm: "移除", danger: true) {}
        .confirm(isPresented: $plain, title: "恢复这个会话？", text: "会在 MacBook 上重新打开它。", confirm: "恢复") {}
    }
}

private struct InputPage: View {
    @State private var shown = true

    var body: some View {
        Page {
            Btn("重命名…", fill: false) { shown = true }.padding(24)
        }
        .navBar(GalleryPage.input.title)
        .inputAlert(
            isPresented: $shown, title: "重命名会话", initial: "修复 relay 重连", placeholder: "会话标题", confirm: "保存",
            text: "留空保存会恢复自动标题") { _ in }
    }
}

/// The input dialog whose text is saved elsewhere: it stays, locked, while it saves, and says why when that fails.
private struct SavingPage: View {
    @State private var shown = true

    var body: some View {
        Page {
            Btn("重命名…", fill: false) { shown = true }.padding(24)
        }
        .navBar(GalleryPage.saving.title)
        .inputAlert(
            isPresented: $shown, title: "重命名会话", initial: "修复 relay 重连", placeholder: "标题", text: "留空保存会恢复自动标题",
            lines: 1...1, debugSignal: "gallery.save", debugText: { "修复 relay 重连后丢事件的问题" }
        ) { _ in
            try await Task.sleep(for: .milliseconds(600))
            throw CommandError(CommandError.unavailable, "MacBook 不在线，指令没有发出")
        }
    }
}

private struct MenuPage: View {
    @State private var shown = true

    var body: some View {
        Page {
            Btn("更多…", fill: false) { shown = true }.padding(24)
        }
        .navBar(GalleryPage.menu.title, onMore: { shown = true })
        .actionMenu(
            isPresented: $shown,
            actions: [
                SheetAction("审批模式 · 询问") {},
                SheetAction("会话设置", note: "上下文 86%") {},
                SheetAction("重命名") {},
                SheetAction("分叉会话", enabled: false, note: "这一轮结束后可以分叉") {},
                SheetAction("正在中断…", busy: true) {},
                SheetAction("结束会话", danger: true, note: "没能结束：MacBook 不在线", noteError: true, keepOpen: true) {},
            ]
        ) {
            VStack(spacing: 2) {
                Text("Codex · remote-agent · MacBook").type(T.sm, Theme.fg2)
                Text("~/Code/remote-agent").type(T.mono, Theme.muted)
            }
        }
    }
}

#Preview("组件") { GalleryRoot(page: .index) }
#Preview("按钮") { GalleryRoot(page: .page(.buttons)) }
#Preview("分组列表") { GalleryRoot(page: .page(.lists)) }
#Preview("选项与切换") { GalleryRoot(page: .page(.choices)) }
#Preview("输入") { GalleryRoot(page: .page(.fields)) }
#Preview("提示条") { GalleryRoot(page: .page(.notices)) }
#Preview("图标与标记") { GalleryRoot(page: .page(.icons)) }
#Preview("颜色与字号") { GalleryRoot(page: .page(.tokens)) }
#Preview("确认对话框") { GalleryRoot(page: .page(.dialog)) }
#Preview("更多菜单") { GalleryRoot(page: .page(.menu)) }
#endif
