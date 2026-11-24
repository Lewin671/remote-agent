import { t } from "../i18n";
// Everything that lists or manages projects (client-v34): one row and one
// list for the scope menu, the project picker and 项目管理.

import { useState, type ReactNode } from "react";
import { useCoreHost, useProjectView, useSyncState, type Go } from "../core/host";
import { renderText } from "../core/text";
import type { ProjectListingView, ProjectRowView } from "../gen/remoteagent/core/v1/home_view_pb";
import type { DirPickerView, HiddenProjectNoticeView, ManageProjectsView } from "../gen/remoteagent/core/v1/project_view_pb";
import type { Remote } from "../remote";
import { ActionSheet, Alert, Avatar, Banner, NavBar, PathText, Segmented, Sheet, StateMark } from "../ui";
import { Ellipsis, Laptop, LoaderCircle, Plus, Search, X } from "../icons";
import { DirBrowserView } from "./DirBrowser";

/** What the list's sync is doing on Home banners: saving or load failed. */
export function PrefsBanner({ remote, quiet }: { remote: Remote; quiet?: boolean }) {
  const host = useCoreHost(remote);
  const sync = useSyncState(host);
  if (sync.prefsSyncing) return <Banner><LoaderCircle /> {t("projects.syncing")}</Banner>;
  if (quiet) return null;
  if ((sync.prefsLoadFailed || sync.prefsBroken) && !sync.prefs) {
    return <Banner kind="err" action={t("common.retry")} onAction={() => host.retryPrefs()}>{t("projects.load_failed")}</Banner>;
  }
  return null;
}

/** One project row rendered directly from Core's ProjectRowView. */
export function ProjectItem({ p, onClick, end, onMore, checked, label }: { p: ProjectRowView; onClick: () => void; end?: ReactNode; onMore?: () => void; checked?: boolean; label?: string }) {
  const name = renderText(p.name) || t("projects.unknown");
  const sub = renderText(p.subtitle);
  const text = (
    <>
      <Avatar name={name} size="sm" />
      <div><b>{name}</b><small><PathText path={p.shortPath} />{sub ? ` · ${sub}` : ""}</small></div>
    </>
  );
  if (!onMore) {
    return <button className="pi" role={checked === undefined ? undefined : "menuitemradio"} aria-checked={checked} aria-label={label} onClick={onClick}>{text}{end}</button>;
  }
  return (
    <div className={"pi split" + (p.syncing ? " busy" : "")}>
      <button className="pm" onClick={onClick} aria-label={label}>{text}</button>
      <button className="dots" onClick={onMore} disabled={p.syncing} aria-label={t("projects.manage_named", { name })}>{p.syncing ? <LoaderCircle /> : <Ellipsis />}</button>
    </div>
  );
}

/** The projects list rendered from Core's ProjectListingView. */
export function ProjectList({ listing, onSearch, item, first, onNew, children }: { listing: ProjectListingView; onSearch: (query: string) => void; item: (p: ProjectRowView) => ReactNode; first?: ReactNode; onNew: () => void; children?: ReactNode }) {
  return (
    <>
      {(listing.canSearch || listing.searching) && (
        <label className="find">
          <Search />
          <input aria-label={t("projects.search")} placeholder={t("projects.search")} value={listing.searchQuery} onChange={(e) => onSearch(e.target.value)} onKeyDown={(e) => e.key === "Escape" && onSearch("")} />
          {listing.searchQuery && <button className="x" aria-label={t("common.clear_search")} onClick={() => onSearch("")}><X /></button>}
        </label>
      )}
      <div className="plist" role={first ? "menu" : undefined}>
        {!listing.searching && first && <div className="grp">{first}</div>}
        {listing.titled && <div className="fl">{t("projects.pinned")}</div>}
        {listing.pinned.length > 0 && <div className="grp">{listing.pinned.map(item)}</div>}
        {listing.titled && <div className="fl">{t("projects.others")}</div>}
        {listing.others.length > 0 && <div className="grp">{listing.others.map(item)}</div>}
        {listing.emptyMatch && <div className="hint">{renderText(listing.emptyMatch)}</div>}
        <div className="grp"><button className="pi add" onClick={onNew}><Plus /> {t("projects.new")}</button>{children}</div>
      </div>
    </>
  );
}

