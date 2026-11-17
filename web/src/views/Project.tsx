// 新任务 on a phone (design-system §6 新任务, client-v58 #new): the
// whole-page sheet that rises from the bottom, for a project. Its close is
// the way back; picking another project in the sentence opens that one's
// sheet in its place. A wide window has no such page: the main area is the
// new task.

import { create } from "@bufbuild/protobuf";
import { useEffect, useState } from "react";
import { useCoreHost, useProjectView, type Go } from "../core/host";
import { EventSchema, ProjectKeyActionSchema } from "../gen/remoteagent/core/v1/app_pb";
import type { Remote } from "../remote";
import { NewBox, NewFlow } from "./NewSession";
import { RemovedNotice } from "./Projects";

export function ProjectPage({
  remote,
  pkey,
  go,
  onBack,
}: {
  remote: Remote;
  pkey: string;
  go: Go;
  onBack: () => void;
}) {
  const host = useCoreHost(remote, go);
  const vm = useProjectView(host, pkey, false);
  const [settings] = useState(() => remote.demoFlag("project-settings"));
  const [picking, setPicking] = useState(false);
  // Opened from 新任务 or a project just picked: the keyboard is up.
  const [focus] = useState(() => remote.focusProject === pkey);
  useEffect(() => {
    if (remote.focusProject === pkey) remote.focusProject = "";
  }, [remote, pkey]);

  const hiddenNotice = vm.detail?.hiddenNotice;
  const notice = hiddenNotice?.active ? (
    <RemovedNotice
      notice={hiddenNotice}
      onReadd={() =>
        host.dispatch(
          create(EventSchema, {
            event: {
              case: "projectToggleHide",
              value: create(ProjectKeyActionSchema, { projectKey: pkey }),
            },
          }),
        )
      }
    />
  ) : undefined;

  return (
    <div className="page rise">
      <NewBox
        remote={remote}
        projectKey={pkey}
        go={go}
        autoFocus={focus}
        settings={settings}
        onClose={onBack}
        onPickProject={() => setPicking(true)}
        notice={notice}
      />
      {picking && (
        <NewFlow
          remote={remote}
          go={go}
          onClose={() => setPicking(false)}
          onPicked={(key) => {
            if (key === pkey) return;
            // Another project's sheet takes this one's place: closing it still goes back.
            remote.focusProject = key;
            location.replace("#/p/" + encodeURIComponent(key));
          }}
        />
      )}
    </div>
  );
}
