import { t } from "../i18n";
// Everything that lists or manages projects (client-v34): one row and one
// list for the scope menu, the project picker and 项目管理, and the two
// things done to a project there, pinning and removing.

import { create } from "@bufbuild/protobuf";
import { useState, type ReactNode } from "react";
import { useCoreHost, useProjectView, type Go } from "../core/host";
import { renderText } from "../core/text";
import {
  EventSchema,
  HomeSetScopeActionSchema,
  ProjectAddActionSchema,
  ProjectKeyActionSchema,
  ProjectSetSearchActionSchema,
  DirNavigateActionSchema,
  DirPickerOpenActionSchema,
} from "../gen/remoteagent/core/v1/app_pb";
import type { ProjectListingView, ProjectRowView } from "../gen/remoteagent/core/v1/home_view_pb";
import type {
  DirPickerView,
  HiddenProjectNoticeView,
  ManageProjectsView,
} from "../gen/remoteagent/core/v1/project_view_pb";
import { TextSchema } from "../gen/remoteagent/core/v1/text_pb";
import type { Remote } from "../remote";
import { ActionSheet, Alert, Avatar, Banner, NavBar, PathText, Segmented, Sheet, StateMark, useNow } from "../ui";
import { Ellipsis, LoaderCircle, Plus, Search, X } from "../icons";
import { DirBrowserView } from "./DirBrowser";

/** What the list's sync is doing on Home banners: saving, failed, or changed elsewhere. */
export function PrefsBanner({ remote, quiet }: { remote: Remote; quiet?: boolean }) {
  const now = useNow(1000);
  const p = remote.prefs;
  if (p.failed)
    return (
      <Banner kind="err" action={t("common.retry")} onAction={() => remote.retryPrefs()}>
        {t("projects.save_failed", { reason: p.failed.error })}
      </Banner>
    );
  if (p.pending.size > 0) return <Banner><LoaderCircle /> {t("projects.syncing")}</Banner>;
  if (quiet) return null;
  if (p.status === "error" && !p.snap)
    return (
      <Banner kind="err" action={t("common.retry")} onAction={() => void remote.retryPrefs()}>
        {t("projects.load_failed")}
      </Banner>
    );
  if (p.changedAt && now - p.changedAt < 8000) return <Banner>{t("projects.changed_elsewhere")}</Banner>;
  return null;
}

/** One project row rendered directly from Core's ProjectRowView. */
export function ProjectItem({
  p,
  onClick,
  end,
  onMore,
  checked,
  label,
}: {
  p: ProjectRowView;
  onClick: () => void;
  end?: ReactNode;
  onMore?: () => void;
  checked?: boolean;
  label?: string;
}) {
  const name = renderText(p.name) || t("projects.unknown");
  const sub = renderText(p.subtitle);
  const text = (
    <>
      <Avatar name={name} size="sm" />
      <div>
        <b>{name}</b>
        <small>
          <PathText path={p.shortPath} />
          {sub ? ` · ${sub}` : ""}
        </small>
      </div>
    </>
  );
  if (!onMore)
    return (
      <button
        className="pi"
        role={checked === undefined ? undefined : "menuitemradio"}
        aria-checked={checked}
        aria-label={label}
        onClick={onClick}
      >
        {text}
        {end}
      </button>
    );
  return (
    <div className={"pi split" + (p.syncing ? " busy" : "")}>
      <button className="pm" onClick={onClick} aria-label={label}>
        {text}
      </button>
      <button
        className="dots"
        onClick={onMore}
        disabled={p.syncing}
        aria-label={t("projects.manage_named", { name })}
      >
        {p.syncing ? <LoaderCircle /> : <Ellipsis />}
      </button>
    </div>
  );
}

/** The projects list rendered from Core's ProjectListingView. */
export function ProjectList({
  listing,
  onSearch,
  item,
  first,
  onNew,
  children,
}: {
  listing: ProjectListingView;
  onSearch: (query: string) => void;
  item: (p: ProjectRowView) => ReactNode;
  first?: ReactNode;
  onNew: () => void;
  children?: ReactNode;
}) {
  return (
    <>
      {(listing.canSearch || listing.searching) && (
        <label className="find">
          <Search />
          <input
            aria-label={t("projects.search")}
            placeholder={t("projects.search")}
            value={listing.searchQuery}
            onChange={(e) => onSearch(e.target.value)}
            onKeyDown={(e) => e.key === "Escape" && onSearch("")}
          />
          {listing.searchQuery && (
            <button className="x" aria-label={t("common.clear_search")} onClick={() => onSearch("")}>
              <X />
            </button>
          )}
        </label>
      )}
      <div className="plist" role={first ? "menu" : undefined}>
        {!listing.searching && first && <div className="grp">{first}</div>}
        {listing.titled && <div className="fl">{t("projects.pinned")}</div>}
        {listing.pinned.length > 0 && <div className="grp">{listing.pinned.map(item)}</div>}
        {listing.titled && <div className="fl">{t("projects.others")}</div>}
        {listing.others.length > 0 && <div className="grp">{listing.others.map(item)}</div>}
        {listing.emptyMatch && <div className="hint">{renderText(listing.emptyMatch)}</div>}
        <div className="grp">
          <button className="pi add" onClick={onNew}>
            <Plus /> {t("projects.new")}
          </button>
          {children}
        </div>
      </div>
    </>
  );
}

