import SwiftUI

private let mixed = """
找到原因了：`auth/login.go` 每次都新建连接，**没有复用**连接池，~~也没设超时~~。
见 [auth/login.go:58](/Users/me/foo/auth/login.go:58) 和 https://example.com/docs。*斜体 italic*，以及一个会折行的行内代码 `internal/server/handlers/authentication/login_handler.go`。

## 改了什么 `pool.go`
1. 复用连接池
   - 默认 8 个连接
   - 可配置
2. 加超时

**验证**

| 场景 | 改动前 | 改动后 |
| --- | --- | --- |
| 50 并发 | 180 ms | 42 ms |
| 200 并发 | 5.2 s | 61 ms |

```diff
--- a/auth/login.go
+++ b/auth/login.go
-conn := dial()
+conn := pool.Get() // a long line scrolls sideways inside the block and is never wrapped
```

> 注意：上线前先灰度。
> - [x] 单测
> - [ ] 压测

---

#### 后续
9. 九
10. 十

Averyveryveryveryveryveryveryveryveryveryveryveryverylongunbrokenstringwithoutanyspaces

### 长代码
```
\((1...20).map { "line \($0)" }.joined(separator: "\n"))
```
"""

private let tables = """
| 估值口径 | 每股收益 EPS | 市盈率 |
| --- | ---: | ---: |
| 2026 财年实际，GAAP | $74.33 | **14.5 倍** |
| 2026 财年实际，调整后 | $75.52 | **14.3 倍** |
| 下季指引中值 ×4 | $152.60 | **7.1 倍** |
| 第三方列示的 2027 财年预期 | $159.03 | **6.8 倍** |

| 第一列 | 第二列 | 第三列 | 第四列 | 第五列 | 第六列 |
| --- | --- | --- | --- | --- | --- |
| 长内容一 | 长内容二 | 长内容三 | 长内容四 | 长内容五 | 长内容六 |

| 说明 | 数值 |
| --- | ---: |
| \(String(repeating: "很长的内容不会被截断，", count: 8)) | 7.1 倍 |
| 下一行 | 14.3 倍 |

| 标签 | 代码 | 数值 |
| --- | --- | ---: |
| 第一行 | `a\\|b` | 14.5 |
| 第二行 | | 7.1 |
| 第三行 | `x` | |
"""

private let lists = """
- 第一层
  - 第二层 `code`
    - 第三层
      1. 有序
      2. 有序二
- [x] 做完的任务
  - 子项也变灰
- [ ] 没做完的任务
- 这一项很长，长到在手机上一定会折行，所以这个列表的各项之间要多留一些空白

> 注意：
> - 一
>   ```sh
>   ls
>   ```
> - [x] 二

1. 步骤

   | a | b |
   | --- | --- |
   | 1 | 2 |
"""

private struct Page: View {
    let text: String

    var body: some View {
        ScrollView {
            MarkdownView(text, bleed: 16).padding(16)
        }
    }
}

#Preview("Mixed document") { Page(text: mixed) }
#Preview("Mixed document, dark, large text") { Page(text: mixed).preferredColorScheme(.dark).dynamicTypeSize(.accessibility2) }
#Preview("Wide tables") { Page(text: tables) }
#Preview("Nested lists") { Page(text: lists) }
