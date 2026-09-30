import { Code, ConnectError } from "@connectrpc/connect";
import qrcode from "qrcode-generator";
import { useEffect, useRef, useState } from "react";
import { DeviceKind } from "../gen/remoteagent/v1/relay_pb";
import { initiate, joinByPairing, PairCanceled, parsePairLink, type Initiation } from "../pairing";
import { errorText, join, type Remote } from "../remote";
import { Banner, NavBar, useRemote } from "../ui";

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

const kindName = (k: DeviceKind) => (k === DeviceKind.MACHINE ? "新电脑" : "新设备");

/** Adds a device: shows a QR code and a code, then the emoji to check. */
export function AddDevicePage({ remote, onBack }: { remote: Remote; onBack: () => void }) {
  const [p, setP] = useState<Initiation>();
  const [req, setReq] = useState<{ emoji: string[]; kind: DeviceKind }>();
  const [state, setState] = useState<"waiting" | "sending" | "done">("waiting");
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  useRemote(remote);
  // Pairing calls need the login token: wait for the connection.
  const online = remote.conn === "online";
  useEffect(() => {
    if (!online) return;
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
  }, [remote, attempt, online]);
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
  const again = () => setAttempt((a) => a + 1);
  const who = req ? kindName(req.kind) : "新设备";
  let banner;
  if (error) {
    if (/不确定|可能没有加入成功/.test(error))
      banner = (
        <Banner kind="warn" action="重试" onAction={again}>
          不确定{who}收到没有，到{who}上看一眼是否已加入
        </Banner>
      );
    else if (/过期/.test(error))
      banner = (
        <Banner kind="err" action="换一个" onAction={again}>
          配对码过期了
        </Banner>
      );
    else
      banner = (
        <Banner kind="err" action="重新开始" onAction={again}>
          {error}
        </Banner>
      );
  } else if (state === "sending") banner = <Banner>◌ 正在把密钥交给{who}…</Banner>;
  else if (state === "done")
    banner = (
      <Banner action="完成" onAction={onBack}>
        ✓ {who}已加入
      </Banner>
    );
  return (
    <div className="page">
      <NavBar onBack={onBack} title="添加设备" />
      <div className="body pairing">
        {p && state !== "done" && !(error && !req) && (
          <>
            <div className="qr">
              <QR text={p.link} />
            </div>
            <div className="code">{spaced(p.code)}</div>
            <p className="muted small center-text">
              用新手机的相机扫码，或在新设备的网页里输入这 8 位数字。5 分钟内有效。
              <br />
              电脑上运行 <code>remote-agent account join --server {remote.account.server} --code {p.code}</code>
            </p>
          </>
        )}
        {!p && !error && <p className="center-text muted small">◌ 准备中…</p>}
        {req && state !== "done" && !error && (
          <div className="req">
            <div className="small">
              <b>{who}</b> <span className="muted">请求加入</span>
            </div>
            <div className="emoji">{req.emoji.join(" ")}</div>
            <div className="muted small center-text" style={{ marginBottom: 10 }}>
              和新设备上的 4 个图案一模一样才允许
            </div>
            <div className="btns">
              <button className="btn no" disabled={state === "sending"} onClick={() => (p?.cancel(), again())}>
                拒绝
              </button>
              <button className="btn pri" disabled={state === "sending"} onClick={() => void approve()}>
                图案一致，允许
              </button>
            </div>
          </div>
        )}
        {banner && <div className="pad">{banner}</div>}
      </div>
    </div>
  );
}

const defaultServer = () => localStorage.getItem("ra-server") || import.meta.env.VITE_DEFAULT_SERVER || "";

/** A joining error, as the banner the new device shows. */
function joinBanner(e: unknown): { kind: "err" | "warn"; text: string; action: string } {
  const text = errorText(e);
  if (e instanceof ConnectError && [Code.NotFound, Code.InvalidArgument, Code.PermissionDenied, Code.FailedPrecondition].includes(e.code))
    return { kind: "err", text: "配对码不对，或者已经用过了", action: "重填" };
  if (e instanceof ConnectError && e.code === Code.ResourceExhausted) return { kind: "err", text: "试得太多了，等一分钟再试", action: "重填" };
  if (/取消/.test(text)) return { kind: "err", text: "对方拒绝了这次加入", action: "重新开始" };
  if (/过期/.test(text)) return { kind: "err", text: "配对码过期了", action: "重填" };
  if ((e instanceof ConnectError && e.code === Code.Unavailable) || e instanceof TypeError) return { kind: "warn", text: "不确定是否成功：网络断了", action: "重试" };
  return { kind: "err", text, action: "重新开始" };
}

/** Joins this browser to an account: by pairing code, pairing link, or
 * the account secret. */