/** Confirmation alert before hiding a project from the list. */
export function RemoveProjectAlert({
  name,
  onOk,
  onCancel,
}: {
  name: string;
  onOk: () => void;
  onCancel: () => void;
}) {
  return (
    <Alert
      title={t("projects.remove_title", { name })}
      text={t("projects.remove_explanation")}
      ok={t("common.remove")}
      danger
      onCancel={onCancel}
      onOk={() => {
        onCancel();
        onOk();
      }}
    />
  );
}

/** Core-driven directory picker sheet for adding a project. */
export function DirPickerSheet({
  picker,
  onNavigate,
  onConfirm,
  onBack,
  onClose,
}: {
  picker: DirPickerView;
  onNavigate: (machineId: string, path: string) => void;
  onConfirm: (path: string) => void;
  onBack?: () => void;
  onClose: () => void;
}) {
  const crumbs = picker.crumbs.map((c) => ({ label: renderText(c.label), path: c.path }));
  const last = crumbs[crumbs.length - 1]?.label || "/";
  const confirmLabel = renderText(picker.confirmLabel) || t("projects.use_directory", { name: last });
  return (
    <Sheet onClose={onClose} title={t("projects.new_choose_directory")} back={onBack}>
      {picker.machines.length > 1 && (
        <Segmented
          className="full machines"
          label={t("common.machine")}
          value={picker.machineId || undefined}
          onChange={(id) => onNavigate(id, "")}
          options={picker.machines.map((x) => ({
            value: x.machineId,
            disabled: !x.online,
            label: (
              <>
                <StateMark kind={x.online ? "online" : "off"} />
                {x.name}
              </>
            ),
          }))}
        />
      )}
      {!picker.machineId ? (
        <div className="hint">{t("projects.no_online_computers")}</div>
      ) : (
        <DirBrowserView
          crumbs={crumbs}
          parentPath={picker.parentPath}
          dirs={picker.dirs}
          loading={picker.loading}
          error={renderText(picker.error)}
          confirmLabel={confirmLabel}
          canConfirm={picker.canConfirm}
          onOpenDir={(path) => onNavigate(picker.machineId, path)}
          onRetry={() => onNavigate(picker.machineId, picker.currentPath)}
          onConfirm={() => onConfirm(picker.currentPath)}
        />
      )}
    </Sheet>
  );
}

/** Pure ViewModel renderer for 项目管理; used by ManagePage and golden UI tests. */
export function ManageProjectsScreen({
  vm,
  onSearch,
  onTogglePin,
  onToggleHide,
  onOpenDirPicker,
  onDirNavigate,
  onCloseDirPicker,
  onAddProject,
  onSelectProject,
  onRetryPrefs,
  onBack,
}: {
  vm: ManageProjectsView;
  onSearch: (query: string) => void;
  onTogglePin: (key: string) => void;
  onToggleHide: (key: string) => void;
  onOpenDirPicker: () => void;
  onDirNavigate: (machineId: string, path: string) => void;
  onCloseDirPicker: () => void;
  onAddProject: (path: string) => void;
  onSelectProject: (key: string) => void;
  onRetryPrefs?: () => void;
  onBack: () => void;
}) {
  const [menuKey, setMenuKey] = useState("");
  const [removingKey, setRemovingKey] = useState("");
  const listing = vm.listing;
  const allRows = listing ? [...listing.pinned, ...listing.others] : [];
  const menu = menuKey ? allRows.find((r) => r.key === menuKey) : undefined;
  const removing = removingKey ? allRows.find((r) => r.key === removingKey) : undefined;
  const blocked = !vm.canEdit ? renderText(vm.blockedReason) || t("projects.connect_to_edit") : "";
  return (
    <div className="page set">
      <NavBar onBack={onBack} title={t("projects.manage")} />
      <div className="body">
        <div className="manage">
          {vm.saveError && (
            <Banner kind="err" action={onRetryPrefs ? t("common.retry") : undefined} onAction={onRetryPrefs}>
              {renderText(vm.saveError)}
            </Banner>
          )}
          {vm.syncing && (
            <Banner>
              <LoaderCircle /> {t("projects.syncing")}
            </Banner>
          )}
          {listing?.loadFailed && (
            <Banner kind="err" action={onRetryPrefs ? t("common.retry") : undefined} onAction={onRetryPrefs}>
              {t("projects.load_failed")}
            </Banner>
          )}
          {vm.changedElsewhere && <Banner>{t("projects.changed_elsewhere")}</Banner>}
          {listing && (
            <ProjectList
              listing={listing}
              onSearch={onSearch}
              onNew={onOpenDirPicker}
              item={(p) => {
                const name = renderText(p.name) || t("projects.unknown");
                return (
                  <ProjectItem
                    key={p.key}
                    p={p}
                    label={t("projects.view_sessions", { name })}
                    onClick={() => onSelectProject(p.key)}
                    onMore={() => setMenuKey(p.key)}
                  />
                );
              }}
            />
          )}
          <div className="gf">{t("projects.remove_note")}</div>
        </div>
      </div>
      {menu && (
        <ActionSheet
          info={
            <>
              <b>{renderText(menu.name)}</b>
              <span className="mono">
                <PathText path={menu.shortPath} />
              </span>
            </>
          }
          actions={[
            {
              label: menu.pinned ? t("projects.unpin") : t("projects.pin"),
              onClick: () => onTogglePin(menu.key),
              disabled: !!blocked,
              note: blocked || undefined,
            },
            {
              label: t("projects.remove_action"),
              red: true,
              onClick: () => setRemovingKey(menu.key),
              disabled: !!blocked,
              group: 1,
            },
          ]}
          onClose={() => setMenuKey("")}
        />
      )}
      {removing && (
        <RemoveProjectAlert
          name={renderText(removing.name) || t("projects.unknown")}
          onOk={() => onToggleHide(removing.key)}
          onCancel={() => setRemovingKey("")}
        />
      )}
      {vm.dirPicker?.open && (
        <DirPickerSheet
          picker={vm.dirPicker}
          onNavigate={onDirNavigate}
          onConfirm={onAddProject}
          onClose={onCloseDirPicker}
        />
      )}
    </div>
  );
}

