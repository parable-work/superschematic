import { Identity } from "superscalar";
import { AutoGenerate, Presence, Workflow, key } from "@superschematic/db";

// A session a device opens for an agent (D104). Only the device's principal
// beats it, and it stays live three minutes after its last beat, and fifteen
// while its agent is in a turn, counted from the later of the beat and the
// turn's start. A session not beaten in time is missed: it moves to missing,
// and the leases its device holds on jobs are expired, but for one the device
// renewed since its last beat. Its next beat brings it back to idle.
export abstract class Session
  implements
    Presence<Session, {
      ttlMs: 180000;
      ttlByState: { turn: 900000 };
      principalField: "device";
      onMissed: { transition: "missing"; from: ["idle", "turn"] };
      onBeat: { transition: "idle"; from: ["missing"] };
      releaseLeases: ["Job"];
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  device: Identity.UUID;
  agent: string;
  status: Workflow<{
    states: ["idle", "turn", "missing", "closed"];
    transitions: [
      { from: "idle"; to: "turn" },
      { from: "turn"; to: "idle" },
      { from: "idle"; to: "missing" },
      { from: "turn"; to: "missing" },
      { from: "missing"; to: "idle" },
      { from: "idle"; to: "closed" },
      { from: "turn"; to: "closed" },
      { from: "missing"; to: "closed" }
    ];
  }>;
}
