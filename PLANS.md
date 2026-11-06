# Execution plans

Use an execution plan for work that spans multiple modules, agents, or validation
stages. Store active plans in `docs/plans/` and keep them understandable without
the originating chat.

Every plan must state:

1. The user-visible outcome and what is deliberately out of scope.
2. The current code paths and contracts that the change relies on.
3. Decisions already made and assumptions that still need proof.
4. Ordered milestones with explicit dependencies.
5. File ownership for each agent, including shared files reserved for the
   integrator.
6. Acceptance criteria and exact verification commands.
7. Security, privacy, data-loss, migration, and rollback risks.

During execution, update the plan only when a contract, dependency, or acceptance
criterion changes. Do not turn it into a transcript. Record durable product or
architecture decisions separately under `docs/decisions/`.

A milestone is complete only when its integrated runtime path works, applicable
tests pass, the diff contains no unrelated changes, and remaining risks are
written down.
