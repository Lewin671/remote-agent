import { Plus } from "../icons";
import { futureTime } from "../model";
import { useEffect, useRef, useState } from "react";
import qrcode from "qrcode-generator";
import type { AuthorityPairing } from "../authority-pairing";
import { clientName } from "../devices";
import { errorText, type Remote } from "../remote";
import { Alert, Banner, NavBar, useRemote } from "../ui";

export function authorityName(remote: Remote) {
  return clientName(
    remote.devices.get(remote.authorityDeviceId),
    remote.devices,
    remote.authorityDeviceId,
  );
}
export function ManagementEntry({
  remote,
  go,
}: {
  remote: Remote;
  go: (r: string) => void;
}) {
  return remote.isAuthority ? (
    <button className="row add" onClick={() => go("settings/add-device")}>
      <Plus />
      添加设备
    </button>
  ) : (
    <div className="manage-hint">
      要添加电脑或客户端，在管理端（{authorityName(remote)}）上打开「设置 ›
      添加设备」。
    </div>
  );
}
export function DeviceActions({
  remote,
  id,
  machine,
  go,
  onRemoved,
}: {
  remote: Remote;
  id: string;
  machine?: boolean;
  go?: (r: string) => void;
  onRemoved?: () => void;
}) {
  const [confirm, setConfirm] = useState<"remove" | "delete">();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const active = remote.devices.has(id);
  if (!remote.isAuthority)
    return (
      <div className="manage-hint">
        设备管理请在管理端（{authorityName(remote)}）上操作。
      </div>
    );
  if (id === remote.authorityDeviceId)
    return (
      <div className="manage-hint">管理端不能直接移除，请先交接管理权。</div>
    );
  return (
    <>
      <div className="grp">
        {active ? (
          <button
            className="row danger"
            disabled={busy}
            onClick={() => setConfirm("remove")}
          >
            移除设备
          </button>
        ) : (
          machine && (
            <>
              <button
                className="row act"
                disabled={busy}
                onClick={() => go?.("settings/add-device/" + id)}
              >
                重新加入并接续会话
              </button>
              <button
                className="row danger"
                disabled={busy}
                onClick={() => setConfirm("delete")}
              >
                删除这台电脑的记录
              </button>
            </>
          )
        )}
      </div>
      {error && <Banner kind="err">{error}</Banner>}
      {confirm && (
        <Alert
          title={confirm === "remove" ? "移除设备？" : "删除这台电脑的记录"}
          text={
            confirm === "remove"
              ? "移除后立即停止这台设备的远程访问。电脑的会话和历史档案仍保留，本地 CLI 不受影响。"
              : "将删除中继上的会话、事件和图片，无法撤销。不会删除电脑本地文件，也不会代替移除设备。"
          }
          ok={confirm === "remove" ? "移除" : "删除记录"}
          danger
          onCancel={() => setConfirm(undefined)}
          onOk={() => {
            const action = confirm;
            setConfirm(undefined);
            setBusy(true);
            setError("");
            void (
              action === "remove"
                ? remote.removeDevice(id)
                : remote.deleteMachineRecords(id)
            )
              .then(() => onRemoved?.())
              .catch((e) => setError(errorText(e)))
              .finally(() => setBusy(false));
          }}
        />
      )}
    </>
  );
}
export function PairCodePage({
  remote,
  replaces = "",
  onBack,
}: {
  remote: Remote;
  replaces?: string;
  onBack: () => void;
}) {
  useRemote(remote);
  const [pair, setPair] = useState<AuthorityPairing>();
  const [emoji, setEmoji] = useState<string[]>();
  const [confirmed, setConfirmed] = useState(false);
  const [approved, setApproved] = useState(false);
  const [done, setDone] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const current = useRef<AuthorityPairing | undefined>(undefined);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      if (current.current) void current.current.cancel().catch(() => {});
    };
  }, []);
  useEffect(() => {
    if (!pair || done || (emoji && !approved)) return;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      if (stopped) return;
      try {
        if (!document.hidden) {
          if (approved) {
            await pair.finishPairingOperation();
            if (!(await remote.authority.pending())) {
              await remote.loadDevices();
              if (!stopped) setDone(true);
              return;
            }
          } else {
            const joined = await pair.joined();
            if (!stopped && joined) setEmoji(joined.emoji);
          }
        }
      } catch (e) {
        if (!stopped) setError(errorText(e));
      }
      if (!stopped) timer = setTimeout(poll, 1500);
    };
    void poll();
    return () => {
      stopped = true;
      clearTimeout(timer);
    };
  }, [pair, approved, done, emoji, remote]);
  const run = async (job: () => Promise<void>) => {
    setBusy(true);
    setError("");
    try {
      await job();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  if (!remote.isAuthority)
    return (
      <div className="page">
        <NavBar title="添加设备" onBack={onBack} />
        <ManagementEntry remote={remote} go={() => {}} />
      </div>
    );
  const qr = pair
    ? (() => {
        const q = qrcode(0, "M");
        q.addData(pair.url);
        q.make();
        return q.createDataURL(5, 8);
      })()
    : "";
  return (
    <div className="page">
      <NavBar
        title={replaces ? "重新加入并接续会话" : "添加设备"}
        onBack={onBack}
      />
      <div className="body security-body">
        {done ? (
          <Banner kind="">设备已加入</Banner>
        ) : !pair ? (
          <>
            <p className="hint">
              生成配对码后，让新设备扫码或输入配对码。电脑运行{" "}
              <code>remote-agent join CODE --daemon</code>。
            </p>
            {replaces && (
              <p className="hint">
                只允许这台电脑以新身份接回原档案，保留原会话和历史。
              </p>
            )}
            <button
              className="btn pri"
              disabled={busy}
              onClick={() =>
                void run(async () => {
                  const p = await remote.startPairing(
                    location.origin + location.pathname,
                    replaces,
                  );
                  current.current = p;
                  if (!alive.current) {
                    await p.cancel();
                    return;
                  }
                  setPair(p);
                })
              }
            >
              {busy ? "生成中…" : "生成配对码"}
            </button>
          </>
        ) : (
          <>
            <img className="security-qr" src={qr} alt="添加设备的配对二维码" />
            <div className="pair-code">
              {pair.code.slice(0, 4)} {pair.code.slice(4)}
            </div>
            <p className="hint center-text">
              有效期至 {futureTime(pair.expires, Date.now(), "time")} ·
              一次只能加入一台设备
            </p>
            {emoji ? (
              <>
                <div className="emoji big">{emoji.join(" ")}</div>
                <p className="hint">
                  核对新设备上的 4 个图案。先在新设备确认，再在这里允许。
                </p>
                {!approved && (
                  <>
                    <label className="backup-check">
                      <input
                        type="checkbox"
                        checked={confirmed}
                        onChange={(e) => setConfirmed(e.target.checked)}
                      />
                      图案一致，新设备已先确认
                    </label>
                    <button
                      className="btn pri"
                      disabled={busy || !confirmed}
                      onClick={() =>
                        void run(async () => {
                          await pair.approve(confirmed);
                          setApproved(true);
                        })
                      }
                    >
                      允许加入
                    </button>
                  </>
                )}
                {approved && (
                  <p role="status" className="hint">
                    已批准，等待新设备完成绑定…
                  </p>
                )}
              </>
            ) : (
              <p role="status" className="hint center-text">
                等待新设备输入配对码…
              </p>
            )}
            {!approved && (
              <button
                className="btn"
                disabled={busy}
                onClick={() =>
                  void run(async () => {
                    try {
                      await pair.cancel();
                    } finally {
                      // The local SAS key is already destroyed even if the
                      // cancel acknowledgement is lost. Durable delivery is
                      // retained by the coordinator and checked on restart.
                      current.current = undefined;
                      setPair(undefined);
                      setEmoji(undefined);
                      setConfirmed(false);
                    }
                  })
                }
              >
                取消配对
              </button>
            )}
          </>
        )}
        {error && <Banner kind="err">{error}</Banner>}
      </div>
    </div>
  );
}
