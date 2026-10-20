export function PersistenceResult({ persisted }: { persisted: boolean }) {
  return (
    <p className="hint" role="status">
      {persisted ? "已请求浏览器保留数据" : "浏览器没有承诺保留数据"}
    </p>
  );
}
export function CodeBackup({
  code,
  saved,
  onSaved,
}: {
  code: string;
  saved: boolean;
  onSaved: (v: boolean) => void;
}) {
  return (
    <>
      <div className="glabel">新的恢复码 · 只显示这一次</div>
      <pre className="recovery-code" aria-label="新的恢复码">
        {code}
      </pre>
      <p className="hint">恢复码必须离线保存。不要把它发给别人。</p>
      <label className="backup-check">
        <input
          type="checkbox"
          checked={saved}
          onChange={(e) => onSaved(e.target.checked)}
        />
        我已离线保存新的恢复码
      </label>
    </>
  );
}
