# Remove Proactive Project Initialization Offer

**Problem:** When Anvil is run for the first time in a folder, it shows a "Would you like to initialize this project?" prompt. This is almost always declined, adding friction to startup without providing value.

**Goal:** Remove the proactive initialization offer and all associated tracking infrastructure. Initialization remains available on-demand via the command palette (`ctrl+p` → "Initialize Project").

**Scope:**

In scope:
- Remove the `uiInitialize` UI state and all code that enters it
- Remove the init-flag tracking system (`ProjectNeedsInitialization`, `MarkProjectInitialized`, `ProjectInitFlag`, `InitFlagFilename` constant, the `init` flag file)
- Remove helper functions that only serve the tracking: `contextPathsExist`, `dirHasNoVisibleFiles`
- Remove `HasInitialDataConfig` (zero callers)
- Remove the tracking plumbing from workspace interface, backend, server (HTTP routes + route registrations in `server.go`), and client
- Remove server route registrations: `needs-init` (`server.go:157`) and `project/init` (`server.go:158`); keep `init-prompt` (`server.go:159`)
- Remove `proto.ProjectNeedsInitResponse` (`proto/requests.go:50-53`); keep `ProjectInitPromptResponse`
- Remove `Initialize` keybindings (`keys.go`)
- Remove `Initialize` styles (`styles.go`, `quickstyle.go`)
- Remove `initializeView()`, `updateInitializeView()`, `skipInitializeProject()`, `markProjectInitializedCmd()`
- Remove the entire `onboarding` struct and field on `UI` (`ui.go:283-286`) — it only has `yesInitializeSelected`
- Simplify `initializeProject()` to just call `InitializePrompt()` and send the result — no new session creation
- Regenerate swagger docs after route removal

Out of scope:
- The command palette "Initialize Project" entry stays
- `InitializePrompt()` and its implementations stay (workspace, backend, server handler, client)
- `InitializeAs` config option stays (used by the init prompt builder)
- `config.Init()` function stays (it loads config, unrelated to project init)
- Existing `init` flag files on user machines become harmless orphans — no cleanup needed

**Constraints:** None — this is pure removal/simplification.

**Success Criteria:**
- [ ] Anvil launches directly to `uiLanding` for configured projects (no init prompt)
- [ ] Command palette "Initialize Project" sends the init prompt into the current session
- [ ] No dead code remains: `ProjectNeedsInitialization`, `MarkProjectInitialized`, `HasInitialDataConfig`, `contextPathsExist`, `dirHasNoVisibleFiles`, `ProjectInitFlag`, `InitFlagFilename`, init flag file, `uiInitialize` state, Initialize keybindings/styles, `onboarding` struct, `ProjectNeedsInitResponse` proto type are all gone
- [ ] `ProjectNeedsInitialization`/`MarkProjectInitialized` removed from workspace interface, all implementations, backend, server (handlers + route registrations), and client
- [ ] Project compiles and existing tests pass

**Design Decisions:**
- Full removal over shallow removal (just skipping the state) — eliminates dead code and tracking infrastructure
- `initializeProject()` no longer creates a new session — user may have useful context in the current session that helps initialization
- Keeping `InitializePrompt()` — still needed for command-palette-triggered init

**Context Files:**
- `internal/ui/model/onboarding.go` — proactive init UI (removing most of this)
- `internal/ui/model/ui.go:449-455` — state selection logic that enters `uiInitialize`
- `internal/ui/model/ui.go:283-286` — `onboarding` struct (removing entirely)
- `internal/ui/model/ui.go:2091-2096` — command palette handler (keeping, simplifying)
- `internal/ui/model/keys.go` — `Initialize` keybindings
- `internal/ui/styles/styles.go` — `Initialize` style struct
- `internal/ui/styles/quickstyle.go` — `Initialize` style values
- `internal/config/init.go` — `ProjectNeedsInitialization`, `MarkProjectInitialized`, `ProjectInitFlag`, `HasInitialDataConfig`, `contextPathsExist`, `dirHasNoVisibleFiles`
- `internal/workspace/workspace.go:132-133` — interface methods
- `internal/workspace/app_workspace.go:320-325` — app implementations
- `internal/workspace/client_workspace.go:464-469` — client implementations
- `internal/backend/config.go:91-107` — backend implementations
- `internal/server/config.go:214-240` — HTTP route handlers
- `internal/server/server.go:157-158` — route registrations (keep line 159)
- `internal/client/config.go:134-156` — client methods
- `internal/proto/requests.go:50-53` — `ProjectNeedsInitResponse` type
- `internal/swagger/` — generated swagger docs (regenerate after changes)
- `internal/ui/dialog/commands.go:527` — command palette entry (keeping)
- `internal/ui/dialog/actions.go:56` — `ActionInitializeProject` (keeping)
