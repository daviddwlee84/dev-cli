package help

// WorkflowTLDR is shared by CLI orientation and the dashboard manual.
const WorkflowTLDR = `TL;DR: default managed-task loop

  dev start --> HOT: work / commit / test
                  ^               |
                  |  dev resume   |  dev park --next "..."
                  +------ WARM <--+
                  |
                  +-- direct:          dev done      --> DONE
                  +-- branch/worktree: dev done --ff --> DONE
                  +-- branch/worktree: dev done --pr --> push / review handoff
                                                        |
                          feedback --> resume if parked --> work

  DONE --> dev sweep (report) --> dev sweep --apply (reap) --> next task
  Remote merge detection and cleanup are not automatic; verify integration first.`
