import { ChevronRight } from "../icons";
import { futureTime } from "../model";
import { useEffect, useState } from "react";
import {
  BROWSER_AUTHORITY_NOTICE,
  authorityExitAdvice,
  type SecurityOperation,
} from "../security-operation";
import { clientName } from "../devices";
import { DeviceKind } from "../gen/remoteagent/v1/relay_pb";
import { errorText, type Remote } from "../remote";
import { Alert, Banner, NavBar, useRemote, useNow, usePageVisible } from "../ui";
import { CodeBackup, PersistenceResult } from "./AuthorityKit";
import { forgetAccount } from "../storage";
import type { HandoffOffer } from "../authority-client";
import { equal } from "../device-security";

import { authorityName } from "./DeviceManagement";
export {
  authorityName,
  ManagementEntry,
  PairCodePage,
  DeviceActions,
} from "./DeviceManagement";

export function RecoveryNotice({
  remote,
  go,
}: {
  remote: Remote;
  go: (r: string) => void;
}) {
  useRemote(remote);
  if (!remote.isAuthority || !remote.recoveryPending) return null;
  return (
    <div className="gbanner">
      <Banner
        kind="warn"
        action="处理"
        onAction={() => go("settings/security")}
      >
        恢复申请待处理
        {remote.recoveryReadyMs > 0n
          ? ` · 可生效时间：${futureTime(remote.recoveryReadyMs)}`
          : ""}
      </Banner>
    </div>
  );
}
export function SecurityGroup({
  remote,
  go,
}: {
  remote: Remote;
  go: (r: string) => void;
}) {
  return (
    <>
      <div className="glabel">账户安全</div>
      <div className="grp">
        <button className="row" onClick={() => go("settings/security")}>
          账户安全<span className="v">{remote.isAuthority ? "本机是管理端" : authorityName(remote)}</span>
        </button>
      </div>
    </>
  );
}

