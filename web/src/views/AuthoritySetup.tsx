import { futureTime } from "../model";
import { useEffect, useState } from "react";
import {
  AuthoritySetup,
  authorityStartup,
  type AuthorityStartup,
  type RecoveryStatus,
} from "../authority-setup";
import { GetRecoveryContextResponse_Phase as Phase } from "../gen/remoteagent/v1/relay_pb";
import {
  BROWSER_AUTHORITY_NOTICE,
  requestAuthorityPersistence,
} from "../security-operation";
import { errorText } from "../remote";
import { Banner, NavBar } from "../ui";
import { Setup } from "./Pairing";
import { CodeBackup, PersistenceResult } from "./AuthorityKit";
import { SetupReset } from "../SetupReset";

export function AccountEntry({
  onDone,
  setup: supplied,
}: {
  onDone: () => void;
  setup?: AuthoritySetup;
}) {
  const [setup, setSetup] = useState(supplied);
  const [mode, setMode] = useState<"" | "join" | "create" | "recovery">(
    location.hash.startsWith("#/pair?") ? "join" : "",
  );
  const [startup, setStartup] = useState<AuthorityStartup>();
  const [server, setServer] = useState(
    localStorage.getItem("ra-server") ||
      import.meta.env.VITE_DEFAULT_SERVER ||
      "",
  );
  const [token, setToken] = useState("");
  const [oldCode, setOldCode] = useState("");
  const [code, setCode] = useState("");
  const [saved, setSaved] = useState(false);
  const [persisted, setPersisted] = useState<boolean>();
  const [status, setStatus] = useState<RecoveryStatus>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const refresh = async () => {
    const state = await authorityStartup();
    setStartup(state);
    if (state.kind === "create" || state.kind === "recovery")
      setMode(state.kind);
  };
  useEffect(() => {
    const instance = supplied ?? new AuthoritySetup();
    setSetup(instance);
    void refresh().catch((e) => setError(errorText(e)));
    return () => {
      if (!supplied) instance.lifecycle.dispose();
    };
  }, [supplied]);
  const run = async (work: () => Promise<void>) => {
    setBusy(true);
    setError("");
    try {
      await work();
      await refresh();
    } catch (e) {
      setError(errorText(e));
      await refresh().catch(() => {});
    } finally {
      setBusy(false);
    }
  };
  if (!setup || (!startup && !error))
    return <div className="center muted">加载中…</div>;
  if (mode === "join")
    return (
      <>
        <NavBar onBack={() => setMode("")} title="加入已有账户" />
        <Setup onDone={onDone} />
      </>
    );
  if (!mode)
    return (
      <div className="setup authority-setup">
        <div className="large">
          <h3>remote-agent</h3>
        </div>
        <div className="grp">
          <button className="row act" onClick={() => setMode("create")}>
            新建账户
          </button>
          <button className="row act" onClick={() => setMode("join")}>
            加入已有账户
          </button>
          <button className="row act" onClick={() => setMode("recovery")}>
            用恢复码找回管理权
          </button>
        </div>
        {error && <Banner kind="err">{error}</Banner>}
      </div>
    );
  const pending = startup?.kind === mode;
  return (
    <div className="setup authority-setup">
      <NavBar
        title={mode === "create" ? "新建账户" : "用恢复码找回管理权"}
        onBack={!pending ? () => setMode("") : undefined}
      />
      <p className="hint">{BROWSER_AUTHORITY_NOTICE}</p>
      {!pending && (
        <label className="field">
          <small>服务器</small>
          <input
            aria-label="服务器"
            placeholder="https://ra.example.com"
            value={server}
            onChange={(e) => setServer(e.target.value.trim())}
          />
        </label>
      )}
      {mode === "recovery" && persisted !== undefined && (
        <PersistenceResult persisted={persisted} />
      )}
      {mode === "create" ? (
        <>
          {pending && !code && (
            <Banner kind="warn">
              原创建草稿已保留，继续同一个账户。恢复码不会再次显示；没有备份时，进入账户后立即换码。
            </Banner>
          )}
          {persisted !== undefined && (
            <PersistenceResult persisted={persisted} />
          )}
          {code && <CodeBackup code={code} saved={saved} onSaved={setSaved} />}
          {pending && (
            <label className="field">
              <small>Setup token（开放注册可留空）</small>
              <input
                aria-label="Setup token"
                type="password"
                value={token}
                onChange={(e) => setToken(e.target.value)}
              />
            </label>
          )}
          <button
            className="btn pri"
            disabled={busy || (!pending && !server) || (!!code && !saved)}
            onClick={() =>
              void run(async () => {
                if (!pending) {
                  const p = await setup.prepareCreation(server);
                  setCode(p.code ?? "");
                  setPersisted(p.storagePersisted);
                  return;
                }
                if (code && saved) {
                  await setup.confirmCreationCodeSaved();
                }
                if ((await authorityStartup()).kind === "create")
                  await setup.continueCreation(token);
                if (
                  !code &&
                  startup?.kind === "create" &&
                  !startup.codeSaved &&
                  (await authorityStartup()).kind === "create"
                )
                  await setup.adoptCreationWithoutCode();
                onDone();
              })
            }
          >
            {busy ? "处理中…" : !pending ? "生成恢复码" : "继续创建"}
          </button>
        </>
      ) : (
        <>
          {!pending && (
            <>
              <p className="hint">
                提交后需等待 48
                小时。当前管理端可以否决申请；恢复后无法读取恢复边界之前的历史。
              </p>
              <textarea
                className="field"
                aria-label="原恢复码"
                placeholder="原恢复码"
                value={oldCode}
                onChange={(e) => setOldCode(e.target.value)}
              />
              <button
                className="btn pri"
                disabled={busy || !server || !oldCode.trim()}
                onClick={() =>
                  void run(async () => {
                    setPersisted(await requestAuthorityPersistence());
                    const p = await setup.prepareEmergencyRecovery(
                      server,
                      oldCode,
                    );
                    setCode(p.code);
                    setOldCode("");
                  })
                }
              >
                准备恢复
              </button>
            </>
          )}
          {pending && (
            <>
              {code ? (
                <CodeBackup code={code} saved={saved} onSaved={setSaved} />
              ) : (
                <p className="hint">
                  原恢复草稿已保留。新恢复码不会再次显示；未保存时仍可完成原恢复，进入账户后立即换码。
                </p>
              )}
              {status && (
                <Banner kind={status.phase === Phase.REJECTED ? "err" : "warn"}>
                  {status.phase === Phase.REJECTED
                    ? "恢复已被否决，可以重新申请。"
                    : `恢复申请待处理。可生效时间：${futureTime(status.readyMs)}`}
                </Banner>
              )}
              {error.includes("签名尚未保存") && (
                <>
                  <textarea
                    className="field"
                    aria-label="原恢复码"
                    value={oldCode}
                    onChange={(e) => setOldCode(e.target.value)}
                  />
                  <button
                    className="btn"
                    disabled={busy || !oldCode}
                    onClick={() =>
                      void run(async () => {
                        await setup.continueRecoverySigning(oldCode);
                        setOldCode("");
                      })
                    }
                  >
                    继续原草稿
                  </button>
                  <button
                    className="btn"
                    disabled={busy}
                    onClick={() =>
                      void run(async () => {
                        await setup.discardUnsignedRecovery();
                        setMode("");
                      })
                    }
                  >
                    放弃未签名草稿
                  </button>
                </>
              )}
              <button
                className="btn pri"
                disabled={busy || (!!code && !saved)}
                onClick={() =>
                  void run(async () => {
                    if (saved) await setup.confirmRecoveryCodeSaved();
                    if (
                      status &&
                      [Phase.READY, Phase.PUBLISHED].includes(status.phase)
                    ) {
                      await setup.completeRecovery();
                      onDone();
                    } else setStatus(await setup.resumeRecovery());
                  })
                }
              >
                {busy
                  ? "处理中…"
                  : status &&
                      [Phase.READY, Phase.PUBLISHED].includes(status.phase)
                    ? "完成恢复"
                    : "提交或查询原恢复"}
              </button>
              {status?.phase === Phase.REJECTED && (
                <button
                  className="btn"
                  disabled={busy}
                  onClick={() =>
                    void run(async () => {
                      await setup.discardRejectedRecovery();
                      setStatus(undefined);
                      setCode("");
                      setSaved(false);
                    })
                  }
                >
                  重新申请
                </button>
              )}
            </>
          )}
        </>
      )}
      {error && <Banner kind="err">{error}</Banner>}
      {pending && <SetupReset onDone={onDone} />}
    </div>
  );
}
