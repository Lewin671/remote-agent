import { useState } from "react";
import { machineName, when } from "../model";
import { errorText, join, type Remote } from "../remote";
import { forgetAccount } from "../storage";
import { getTheme, setTheme, type Theme } from "../theme";
import { Dot, useRemote } from "../ui";

export function Settings({ remote }: { remote: Remote }) {
  useRemote(remote);
  const machines = [...remote.machines.values()].sort((a, b) => machineName(a).localeCompare(machineName(b)));
  return (
    <div className="body settings">
      <div className="large">
        <h3>设置</h3>
      </div>
      <div className="glabel">设备</div>
      <div className="grp">
        <div className="row">
          🌐 这个浏览器<span className="v">本机</span>
        </div>
        {machines.map((m) => (
          <div key={m.id} className="row">
            💻 {machineName(m)}
            <span className="v">
              <Dot tone={m.online ? "idle" : "off"} />
              {m.online ? "在线" : when(m.lastSeenMs) || "离线"}
            </span>
          </div>
        ))}
      </div>
      <div className="glabel">外观</div>
      <div className="grp">
        <div className="row">
          <ThemePicker />
        </div>
      </div>
      <div className="glabel">添加设备</div>
      <div className="grp">
        <div className="row help">
          在已加入的电脑上运行 <code>remote-agent account secret</code>，把输出粘贴到新设备的网页，或在新电脑上运行 <code>remote-agent account join</code>。
        </div>
      </div>
      <div className="glabel">账户</div>
      <div className="grp">
        <div className="row">
          服务器<span className="v">{new URL(remote.account.server).host}</span>
        </div>
        <button
          className="row danger"
          onClick={async () => {
            if (confirm("在这个浏览器里删除账户密钥？之后需要重新粘贴。")) {
              await forgetAccount();
              location.hash = "";
              location.reload();
            }
          }}
        >
          退出并删除本机密钥
        </button>
      </div>
      {remote.error && <p className="empty">最近的错误：{remote.error}</p>}
    </div>
  );
}

export function Setup({ onDone }: { onDone: () => void }) {
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submit = async () => {
    setBusy(true);
    setError("");
    try {
      await join(text);
      setText("");
      onDone();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="setup">
      <div className="large">
        <h3>添加设备</h3>
      </div>
      <p className="muted">
        在已加入的电脑上运行 <code>remote-agent account secret</code>，把输出的账户密钥粘贴到这里。密钥只保存在这个浏览器里，不会发给服务器。
      </p>
      <textarea className="ta" value={text} onChange={(e) => setText(e.target.value)} placeholder="ra1.…" rows={4} autoComplete="off" spellCheck={false} aria-label="账户密钥" />
      {error && <p className="error">{error}</p>}
      <button className="big" disabled={busy || !text.trim()} onClick={submit}>
        {busy ? "连接中…" : "连接"}
      </button>
    </div>
  );
}

function ThemePicker() {
  const [theme, set] = useState<Theme>(getTheme);
  const opts: [Theme, string][] = [
    ["auto", "自动"],
    ["light", "亮色"],
    ["dark", "暗色"],
  ];
  return (
    <div className="seg full">
      {opts.map(([t, name]) => (
        <button
          key={t}
          className={t === theme ? "on" : ""}
          onClick={() => {
            setTheme(t);
            set(t);
          }}
        >
          {name}
        </button>
      ))}
    </div>
  );
}
