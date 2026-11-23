import { NotifyKind } from "./gen/remoteagent/v1/relay_pb";
export function machineNotificationKinds(kinds: readonly NotifyKind[], advertised: boolean): NotifyKind[] {
  const result: NotifyKind[] = kinds.filter(k => k !== NotifyKind.MACHINE_DISK);
  if (advertised && !result.includes(NotifyKind.MACHINE_DISK)) result.push(NotifyKind.MACHINE_DISK);
  return result;
}