/** 项目管理: connected to CoreHost. */
export function ManagePage({ remote, go, onBack }: { remote: Remote; go: Go; onBack: () => void }) {
  const host = useCoreHost(remote, go);
  const vm = useProjectView(host, "", true);
  const manage = vm.manage;
  if (!manage) return null;
  const effectiveManage =
    remote.conn !== "online" && manage.canEdit
      ? {
          ...manage,
          canEdit: false,
          blockedReason: manage.blockedReason ?? create(TextSchema, { literal: t("projects.connect_to_edit") }),
        }
      : manage;
  return (
    <ManageProjectsScreen
      vm={effectiveManage}
      onSearch={(query) =>
        host.dispatch(
          create(EventSchema, {
            event: { case: "projectSetSearch", value: create(ProjectSetSearchActionSchema, { query }) },
          }),
        )
      }
      onTogglePin={(projectKey) =>
        host.dispatch(
          create(EventSchema, {
            event: { case: "projectTogglePin", value: create(ProjectKeyActionSchema, { projectKey }) },
          }),
        )
      }
      onToggleHide={(projectKey) =>
        host.dispatch(
          create(EventSchema, {
            event: { case: "projectToggleHide", value: create(ProjectKeyActionSchema, { projectKey }) },
          }),
        )
      }
      onOpenDirPicker={() =>
        host.dispatch(
          create(EventSchema, {
            event: {
              case: "projectOpenDirPicker",
              value: create(DirPickerOpenActionSchema, { forNewTask: false }),
            },
          }),
        )
      }
      onDirNavigate={(machineId, path) =>
        host.dispatch(
          create(EventSchema, {
            event: {
              case: "projectDirNavigate",
              value: create(DirNavigateActionSchema, { machineId, path }),
            },
          }),
        )
      }
      onCloseDirPicker={() =>
        host.dispatch(
          create(EventSchema, { event: { case: "projectCloseDirPicker", value: {} } }),
        )
      }
      onAddProject={(path) => {
        if (remote.projectPrefs().hidden.some((h) => h.path === path)) {
          host.dispatch(
            create(EventSchema, {
              event: { case: "projectToggleHide", value: create(ProjectKeyActionSchema, { projectKey: path }) },
            }),
          );
        }
        host.dispatch(
          create(EventSchema, {
            event: { case: "projectAdd", value: create(ProjectAddActionSchema, { path }) },
          }),
        );
      }}
      onSelectProject={(scopeKey) => {
        host.dispatch(
          create(EventSchema, {
            event: { case: "homeSetScope", value: create(HomeSetScopeActionSchema, { scopeKey }) },
          }),
        );
        go("");
      }}
      onRetryPrefs={() => remote.retryPrefs()}
      onBack={onBack}
    />
  );
}

/** On the page of a project that is off the list: one tap lists it again. */
export function RemovedNotice({
  notice,
  onReadd,
}: {
  notice?: HiddenProjectNoticeView;
  onReadd: () => void;
}) {
  if (!notice?.active) return null;
  return (
    <>
      <Banner
        action={notice.busy ? t("projects.adding") : t("projects.add_again")}
        onAction={notice.actionEnabled && !notice.busy ? onReadd : undefined}
      >
        {renderText(notice.text) || t("projects.removed")}
      </Banner>
      {notice.error && <Banner kind="err">{renderText(notice.error)}</Banner>}
    </>
  );
}
