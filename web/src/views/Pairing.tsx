import qrcode from "qrcode-generator";
import { useEffect, useRef, useState } from "react";
import { DeviceKind } from "../gen/remoteagent/v1/relay_pb";
import { initiate, joinByPairing, parsePairLink, type Initiation } from "../pairing";
import { errorText, join, type Remote } from "../remote";

/** A QR code drawn as SVG from the module grid. */
export function QR({ text }: { text: string }) {
  const q = qrcode(0, "M");
  q.addData(text);
  q.make();
  const n = q.getModuleCount();
  let d = "";
  for (let y = 0; y < n; y++) for (let x = 0; x < n; x++) if (q.isDark(y, x)) d += `M${x} ${y}h1v1h-1z`;
  return (
    <svg className="qrsvg" viewBox={`-2 -2 ${n + 4} ${n + 4}`} shapeRendering="crispEdges" role="img" aria-label="配对二维码">
      <rect x={-2} y={-2} width={n + 4} height={n + 4} fill="#fff" />
      <path d={d} fill="#16181c" />
    </svg>
  );
}

const spaced = (code: string) => `${code.slice(0, 4)} ${code.slice(4)}`;

/** Adds a device: shows a QR code and a code, then the emoji to check. */
export function AddDevicePage({ remote, onBack }: { remote: Remote; onBack: () => void }) {
  const [p, setP] = useState<Initiation>();
  const [req, setReq] = useState<{ emoji: string[]; kind: DeviceKind }>();
  const [state, setState] = useState<"waiting" | "sending" | "done">("waiting");
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    let cur: Initiation | undefined;
    let alive = true;
    setP(undefined);
    setReq(undefined);
    setState("waiting");
    setError("");
    initiate(remote).then(
      (x) => {
        cur = x;
        if (!alive) return x.cancel();
        setP(x);
        x.joined.then(
          (r) => alive && setReq(r),
          (e) => alive && setError(errorText(e)),
        );
      },
      (e) => alive && setError(errorText(e)),
    );
    return () => {
      alive = false;
      cur?.cancel();
    };
  }, [remote, attempt]);
  const approve = async () => {
    if (!p) return;
    setState("sending");
    try {
      await p.approve();
      setState("done");
    } catch (e) {
      setError(errorText(e));
    }
  };
  return (
    <>
      <header className="navbar">
        <button className="back" onClick={onBack} aria-label="返回">
          ‹
        </button>
        <span className="title">
          <b>添加设备</b>
        </span>
        <span className="more" />
      </header>
      <div className="body pairing">
        {error && (
          <div className="req">
            <p className="error">{error}</p>
            <button className="big" onClick={() => setAttempt((a) => a + 1)}>
              重新开始
            </button>
          </div>
        )}
        {!error && state === "done" && (
          <div className="req">
            <div className="emoji">✅</div>
            <p className="center-text">新设备已加入。</p>
          </div>
        )}
        {!error && state !== "done" && p && (
          <>
            <div className="qr">
              <QR text={p.link} />
            </div>
            <div className="code">{spaced(p.code)}</div>
            <p className="muted small center-text">
              用新手机的相机扫码，或在新设备的网页里输入这 8 位数字。
              <br />
              电脑上运行 <code>remote-agent account join --server {remote.account.server} --code {p.code}</code>
            </p>
            {!req && <p className="muted small center-text">等待新设备…（5 分钟内有效）</p>}
            {req && (
              <div className="req">
                <div className="req-t">
                  <b>{req.kind === DeviceKind.MACHINE ? "新电脑" : "新浏览器"}</b> <span className="muted">请求加入</span>
                </div>
                <div className="emoji">{req.emoji.join(" ")}</div>
                <p className="muted small center-text">核对新设备上显示的 4 个图案是否一模一样。不一样就拒绝。</p>
                <div className="btns">
                  <button className="btn no" disabled={state === "sending"} onClick={() => (p.cancel(), setAttempt((a) => a + 1))}>
                    拒绝
                  </button>
                  <button className="btn pri" disabled={state === "sending"} onClick={() => void approve()}>
                    {state === "sending" ? "发送中…" : "图案一致，允许"}
                  </button>
                </div>
              </div>
            )}
          </>
        )}
        {!error && !p && <p className="center muted">准备中…</p>}
      </div>
    </>
  );
}

