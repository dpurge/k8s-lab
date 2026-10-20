package jobs

import (
	"time"

	"k8s-lab/shared/retention"

	"phraseforge/internal/metrics"
)

// RetentionRules are the rules that purge finished jobs: done ones after
// keepDone and failed or cancelled ones after keepFailed (0 keeps them
// forever). A pending or running job is never selected, whatever its age.
//
// The age is measured from updated_at: the table has no finished_at, and
// updated_at is last written when the job reaches its final status (complete,
// cancel, FailStale); progress and step updates only touch running jobs, so for
// a finished job it is the time it finished. Nothing references jobs by a
// foreign key, and a job's checkpoint is a column of its own row, so a purge
// leaves nothing dangling. llm_calls.job_id has no foreign key either and
// keeps its old value.
func RetentionRules(keepDone, keepFailed time.Duration) []retention.Rule {
	return []retention.Rule{
		{Name: metrics.RuleJobsDone, Table: "jobs", TimeColumn: "updated_at", Condition: "status = 'done'", Retention: keepDone},
		{Name: metrics.RuleJobsFailed, Table: "jobs", TimeColumn: "updated_at", Condition: "status IN ('failed','cancelled')", Retention: keepFailed},
	}
}
