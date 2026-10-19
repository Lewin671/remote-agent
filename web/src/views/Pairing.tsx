import { SetupReset } from "../SetupReset";
import { Code, ConnectError } from "@connectrpc/connect";
import { useEffect, useRef, useState } from "react";
import { joinByPairing, PairCanceled, parsePairLink } from "../pairing";
import { errorText } from "../remote";
import { Banner, NavBar } from "../ui";
import { LoaderCircle } from "../icons";

const spaced = (code: string) => `${code.slice(0, 4)} ${code.slice(4)}`;

/** Adding a device is done on the phone: this browser can only join one. */
export function AddDevicePage({ onBack }: { onBack: () => void }) {
  return (
    <div className="page">
      <NavBar onBack={onBack} title="添加设备" />
      <div className="body">
        <div className="grp">
          <div className="row help">添加和删除设备只在手机 App 上进行。</div>
        </div>
        <div className="manage-hint">
          打开手机上的 Remote
          Agent，进入「设备」，点「添加设备」，再在新设备上扫码或输入它显示的 8
          位配对码。
        </div>
      </div>
    </div>
  );
}

const defaultServer = () =>
  localStorage.getItem("ra-server") ||
  import.meta.env.VITE_DEFAULT_SERVER ||
  "";

/** A joining error, as the banner the new device shows. */
function joinBanner(e: unknown): {
  kind: "err" | "warn";
  text: string;
  action: string;
} {
  const text = errorText(e);
  if (/security: clock/.test(text))
    return {
      kind: "warn",
      text: "本机时间比手机慢，操作会被拒绝。请校准系统时间和时区后再加入。",
      action: "重试",
    };
  if (e instanceof Error && /security chain is too large/.test(e.message))
    return {
      kind: "err",
      text: "账户的状态链太长，无法加入；请在手机上重建账户。",
      action: "",
    };
  if (
    e instanceof ConnectError &&
    [
      Code.NotFound,
      Code.InvalidArgument,
      Code.PermissionDenied,
      Code.FailedPrecondition,
    ].includes(e.code)
  )
    return { kind: "err", text: "配对码不对，或者已经用过了", action: "重填" };
  if (e instanceof ConnectError && e.code === Code.ResourceExhausted)
    return { kind: "err", text: "试得太多了，等一分钟再试", action: "重填" };
  if (/取消/.test(text))
    return { kind: "err", text: "对方拒绝了这次加入", action: "重新开始" };
  if (/过期/.test(text))
    return { kind: "err", text: "配对码过期了", action: "重填" };
  if (
    (e instanceof ConnectError && e.code === Code.Unavailable) ||
    e instanceof TypeError
  )
    return { kind: "warn", text: "不确定是否成功：网络断了", action: "重试" };
  return { kind: "err", text, action: "重新开始" };
}

/** Joins this browser with explicit approval from an enrolled device. */
export function Setup({
  onDone,
  existing,
}: {
  onDone: () => void;
  existing?: import("../storage").StoredAccount;
}) {
  const fromLink = location.hash.startsWith("#/pair?")
    ? parsePairLink(location.hash.slice("#/pair?".length))
    : undefined;
  const [code, setCode] = useState(fromLink ? spaced(fromLink.code) : "");
  const [server, setServer] = useState(
    existing?.server ?? fromLink?.server ?? defaultServer(),
  );
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
  const [comparisonPending, setComparisonPending] = useState(false);
  const comparison = useRef<((allowed: boolean) => void) | undefined>(
    undefined,
  );
  const started = useRef(false);
  // Mounting again, as StrictMode does in development, must not leave the
  // join in progress marked as stopped.
  useEffect(() => {
    stopped.current = false;
    return () => {
      stopped.current = true;
      comparison.current?.(false);
      comparison.current = undefined;
    };
  }, []);

  const pair = async (link = fromLink ?? { code, server }) => {
    const mine = { canceled: false };
    attempt.current = mine;
    const current = () => attempt.current === mine;
    comparison.current?.(false);
    comparison.current = undefined;
    setComparisonPending(false);
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
        existing,
        async () => {
          if (!current() || stopped.current || mine.canceled) return false;
          setComparisonPending(true);
          return new Promise<boolean>((resolve) => {
            comparison.current = resolve;
          });
        },
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
    comparison.current?.(false);
    comparison.current = undefined;
    setComparisonPending(false);
    setEmoji(undefined);
    setBusy(false);
    setCode("");
    setError({
      kind: "warn",
      text: "已停止这次等待。请在另一台设备上拒绝旧请求，再生成新的配对码。",
      action: "",
    });
    if (location.hash.startsWith("#/pair?")) location.hash = "";
  };
  useEffect(() => {
    // Once per page: a second join would take the pairing's only slot.
    if (fromLink && !started.current) {
      started.current = true;
      void pair(fromLink);
    }
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

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
      <SetupReset onDone={onDone} />
    </div>
  );

  if (emoji)
    return (
      <div className="setup">
        <div className="large">
          <h3>核对图案</h3>
        </div>
        <div className="emoji big">{emoji.join(" ")}</div>
        <p className="muted center-text small">
          核对另一台设备的 4 个图案；一致后先在本机确认，再在手机上允许。
        </p>
        {comparisonPending && (
          <button
            className="btn pri"
            onClick={() => {
              const finish = comparison.current;
              comparison.current = undefined;
              setComparisonPending(false);
              setRegistering(true);
              finish?.(true);
            }}
          >
            图案一致，信任并继续
          </button>
        )}
        <p className="muted center-text small" style={{ marginTop: 24 }}>
          <LoaderCircle />{" "}
          {comparisonPending
            ? "请先在本机确认图案"
            : registering
              ? "本机已确认，等待手机批准并完成绑定…"
              : "等待核对…"}
        </p>
        <div className="pair-wait-actions">
          <button className="btn" disabled={registering} onClick={giveUp}>
            图案不一致，返回填码
          </button>
          <p className="muted center-text small">
            返回后停止这次等待；请让另一台设备拒绝旧请求，再生成新的配对码。
          </p>
        </div>
      </div>
    );

  return (
    <div className="setup">
      <div className="large">
        <h3>{existing ? "一次性安全设置" : "加入账户"}</h3>
      </div>
      <>
        <p className="muted small lead">
          {existing
            ? "原设备身份、密钥和历史保留。在手机的「设备 › 添加设备」里重新配对，核对四个图案完成绑定。"
            : "在已加入的手机上打开「设备 › 添加设备」，用它扫这台设备的二维码，或把手机上显示的 8 位配对码填在这里。"}
        </p>
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
          <input
            placeholder="https://ra.example.com"
            value={server}
            onChange={(e) => setServer(e.target.value.trim())}
            aria-label="服务器"
          />
        </label>
        {banner}
        <div className="big">
          <button
            className="btn pri"
            disabled={busy || code.replace(/\s/g, "").length !== 8 || !server}
            onClick={() => void pair({ code, server })}
          >
            {busy ? "连接中…" : "加入"}
          </button>
        </div>
      </>
    </div>
  );
}