/** Confirmation alert before hiding a project from the list. */
export function RemoveProjectAlert({ name, onOk, onCancel }: { name: string; onOk: () => void; onCancel: () => void }) {
  return <Alert title={t("projects.remove_title", { name })} text={t("projects.remove_explanation")} ok={t("common.remove")} danger onCancel={onCancel} onOk={() => { onCancel(); onOk(); }} />;
}

/** Core-driven directory picker sheet for adding a project. */
export function DirPickerSheet({ picker, fixedMachine, title, onNavigate, onConfirm, onBack, onClose }: { picker: DirPickerView; fixedMachine?: string; title?: string; onNavigate: (machineId: string, path: string) => void; onConfirm: (path: string) => void; onBack?: () => void; onClose: () => void }) {
  const [initialPath] = useState(() => `${picker.machineId}:${picker.currentPath}`);
  const dirty = picker.canConfirm && `${picker.machineId}:${picker.currentPath}` !== initialPath;
  const crumbs = picker.crumbs.map((c) => ({ label: renderText(c.label), path: c.path }));
  const confirmLabel = renderText(picker.confirmLabel) || t("projects.use_directory", { name: crumbs[crumbs.length - 1]?.label || "/" });
  const fixedEntry = fixedMachine ? picker.machines.find((x) => { return x.machineId === fixedMachine; }) : undefined;
  return (
    <Sheet onClose={onClose} dirty={dirty} title={title ?? t("projects.new_choose_directory")} back={onBack}>
      {fixedEntry ? (
        <div className="seg machines"><button type="button" className="on" disabled>{fixedEntry.name}</button></div>
      ) : !fixedMachine && picker.machines.length > 1 && (
        <Segmented className="full machines" label={t("common.machine")} value={picker.machineId || undefined} onChange={(id) => onNavigate(id, "")} options={picker.machines.map((x) => ({ value: x.machineId, disabled: !x.online, label: <><StateMark kind={x.online ? "online" : "off"} />{x.name}</> }))} />
      )}
      {!picker.machineId ? <div className="hint">{t("projects.no_online_computers")}</div> : (
        <DirBrowserView crumbs={crumbs} parentPath={picker.parentPath} dirs={picker.dirs} loading={picker.loading} error={renderText(picker.error)} confirmLabel={confirmLabel} canConfirm={picker.canConfirm} onOpenDir={(path) => onNavigate(picker.machineId, path)} onRetry={() => onNavigate(picker.machineId, picker.currentPath)} onConfirm={() => onConfirm(picker.currentPath)} />
      )}
    </Sheet>
  );
}

