import { t, localeStore, type Locale } from "./i18n";
import type { NotifyContent } from "./gen/remoteagent/v1/remote_pb";
import { NotifyKind } from "./gen/remoteagent/v1/relay_pb";
import { ResourcePressure } from "./gen/remoteagent/v1/local_pb";
export function machineNotificationKinds(kinds: readonly NotifyKind[], advertised: boolean): NotifyKind[] {
  const result: NotifyKind[] = kinds.filter(k => k !== NotifyKind.MACHINE_DISK);
  if (advertised && !result.includes(NotifyKind.MACHINE_DISK)) result.push(NotifyKind.MACHINE_DISK);
  return result;
}
export interface MachineDiskNotice { machineId: string; tag: string; title: string; body: string }
export function machineDiskNotice(c: NotifyContent, locale: Locale = localeStore.snapshot()): MachineDiskNotice | undefined {
  const disk = c.diskStatus;
  if(c.kind !== NotifyKind.MACHINE_DISK || c.sessionId || !c.machineId || !c.machineName.trim() || c.title || c.text || c.approval || !disk) return;
  const critical = disk.pressure === ResourcePressure.CRITICAL;
  if(disk.availableBytes < 0n) return;
  if(!critical && disk.pressure !== ResourcePressure.WARNING || disk.availableBytes >= (critical ? 2_000_000_000n : 10_000_000_000n)) return;
  const number = new Intl.NumberFormat(locale, { minimumFractionDigits: 1, maximumFractionDigits: 1 }).format(Number(disk.availableBytes)/1e9);
  const size = t("machine_status.size_gb", {size:number}, locale);
  return { machineId:c.machineId, tag:`${c.machineId}/NOTIFY_KIND_MACHINE_DISK`,
    title: critical ? t("machine_status.notify_critical",{computer:c.machineName,size},locale) : t("machine_status.notify_warning",{computer:c.machineName,size},locale),
    body: critical ? t("machine_status.notify_critical_body",{},locale) : t("machine_status.notify_warning_body",{},locale) };
}