const defaultServer = () => localStorage.getItem("ra-server") || import.meta.env.VITE_DEFAULT_SERVER || "";

/** Joins this browser to an account: by pairing code, pairing link, or
 * the account secret. */
export function Setup({ onDone }: { onDone: () => void }) {
  const fromLink = location.hash.startsWith("#/pair?") ? parsePairLink(location.hash.slice("#/pair?".length)) : undefined;
  const [code, setCode] = useState(fromLink?.code ?? "");
  const [server, setServer] = useState(fromLink?.server ?? defaultServer());
  const [secret, setSecret] = useState("");
  const [useSecret, setUseSecret] = useState(false);
  const [emoji, setEmoji] = useState<string[]>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const stopped = useRef(false);
  useEffect(
    () => () => {
      stopped.current = true;
    },
    [],
  );

  const pair = async (link = fromLink ?? { code, server }) => {
    setBusy(true);
    setError("");
    setEmoji(undefined);
    try {
      await joinByPairing(link, setEmoji, () => stopped.current);
      location.hash = "";
      onDone();
    } catch (e) {
      setError(errorText(e));
      setEmoji(undefined);
      setBusy(false);
    }
  };
  useEffect(() => {
    if (fromLink) void pair(fromLink);
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const paste = async () => {
    setBusy(true);
    setError("");
    try {
      await join(secret);
      setSecret("");
      onDone();
    } catch (e) {
      setError(errorText(e));
      setBusy(false);
    }
  };

  if (emoji)
    return (
      <div className="setup">
        <div className="large">
          <h3>核对图案</h3>
        </div>
        <div className="emoji">{emoji.join(" ")}</div>
        <p className="muted center-text">确认另一台设备上显示的 4 个图案和这里一模一样，然后在那台设备上点「图案一致，允许」。</p>
        <p className="muted small center-text">等待允许…</p>
      </div>
    );

  return (
    <div className="setup">
      <div className="large">
        <h3>添加这台设备</h3>
      </div>
      {!useSecret ? (
        <>
          <p className="muted">在已加入的设备上打开「设置 › 添加设备」，扫二维码，或把 8 位配对码填在这里。</p>
          <input
            className="ta code-in"
            inputMode="numeric"
            autoComplete="one-time-code"
            placeholder="配对码"
            value={code}
            onChange={(e) => setCode(e.target.value.replace(/[^\d ]/g, ""))}
            aria-label="配对码"
          />
          <input className="ta" placeholder="服务器，如 https://ra.example.com" value={server} onChange={(e) => setServer(e.target.value.trim())} aria-label="服务器" />
          {error && <p className="error">{error}</p>}
          <button className="big" disabled={busy || code.replace(/\s/g, "").length !== 8 || !server} onClick={() => void pair({ code, server })}>
            {busy ? "连接中…" : "加入"}
          </button>
          <button className="link small" onClick={() => setUseSecret(true)}>
            改用账户密钥
          </button>
        </>
      ) : (
        <>
          <p className="muted">
            在电脑上运行 <code>remote-agent account secret</code>，把输出的账户密钥粘贴到这里。密钥只保存在这个浏览器里，不会发给服务器。
          </p>
          <textarea className="ta" value={secret} onChange={(e) => setSecret(e.target.value)} placeholder="ra1.…" rows={4} autoComplete="off" spellCheck={false} aria-label="账户密钥" />
          {error && <p className="error">{error}</p>}
          <button className="big" disabled={busy || !secret.trim()} onClick={() => void paste()}>
            {busy ? "连接中…" : "连接"}
          </button>
          <button className="link small" onClick={() => setUseSecret(false)}>
            改用配对码
          </button>
        </>
      )}
    </div>
  );
}
