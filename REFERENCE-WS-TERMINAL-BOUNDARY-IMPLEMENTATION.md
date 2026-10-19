# WS Terminal Delivery And Local Round Completion

Decision before source, 2026-10-19. The independent background-null correction
f8a139f passed complete local checks but automatic push39212734118 and
PR39212774462 failed existing native WS immediate-next-round tests with busy
instead of continuation handling. Both container delivery jobs passed. Their
raw failed logs are retained outside Git. No failed head is accepted or merged.

1. L3 responsesws client owns one downstream reader and sequential worker; L2
compat/responsesws classifies native terminal frames; main injects that narrow
presentation fact. Scope is the interval after a terminal frame becomes public
but before Session.Execute finishes its host cleanup. Actual generation, gates,
continuation checks and cleanup remain with the existing native session.
2. Retain existing frozen responsesws/v1 and execution/v2 declarations exactly.
Use a new implementation constructor accepting a pure terminal-frame predicate,
with L2 classification injected by main. Do not parse native wire in L3 or add
context hooks, implementation imports, sleep windows or a second execution path.
The existing constructor remains for consumers without terminal awareness.
3. A terminal-delivery boundary may reserve at most one bounded next input frame
while current local cleanup completes. No next Execute starts before the prior
Execute returns. Genuine overlapping generation before terminal delivery remains
busy; additional frames while the one boundary slot is occupied remain busy.
Mark the boundary before the terminal Write because a peer can read/respond
before the server Write call returns; failed delivery/cleanup discards the slot
and closes/fails through existing behavior. CAS transitions must not erase a
concurrently accepted frame. No automatic request retry, wire duplication or
upstream overlap is introduced.
4. Keep the existing queue/frame bounds, single reader, sequential worker,
current authorization/model/target checks, disabled billing, idle/deadline,
publication leases, cancellation and resource-join behavior. No L0, schema,
public route or paid capability change. Native terminal parsing belongs in the
existing L2 codec; invalid, nontext or nonterminal frames cannot grant a boundary.
5. Preserve CI failures as direct old-source evidence. Add a deterministic
client regression that pauses local cleanup after terminal Write and proves
one immediate next frame is neither rejected nor executed early. Retain genuine
busy, excess frames, write/cleanup failure, cancellation and sequential-count
regressions. Repeat affected real main WS lifecycle/tool/continuation groups,
then run complete local checks/main smoke and all normal automatic exact-head
CI for the final combined correction source. Do not accept an old full pass for
new code. Verify merged tree and independent main CI before resuming features.
6. The protocol owner implements L3/L2/main correction and focused tests only
inside D:/my-project/TXAPI-references/worktrees/background-null. Root owns this
decision, docs, validation and the corrective PR. Previously authored owner-ledger
52cbe2c stays local in its own worktree; its final acceptance will use the merged
corrective baseline. Shadow scoring is only an external proposal, not in source.
