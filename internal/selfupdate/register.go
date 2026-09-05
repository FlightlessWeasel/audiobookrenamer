package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"

	"audiobookrenamer/internal/model"
	"audiobookrenamer/internal/worker"
)

// Register binds the "selfupdate" job type to the worker Manager. The job
// payload is the target Release (as written by the /api/update/apply handler);
// the job downloads, verifies and swaps the binary, then asks main to re-exec
// into it via Updater.RestartRequested. The job row still persists as "done" —
// the restart is orchestrated by main, not from inside the handler.
func Register(wm *worker.Manager, u *Updater) {
	wm.Register(model.JobSelfUpdate, func(ctx context.Context, job model.Job, p *worker.Progress) error {
		var rel Release
		if err := json.Unmarshal([]byte(job.Payload), &rel); err != nil {
			return fmt.Errorf("bad payload: %w", err)
		}
		if err := u.Apply(ctx, rel, func(pct int, msg string) {
			p.Set(pct, 100, msg)
		}); err != nil {
			return err
		}
		u.requestRestart()
		return nil
	})
}