export function LeaveAlert({ onCancel }: { onCancel: () => void }) {
  const [advice, setAdvice] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    void authorityExitAdvice()
      .then((a) => setAdvice(a.message))
      .catch((e) => setError(errorText(e)));
  }, []);
  return (
    <Alert
      title="退出这个浏览器？"
      text={error || advice || "正在核实管理身份和未完成的安全操作…"}
      ok={busy ? "退出中…" : "退出"}
      danger
      onCancel={onCancel}
      onOk={() => {
        if (!advice || busy) return;
        setBusy(true);
        void forgetAccount()
          .then(() => {
            location.hash = "";
            location.reload();
          })
          .catch((e) => {
            setError(errorText(e));
            setBusy(false);
          });
      }}
    />
  );
}
export function SecurityPage({
  remote,
  onBack,
}: {
  remote: Remote;
  onBack: () => void;
}) {
  useRemote(remote);
  const visible = usePageVisible();
  const [page, setPage] = useState<"overview" | "recovery" | "handoff" | "pending">("overview");
  const now = useNow(30_000);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const [readError, setReadError] = useState("");
  const [code, setCode] = useState(""),
    [saved, setSaved] = useState(false);
  const [persisted, setPersisted] = useState<boolean>();
  const [lease, setLease] = useState("");
  const [pending, setPending] = useState<SecurityOperation>();
  const [rotation, setRotation] = useState(false);
  const [offers, setOffers] = useState<HandoffOffer[]>([]);
  const [target, setTarget] = useState("");
  const [message, setMessage] = useState("");
  const [profileCode, setProfileCode] = useState("");
  const [profileReady, setProfileReady] = useState(true);
  const [takingOver, setTakingOver] = useState(false);
  const refresh = async () => {
    const r = await remote.security.verifiedRecord();
    setLease(
      r.lease && equal(r.lease.head, r.stateHash)
        ? futureTime(r.lease.notAfter)
        : "尚未取得远程控制有效期",
    );
    setTakingOver(
      remote.isAuthority &&
        remote.devices.get(remote.account.deviceId)?.meta?.handoffRecoveryEd
          .length === 32 &&
        (!r.lease || !equal(r.lease.head, r.stateHash)),
    );
    setPending(await remote.authority.pending());
    setRotation(await remote.authority.needsRecoveryCodeRotation());
    setProfileReady(await remote.authority.hasAccountPublicProfile());
    if (remote.isAuthority) setOffers(await remote.handoffCandidates());
  };
  useEffect(() => {
    if (!visible) return;
    let current = true;
    // App resumes the transport in its parent effect. Read after those
    // effects, and ignore a read invalidated when the page goes away.
    void Promise.resolve().then(() => current ? refresh() : undefined).then(() => {
      if (current) setReadError("");
    }).catch((e) => {
      if (current && !document.hidden) setReadError(errorText(e));
    });
    return () => { current = false; };
  }, [
    remote,
    visible,
    remote.isAuthority,
    remote.recoveryPending,
    remote.authorityError,
    now,
  ]);
  const run = async (work: () => Promise<void>) => {
    setBusy(true);
    setError("");
    setMessage("");
    try {
      await work();
      await refresh();
    } catch (e) {
      setMessage("");
      setError(errorText(e));
      await refresh().catch(() => {});
    } finally {
      setBusy(false);
    }
  };
  const offered =
    remote.devices.get(remote.account.deviceId)?.meta?.handoffRecoveryEd
      .length === 32;
  const draft =
    pending?.kind === "offer" ||
    (pending?.kind === "state" &&
      (pending.local as { action?: string }).action === "recovery");
  const localClient =
    remote.devices.get(remote.account.deviceId)?.kind === DeviceKind.CLIENT;
  return (
    <div className="page gray" data-route="account-security">
      <NavBar title={page === "overview" ? "账户安全" : page === "recovery" ? "恢复码" : page === "pending" ? "未完成的安全操作" : remote.isAuthority ? "交接管理权" : "接管管理权"} onBack={page === "overview" ? onBack : () => setPage("overview")} />
      <div className="body security-body">
        {remote.recoveryPending && (
          <Banner kind="warn" action={remote.isAuthority ? "否决" : undefined} onAction={() => setPage("recovery")}>
            {`有人正在用恢复码申请接管管理权，${remote.recoveryReadyMs > 0n ? futureTime(remote.recoveryReadyMs) : "48 小时"} 后生效。不是你本人就现在否决。`}
          </Banner>
        )}
        {page === "overview" && pending && (
          <div className="grp"><button className="row" onClick={() => setPage("pending")}>未完成的安全操作<span className="v"><ChevronRight /></span></button></div>
        )}
        {remote.security.recoveryStatusStale && (
          <Banner kind="warn">安全状态可能过期，请联网后重新核实。</Banner>
        )}
        {takingOver && (
          <Banner kind="warn">
            正在完成接管，请保持页面在前台，等待远程控制续期。
          </Banner>
        )}
        {rotation && (
          <Banner kind="warn">
            新恢复码尚未确认备份，请立即换码并离线保存。
          </Banner>
        )}
        {remote.authorityError && (
          <Banner kind="warn">{remote.authorityError}</Banner>
        )}
        {page === "overview" && <>
        <div className="grp">
          <div className="row">当前管理端<span className="v">{remote.isAuthority ? "本机" : authorityName(remote)}</span></div>
          <div className="row col">
            远程控制有效期<div className="hint">{lease || "正在读取…"}</div>
          </div>
          {remote.isAuthority && (
            <button
              className="row act"
              disabled={busy}
              onClick={() =>
                void run(async () => {
                  await remote.authority.renewLeaseNow();
                  if (await remote.authority.pending())
                    throw new Error("请先完成当前安全操作，再续期");
                  setMessage("远程控制已续期");
                })
              }
            >
              现在续期
            </button>
          )}
        </div>
        <p className="manage-hint">回到管理端时自动续期；过期后远程控制暂停，聊天和历史仍可查看。网页关闭或冻结时无法续期。</p>
        {!remote.isAuthority && <p className="manage-hint">{`请打开${authorityName(remote)}续期。`}</p>}
        <div className="grp">
          {remote.isAuthority && <button className="row" onClick={() => setPage("recovery")}>恢复码<span className="v"><ChevronRight /></span></button>}
          {(remote.isAuthority || localClient) && <button className="row" onClick={() => setPage("handoff")}>{remote.isAuthority ? "交接管理权" : "接管管理权"}<span className="v"><ChevronRight /></span></button>}
        </div>
        </>}
        {page === "pending" && pending && (
          <div className="grp">
            <div className="row help">有未完成的安全操作，请先继续原操作。</div>
            <button
              className="row act"
              disabled={busy}
              onClick={() =>
                void run(async () => {
                  await remote.authority.resumeSecurityOperation();
                })
              }
            >
              核实并继续
            </button>
            {draft && !pending.signed && (!pending.codeSaved || pending.kind === "offer") && (
              <button
                className="row act"
                disabled={busy}
                onClick={() =>
                  void run(async () => {
                    await remote.authority.discardUnpublishedCode();
                    setCode(""); setSaved(false);
                  })
                }
              >
                放弃未发布的恢复码
              </button>
            )}
            {pending.kind === "offer" &&
              (pending.local as { rejected?: boolean }).rejected && (
                <button
                  className="row act"
                  disabled={busy}
                  onClick={() =>
                    void run(async () => {
                      await remote.authority.discardRejectedOffer();
                    })
                  }
                >
                  放弃被拒绝的交接申请
                </button>
              )}
            {pending.kind === "state" && !pending.signed && !draft && (
              <button
                className="row act"
                disabled={busy}
                onClick={() =>
                  void run(async () => {
                    await remote.authority.discardUnsignedStateDraft();
                  })
                }
              >
                放弃未签名的操作
              </button>
            )}
          </div>
        )}
        {(page === "recovery" || page === "handoff") && code ? (
          <>
            <CodeBackup code={code} saved={saved} onSaved={setSaved} />
            {persisted !== undefined && (
              <PersistenceResult persisted={persisted} />
            )}
            <button
              className="btn pri"
              disabled={!saved || busy}
              onClick={() =>
                void run(async () => {
                  if (remote.isAuthority)
                    await remote.authority.publishRecovery();
                  else await remote.authority.offerHandoff();
                  if (await remote.authority.pending())
                    throw new Error("原操作尚未完成，请继续核实");
                  setCode("");
                  setSaved(false);
                  setMessage(
                    remote.isAuthority
                      ? "恢复码已更新，原恢复码和待处理恢复申请已作废。"
                      : "恢复码已确认保存，等待当前管理端批准；可以重新打开此页查看。",
                  );
                })
              }
            >
              {busy
                ? "发布中…"
                : remote.isAuthority
                  ? "我已保存，发布换码"
                  : "我已保存，通知管理端"}
            </button>
            <p className="hint">
              保存好之前不会发布。未发布的恢复码可在下次打开时明确放弃并重新生成。
            </p>
          </>
        ) : page === "recovery" && remote.isAuthority ? (
          <>
          <p className="hint">更换恢复码后，旧码立即作废；正在等待的恢复申请会被否决。</p>
          <div className="grp">
            <button
              className="row act"
              disabled={busy || !!pending}
              onClick={() =>
                void run(async () => {
                  const p = await remote.authority.prepareRecovery();
                  setCode(p.code);
                  setSaved(false);
                })
              }
            >
              生成新的恢复码
            </button>
          </div>
          </>
        ) : null}
        {page === "handoff" && (remote.isAuthority ? (
          <>
            <p className="hint">
              在接任设备加入账户并确认保存新恢复码后，在这里批准。交接保留可达历史，同时移除本机。
            </p>
            <div className="grp">
              {offers.map((o) => (
                <button
                  className="row act"
                  key={o.id}
                  disabled={busy || !!pending}
                  onClick={() => setTarget(o.id)}
                >
                  交接给{" "}
                  {clientName(remote.devices.get(o.id), remote.devices, o.id)}
                </button>
              ))}
              <button
                className="row act"
                disabled={busy}
                onClick={() => void run(async () => {})}
              >
                刷新接任设备
              </button>
              {!offers.length && (
                <div className="row help">等待接任设备生成并保存恢复码</div>
              )}
            </div>
          </>
        ) : localClient ? (
          <>
            <p className="hint">{BROWSER_AUTHORITY_NOTICE}</p>
            <p className="hint">
              先保存新的恢复码，再请当前管理端批准交接。当前管理端会被移除，不需要等待 48 小时。
            </p>
            {offered && (
              <Banner kind="warn">
                恢复码已确认保存，等待当前管理端批准；可以重新打开此页查看。
              </Banner>
            )}
            <button
              className="btn"
              disabled={busy || !!pending || !!code}
              onClick={() =>
                void run(async () => {
                  const p = await remote.authority.prepareHandoffCode();
                  setCode(p.code);
                  setPersisted(p.storagePersisted);
                  setSaved(false);
                })
              }
            >
              {offered ? "重新生成恢复码" : "生成新的恢复码"}
            </button>
          </>
        ) : (
          <p className="hint">电脑只能运行 CLI，不能接管管理权。</p>
        ))}
        {page === "handoff" && !profileReady && (
          <>
            <div className="glabel">补齐账户公钥</div>
            <p className="hint">
              接任前可重新配对，或在接任后输入新恢复码补齐公开资料。查询公开资料不会发起紧急恢复。
            </p>
            <button
              className="btn"
              disabled={busy}
              onClick={() => {
                location.hash = "/settings/rejoin";
              }}
            >
              重新配对
            </button>
            <textarea
              className="field"
              aria-label="补齐资料的恢复码"
              value={profileCode}
              onChange={(e) => setProfileCode(e.target.value)}
            />
            <button
              className="btn"
              disabled={busy || !profileCode}
              onClick={() =>
                void run(async () => {
                  const { AuthoritySetup } = await import("../authority-setup");
                  const setup = new AuthoritySetup();
                  try {
                    await setup.completeAccountPublicProfile(
                      remote.account.server,
                      profileCode,
                    );
                    setProfileCode("");
                    setMessage("账户公钥已补齐");
                  } finally {
                    setup.lifecycle.dispose();
                  }
                })
              }
            >
              补齐公开资料
            </button>
          </>
        )}
        {message && <Banner kind="">{message}</Banner>}
        {(error || readError) && <Banner kind="err">{error || readError}</Banner>}
      </div>
      {target && (
        <Alert
          title="交接管理权"
          text={`确认接任设备（${clientName(remote.devices.get(target), remote.devices, target)}）已经保存新的恢复码。交接后本机会被移除，接任设备成为唯一管理端。`}
          ok="确认交接"
          danger
          onCancel={() => setTarget("")}
          onOk={() => {
            const id = target;
            setTarget("");
            void run(async () => {
              await remote.handoffAuthority(id);
              if (await remote.authority.pending())
                throw new Error("交接结果尚未核实，请继续原操作");
              setMessage("已交接管理权，等待接任设备完成接管。");
            });
          }}
        />
      )}
    </div>
  );
}
