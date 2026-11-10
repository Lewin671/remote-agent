// 新任务 on a phone (design-system §6 新任务, client-v58 #new): the
// whole-page sheet that rises from the bottom, for a project. Its close is
// the way back; picking another project in the sentence opens that one's
// sheet in its place. A wide window has no such page: the main area is the
// new task.

import { useEffect, useState } from "react";
import { projectName } from "../model";
import type { Remote } from "../remote";
import { useRemote } from "../ui";
import { NewBox, NewFlow, useNewSession, type Go } from "./NewSession";
import { RemovedNotice } from "./Projects";

export function ProjectPage({ remote, pkey, go, onBack }: { remote: Remote; pkey: string; go: Go; onBack: () => void }) {
  useRemote(remote);
  const ns = useNewSession(remote, pkey, go);
  const name = ns.p?.name ?? projectName(pkey);
  const [settings] = useState(() => remote.demoFlag("project-settings"));
  const [picking, setPicking] = useState(false);
  // Opened from 新任务 or a project just picked: the keyboard is up.
  const [focus] = useState(() => remote.focusProject === pkey);
  useEffect(() => {
    if (remote.focusProject === pkey) remote.focusProject = "";
  }, [remote, pkey]);
  return (
    <div className="page rise">
      <NewBox
        ns={ns}
        name={name}
        autoFocus={focus}
        settings={settings}
        onClose={onBack}
        onPickProject={() => setPicking(true)}
        notice={<RemovedNotice remote={remote} pkey={pkey} />}
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
