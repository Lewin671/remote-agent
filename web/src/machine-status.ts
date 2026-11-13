import { t } from "./i18n";
import type { MachineStatus, ResourceUsage, DiskStatus } from "./gen/remoteagent/v1/local_pb";
import type { MachineMeta } from "./gen/remoteagent/v1/remote_pb";
export const machineCleanupPrompt = () => t("machine_status.cleanup_prompt");
export type MachinePollState = "idle" | "loading" | "live" | "stale" | "unavailable";
export const isDiskFull = (error: unknown): boolean => typeof error === "object" && error !== null && "code" in error && error.code === "disk_full";
export function recentMachineProject(meta: MachineMeta): string | undefined {
  return [...meta.projects].sort((a,b) => Number((b.latestCreatedMs > b.latestEndedMs ? b.latestCreatedMs : b.latestEndedMs) - (a.latestCreatedMs > a.latestEndedMs ? a.latestCreatedMs : a.latestEndedMs)))[0]?.path;
}
export function machineDiskReminder(online: boolean, meta: MachineMeta): DiskStatus | undefined {
  const disk = meta.diskStatus;
  return online && meta.capabilities?.machineStatus && disk && disk.availableBytes < 10_000_000_000n ? disk : undefined;
}
export function stableUsage(previous: readonly string[], rows: readonly ResourceUsage[], memory = false): ResourceUsage[] {
  const byId = new Map(rows.map(r=>[r.id,r]));
  const ordered: ResourceUsage[] = [];
  for(const id of previous) { const row=byId.get(id); if(row) { ordered.push(row); byId.delete(id); } }
  const metric = (r: ResourceUsage) => memory ? Number(r.memoryBytes) : r.cpuPercent;
  ordered.push(...[...byId.values()].sort((a,b)=>metric(b)-metric(a)||a.id.localeCompare(b.id)));
  const threshold=memory ? 50_000_000 : 2;
  for(let i=1;i<ordered.length;i++) for(let j=i;j>0;j--) {
    if(metric(ordered[j])-metric(ordered[j-1])<=threshold) break;
    [ordered[j-1],ordered[j]]=[ordered[j],ordered[j-1]];
  }
  return ordered;
}
/** Owner calls setActive for page/tab visibility, online presence and capability.
 * One request at a time, including while a hidden request is completing. Failures
 * stop live presentation after three attempts; visible pages keep retrying. */
export class MachineStatusController {
  state: MachinePollState = "idle";
  last?: MachineStatus;
  failures = 0;
  curveSinceMs = 0n;
  private active = false;
  private generation = 0;
  private busy = false;
  private timer?: ReturnType<typeof setTimeout>;
  constructor(private readonly fetch: () => Promise<MachineStatus>, private readonly changed: () => void = () => {}, private readonly intervalMs = 2000) {}
  setActive(visible: boolean, online: boolean, supported: boolean) {
    const active=visible&&online&&supported;
    if(visible && supported && !online) { this.state=this.last?"stale":"unavailable"; this.changed(); }
    if(active===this.active) return;
    this.active=active; this.generation++;
    clearTimeout(this.timer);
    if(active) void this.poll();
    else { this.changed(); }
  }
  retry() { clearTimeout(this.timer); if(this.active) void this.poll(); }
  dispose() { this.setActive(false,false,false); }
  private async poll() {
    if(!this.active||this.busy) return;
    const generation=this.generation; const started=Date.now(); this.busy=true;
    if(!this.last && this.state==="idle") this.state="loading";
    this.changed();
    try {
      const value=await this.fetch();
      if(generation!==this.generation) return;
      if(this.state==="stale"||this.state==="unavailable") this.curveSinceMs=value.observedMs;
      this.last=value;this.failures=0;this.state="live";
    } catch {
      if(generation!==this.generation) return;
      this.failures=Math.min(3,this.failures+1);
      if(!this.last) this.state="unavailable";
      if(this.failures===3) this.state=this.last?"stale":"unavailable";
    } finally {
      this.busy=false;
      if(this.active) this.timer=setTimeout(()=>void this.poll(),generation!==this.generation?0:Math.max(0,this.intervalMs-(Date.now()-started)));
      this.changed();
    }
  }
}
