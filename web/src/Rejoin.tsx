import { useState } from "react";
import { Alert, Banner } from "./ui";
import { authorityExitAdvice } from "./security-operation";
import { forgetAccount, loadRegistration } from "./storage";
import { errorText } from "./remote";

/** Rejoin keeps only the server; account keys and cached ciphertext are erased. */
export function RejoinButton({ server, children, onDone }: {
  server: string; children: string; onDone?: () => void;
}) {
  const [advice, setAdvice] = useState<string>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const leave = async (confirmed = false) => {
    if (busy) return;
    setBusy(true);
    try {
      const status = await authorityExitAdvice();
      const pending = !!await loadRegistration();
      const warning = status.authority || status.unresolved || pending
        ? (pending ? "这台设备还有未完成的加入请求，删除后将无法继续原请求。" : status.message)
          + "这会删除本机的账户密钥和缓存，电脑上的会话不受影响。" : undefined;
      if (warning && (!confirmed || warning !== advice)) { setAdvice(warning); return; }
      localStorage.setItem("ra-server", server);
      await forgetAccount();
      location.hash = "/pair?";
      if (onDone) onDone(); else location.reload();
    } catch (e) { setError(errorText(e)); }
    finally { setBusy(false); }
  };
  return <>
    <button className="btn pri" disabled={busy} onClick={() => void leave()}>{children}</button>
    {error && <Banner kind="err">{error}</Banner>}
    {advice && <Alert title="删除本机密钥并重新加入？" text={advice} ok="重新加入" danger
      onCancel={() => setAdvice(undefined)} onOk={() => void leave(true)} />}
  </>;
}

export function RetainedIdentityDeniedScreen({ server, onDone }: { server: string; onDone: () => void }) {
  return <div className="center"><div className="gone">
    <div className="et">服务器不认识这台设备</div>
    <p className="muted small">可能已被移除，或服务已重建。电脑上的会话不受影响。</p>
    <RejoinButton server={server} onDone={onDone}>用配对码重新加入</RejoinButton>
    <p className="muted small">会先删除这个浏览器里的账户密钥和缓存，再用配对码加入。</p>
  </div></div>;
}
