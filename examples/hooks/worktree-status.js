// worktree-status.js: plugin do opencode que informa o estado da sessão ao
// worktree-app (painel de worktrees). Só chama o binário quando o estado muda;
// qualquer erro é engolido para nunca atrapalhar o agente.
import os from "node:os"
import path from "node:path"

const BIN = process.env.WT_HOOK_BIN || path.join(os.homedir(), ".local", "bin", "wt-street-linux")

const last = new Map() // sessionID -> estado enviado

function stateOf(event) {
  const p = event?.properties ?? {}
  switch (event?.type) {
    case "session.status":
      if (p.status?.type === "busy" || p.status?.type === "retry") return ["working", p.sessionID]
      if (p.status?.type === "idle") return ["done", p.sessionID]
      return []
    case "session.idle":
      return ["done", p.sessionID]
    case "permission.asked":
    case "permission.updated":
    case "question.asked":
      return ["waiting", p.sessionID]
    case "permission.replied":
    case "question.replied":
      return ["working", p.sessionID]
    case "session.deleted":
      return ["ended", p.info?.id ?? p.sessionID]
    default:
      return []
  }
}

export const WorktreeStatus = async ({ directory }) => ({
  event: async ({ event }) => {
    try {
      const [state, sessionID] = stateOf(event)
      if (!state || !sessionID || last.get(sessionID) === state) return
      last.set(sessionID, state)
      const proc = Bun.spawn([BIN, "hook", "--agent", "opencode"], {
        stdin: new Blob([JSON.stringify({ session_id: sessionID, cwd: directory || process.cwd(), state })]),
        stdout: "ignore",
        stderr: "ignore",
      })
      await proc.exited
    } catch {
      /* nunca derrubar o agente */
    }
  },
})