/** Pure ViewModel renderer for 项目管理; used by ManagePage and golden UI tests. */
export function ManageProjectsScreen(p: { vm: ManageProjectsView; onSearch: (query: string) => void; onTogglePin: (key: string) => void; onToggleHide: (key: string) => void; onOpenDirPicker: () => void; onDirNavigate: (machineId: string, path: string) => void; onCloseDirPicker: () => void; onAddProject: (path: string) => void; onSelectProject: (key: string) => void; onRetryPrefs?: () => void; onResetFilter?: () => void; onBack: () => void }) {
  const { vm } = p;
  const [menuKey, setMenuKey] = useState("");
  const [removingKey, setRemovingKey] = useState("");
  const allRows = vm.listing ? [...vm.listing.pinned, ...vm.listing.others] : [];
  const menu = menuKey ? allRows.find((r) => r.key === menuKey) : undefined;
  const removing = removingKey ? allRows.find((r) => r.key === removingKey) : undefined;
  const blocked = !vm.canEdit ? renderText(vm.blockedReason) || t("projects.connect_to_edit") : "";
  const filterName = vm.filterMachineName || vm.filterMachineId;
  return (
    <div className="page set">
      <NavBar onBack={p.onBack} title={t("projects.manage")} />
      <div className="body">
        <div className="manage">
          {vm.filterMachineId && (
            <div className="proj-filter-bar">
              <span className="proj-filter-pill">
                <Laptop />
                <span>{t("projects.filter_machine_subtitle", { name: filterName })}</span>
              </span>
              {p.onResetFilter && (
                <button type="button" className="link" onClick={p.onResetFilter}>
                  {t("projects.filter_show_all")}
                </button>
              )}
            </div>
          )}
          {vm.saveError && <Banner kind="err" action={p.onRetryPrefs ? t("common.retry") : undefined} onAction={p.onRetryPrefs}>{renderText(vm.saveError)}</Banner>}
          {vm.syncing && <Banner><LoaderCircle /> {t("projects.syncing")}</Banner>}
          {vm.listing?.loadFailed && <Banner kind="err" action={p.onRetryPrefs ? t("common.retry") : undefined} onAction={p.onRetryPrefs}>{t("projects.load_failed")}</Banner>}
          {vm.changedElsewhere && <Banner>{t("projects.changed_elsewhere")}</Banner>}
          {vm.listing && (
            <ProjectList
              listing={vm.listing}
              onSearch={p.onSearch}
              onNew={p.onOpenDirPicker}
              item={(r) => <ProjectItem key={r.key} p={r} label={t("projects.view_sessions", { name: renderText(r.name) || t("projects.unknown") })} onClick={() => p.onSelectProject(r.key)} onMore={() => setMenuKey(r.key)} />}
            />
          )}
          <div className="gf">{t("projects.remove_note")}</div>
        </div>
      </div>
      {menu && (
        <ActionSheet
          info={<><b>{renderText(menu.name)}</b><span className="mono"><PathText path={menu.shortPath} /></span></>}
          actions={[
            { label: menu.pinned ? t("projects.unpin") : t("projects.pin"), onClick: () => p.onTogglePin(menu.key), disabled: !!blocked, note: blocked || undefined },
            { label: t("projects.remove_action"), red: true, onClick: () => setRemovingKey(menu.key), disabled: !!blocked, group: 1 },
          ]}
          onClose={() => setMenuKey("")}
        />
      )}
      {removing && <RemoveProjectAlert name={renderText(removing.name) || t("projects.unknown")} onOk={() => p.onToggleHide(removing.key)} onCancel={() => setRemovingKey("")} />}
      {vm.dirPicker?.open && <DirPickerSheet picker={vm.dirPicker} onNavigate={p.onDirNavigate} onConfirm={p.onAddProject} onClose={p.onCloseDirPicker} />}
    </div>
  );
}

const MANAGE_PICKER_KEY = "manage";

/** 项目管理: connected to CoreHost. */
export function ManagePage({ remote, machineId = "", go, onBack }: { remote: Remote; machineId?: string; go: Go; onBack: () => void }) {
  const host = useCoreHost(remote, go);
  const manage = useProjectView(host, "", true, 30, machineId, MANAGE_PICKER_KEY).manage;
  if (!manage) return null;
  return (
    <ManageProjectsScreen
      vm={manage}
      onSearch={host.projectSetSearch}
      onTogglePin={host.projectTogglePin}
      onToggleHide={host.projectToggleHide}
      onOpenDirPicker={() => host.projectOpenDirPicker(false, "", "", MANAGE_PICKER_KEY)}
      onDirNavigate={(mid, path) => host.projectDirNavigate(mid, path, MANAGE_PICKER_KEY)}
      onCloseDirPicker={() => host.projectCloseDirPicker(MANAGE_PICKER_KEY)}
      onAddProject={(path) => host.projectAdd(path, MANAGE_PICKER_KEY)}
      onSelectProject={(scopeKey) => { host.homeSetScope(scopeKey); go(""); }}
      onRetryPrefs={() => host.retryPrefs()}
      onResetFilter={machineId ? () => go("projects", true) : undefined}
      onBack={onBack}
    />
  );
}

/** On the page of a project that is off the list: one tap lists it again. */
export function RemovedNotice({ notice, onReadd }: { notice?: HiddenProjectNoticeView; onReadd: () => void }) {
  if (!notice?.active) return null;
  return (
    <>
      <Banner action={notice.busy ? t("projects.adding") : t("projects.add_again")} onAction={notice.actionEnabled && !notice.busy ? onReadd : undefined}>
        {renderText(notice.text) || t("projects.removed")}
      </Banner>
      {notice.error && <Banner kind="err">{renderText(notice.error)}</Banner>}
    </>
  );
}