export function Setup({ onDone }: { onDone: () => void }) {
  const fromLink = location.hash.startsWith("#/pair?") ? parsePairLink(location.hash.slice("#/pair?".length)) : undefined;
  const [code, setCode] = useState(fromLink ? spaced(fromLink.code) : "");
  const [server, setServer] = useState(fromLink?.server ?? defaultServer());
  const [secret, setSecret] = useState("");
  const [useSecret, setUseSecret] = useState(false);
  const [emoji, setEmoji] = useState<string[]>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ReturnType<typeof joinBanner>>();
  const stopped = useRef(false);
  // The user gave up waiting; and whether this device already began to
  // register, after which giving up cannot undo the join.
  // One token per attempt: giving up cancels that attempt for good, and a
  // new attempt never revives an old one or takes its late callbacks.
  const attempt = useRef<{ canceled: boolean }>({ canceled: false });
  const [registering, setRegistering] = useState(false);
  const started = useRef(false);
  // Mounting again, as StrictMode does in development, must not leave the
  // join in progress marked as stopped.
  useEffect(() => {
    stopped.current = false;
    return () => {
      stopped.current = true;
    };
  }, []);

  const pair = async (link = fromLink ?? { code, server }) => {
    const mine = { canceled: false };
    attempt.current = mine;
    const current = () => attempt.current === mine;
    setRegistering(false);
    setBusy(true);
    setError(undefined);
    setEmoji(undefined);
    try {
      await joinByPairing(
        link,
        (e) => current() && setEmoji(e),
        () => stopped.current || mine.canceled || !current(),
        () => current() && setRegistering(true),
      );
      if (!current()) return;
      location.hash = "";
      onDone();
    } catch (e) {
      // Given up or replaced: the page already says what it is doing.
      if (!current() || (e instanceof PairCanceled && mine.canceled)) return;
      setRegistering(false);
      setError(joinBanner(e));
      setEmoji(undefined);
      setBusy(false);
    }
  };
  /** Stops waiting on this device and goes back to the code. The server
   * is kept and the old code cleared; the old request may still hold the
   * code on the other device, which should refuse it. */
  const giveUp = () => {
    attempt.current.canceled = true;
    setEmoji(undefined);
    setBusy(false);
    setCode("");
    setError({ kind: "warn", text: "已停止这次等待。请在另一台设备上拒绝旧请求，再生成新的配对码。", action: "" });
    if (location.hash.startsWith("#/pair?")) location.hash = "";
  };
  useEffect(() => {
    // Once per page: a second join would take the pairing's only slot.
    if (fromLink && !started.current) {
      started.current = true;
      void pair(fromLink);
    }
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const paste = async () => {
    setBusy(true);
    setError(undefined);
    try {
      await join(secret);
      setSecret("");
      onDone();
    } catch (e) {
      setError({ kind: "err", text: errorText(e), action: "" });
      setBusy(false);
    }
  };

  const banner = error && (
    <div className="pad">
      <Banner
        kind={error.kind}
        action={error.action || undefined}
        onAction={() => {
          if (error.action === "重填") {
            setCode("");
            setError(undefined);
          } else if (error.action) void pair({ code, server });
        }}
      >
        {error.text}
      </Banner>
    </div>
  );

  if (emoji)
    return (
      <div className="setup">
        <div className="large">
          <h3>核对图案</h3>
        </div>
        <div className="emoji big">{emoji.join(" ")}</div>
        <p className="muted center-text small">确认另一台设备上显示的 4 个图案和这里一模一样，然后在那台设备上点「图案一致，允许」。</p>
        <p className="muted center-text small" style={{ marginTop: 24 }}>
          {registering ? "◌ 另一台设备已允许，正在登记这个浏览器…" : "◌ 等待允许…"}
        </p>
        <div className="pair-wait-actions">
          <button className="btn" disabled={registering} onClick={giveUp}>
            图案不一致，返回填码
          </button>
          <p className="muted center-text small">返回后停止这次等待；请让另一台设备拒绝旧请求，再生成新的配对码。</p>
        </div>
      </div>
    );

  return (
    <div className="setup">
      <div className="large">
        <h3>加入账户</h3>
      </div>
      {!useSecret ? (
        <>
          <p className="muted small lead">在已加入的设备上打开「设置 › 添加设备」，扫二维码，或把 8 位配对码填在这里。</p>
          <input
            className="field code-in"
            inputMode="numeric"
            autoComplete="one-time-code"
            placeholder="0000 0000"
            value={code}
            onChange={(e) => {
              const d = e.target.value.replace(/\D/g, "").slice(0, 8);
              setCode(d.length > 4 ? spaced(d) : d);
            }}
            aria-label="配对码"
          />
          <label className="field">
            <small>服务器</small>
            <input placeholder="https://ra.example.com" value={server} onChange={(e) => setServer(e.target.value.trim())} aria-label="服务器" />
          </label>
          {banner}
          <div className="big">
            <button className="btn pri" disabled={busy || code.replace(/\s/g, "").length !== 8 || !server} onClick={() => void pair({ code, server })}>
              {busy ? "连接中…" : "加入"}
            </button>
          </div>
          <button className="link mid" onClick={() => setUseSecret(true)}>
            改用账户密钥
          </button>
        </>
      ) : (
        <>
          <p className="muted small lead">
            在电脑上运行 <code>remote-agent account secret</code>，把输出的账户密钥粘贴到这里。密钥只保存在这个浏览器里，不会发给服务器。
          </p>
          <textarea className="field mono" value={secret} onChange={(e) => setSecret(e.target.value)} placeholder="ra1.…" rows={4} autoComplete="off" spellCheck={false} aria-label="账户密钥" />
          {banner}
          <div className="big">
            <button className="btn pri" disabled={busy || !secret.trim()} onClick={() => void paste()}>
              {busy ? "连接中…" : "加入"}
            </button>
          </div>
          <button className="link mid" onClick={() => setUseSecret(false)}>
            改用配对码
          </button>
        </>
      )}
    </div>
  );
}
