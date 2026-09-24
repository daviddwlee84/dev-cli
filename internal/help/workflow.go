package help

// WorkflowTLDR is shared by CLI orientation and the dashboard manual.
const WorkflowTLDR = `TL;DR: default managed-task loop

  dev work start --> HOT: work / commit / test
  HOT  -- dev work park --next "..." --> WARM
  WARM -- dev work resume           --> HOT

  direct:          dev work done      --> DONE
  branch/worktree: dev work done --ff  --> DONE
  branch/worktree: dev work done --pr  --> push / review handoff
                                          |
            feedback --> resume if parked --> work

  DONE --> dev work sweep (report) --> dev work sweep --apply (reap) --> next task
  Remote merge detection and cleanup are not automatic; verify integration first.`
