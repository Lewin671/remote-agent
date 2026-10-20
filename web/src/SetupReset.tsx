import { useState } from "react";
import { Alert } from "./ui";
import { authorityExitAdvice } from "./security-operation";
import { forgetAccount } from "./storage";

/** The retry path retains uncertain work; only this explicit confirmation discards it. */
export function SetupReset({ onDone }: { onDone: () => void }) {
  const [asking, setAsking] = useState(false);
  const [error, setError] = useState("");
  const [advice, setAdvice] = useState("");
  return (
    <>
      {error && <p>{error}</p>}
      <button onClick={() => { void authorityExitAdvice().then(a => { setAdvice(a.message); setAsking(true); }).catch(e => setError(String(e))); }}>退出并重新开始</button>
      {asking && (
        <Alert
          title="退出并删除本机密钥？"
          text={
            error || advice ||
            "本机账户、未完成的注册和安全操作会被清除，需要重新配对或创建。服务器上的会话和档案不会被删除；结果未明的操作可能已经成功，请在管理端核实。"
          }
          ok="确认退出"
          danger
          onCancel={() => setAsking(false)}
          onOk={async () => {
            try {
              await forgetAccount();
              setAsking(false);
              location.hash = "";
              onDone();
            } catch (e) {
              setError(e instanceof Error ? e.message : String(e));
            }
          }}
        />
      )}
    </>
  );
}
