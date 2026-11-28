#!/bin/sh
# Claims a job's runner gate reservation, for a job-started hook in a cluster other than the gate's:
# its gate agent (runner-gate agent) serves GET /claim in-cluster at RUNNER_GATE_URL
# (http://runner-gate-agent.<namespace>.svc:8080) and forwards it to the gate with its token, so
# runner pods never hold the token. Run it (or source it) after the pool's own checks. The block below
# is the same as the end of job-started.sh (test_job_started.py keeps them equal).
set -eu
# Claim this job's reservation. A gate that cannot be reached lets the job run: a gate outage must
# not fail every job. "unknown" (the job never picked through the gate) runs too.
gate=${RUNNER_GATE_URL:-}
[ -n "$gate" ] || exit 0
answer=$(curl -fsS --max-time 5 --get "$gate/claim" \
    --data-urlencode "org=${GITHUB_REPOSITORY_OWNER:-}" --data-urlencode "run=${GITHUB_RUN_ID:-}" \
    --data-urlencode "attempt=${GITHUB_RUN_ATTEMPT:-}" --data-urlencode "job=${GITHUB_JOB:-}" 2>/dev/null) || answer=unreachable
case "$answer" in
    ok) echo "runner gate: reservation claimed (run $GITHUB_RUN_ID attempt $GITHUB_RUN_ATTEMPT job $GITHUB_JOB)" ;;
    expired\ *)
        set -- $answer
        echo "::error title=Runner gate reservation expired::Job $GITHUB_JOB started $2 s after its runner reservation ran out (reservations last $3 s), and the slot went to other work. Re-run the whole run to pick a runner again: gh run rerun $GITHUB_RUN_ID --repo $GITHUB_REPOSITORY (re-running only the failed jobs keeps the old choice)."
        exit 1 ;;
    unknown) echo "runner gate: no reservation for this job (it did not pick through the gate); running" ;;
    *) echo "runner gate: could not claim ($answer); running" ;;
esac
