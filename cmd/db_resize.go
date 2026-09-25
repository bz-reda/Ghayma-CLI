package cmd

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"paas-cli/internal/api"
)

// A disk change runs on the platform after the resize request returns: the
// database reads "resizing" until it ends, and disk_gb keeps its old value
// until then. A grow stays online; a shrink stops the database while its files
// move to the smaller disk.

const (
	dbStatusRunning  = "running"
	dbStatusResizing = "resizing"
	dbStatusError    = "error"
)

// Vars so tests need not sit out real polls.
var (
	dbResizePollInterval = 5 * time.Second
	dbResizeWaitTimeout  = 30 * time.Minute
)

var diskPhaseLabels = map[string]string{
	"expand":    "growing the disk",
	"stop":      "stopping the database",
	"copy_out":  "copying the data to the new disk",
	"release":   "releasing the old disk",
	"recreate":  "creating the new disk",
	"copy_back": "copying the data onto the new disk",
	"start":     "starting the database",
	"cleanup":   "finishing up",
}

// diskPhaseLabel falls back to the raw phase, so a phase added later still shows.
func diskPhaseLabel(phase string) string {
	if label, ok := diskPhaseLabels[phase]; ok {
		return label
	}
	return phase
}

// startedDiskChange is the change a "resizing" reply started. A reply without
// the resize object is read from the request instead.
func startedDiskChange(reply *api.DatabaseInfo, currentGB, requestedGB int) api.DatabaseResize {
	if reply.Resize != nil && reply.Resize.TargetDiskGB > 0 {
		return *reply.Resize
	}
	change := api.DatabaseResize{Direction: "grow", TargetDiskGB: requestedGB}
	if requestedGB < currentGB {
		change.Direction = "shrink"
	}
	return change
}

// diskChangeHeadline is the one line printed when a disk change starts.
func diskChangeHeadline(change api.DatabaseResize) string {
	if change.Direction == "shrink" {
		return fmt.Sprintf("Shrinking to %d GB: the database stops for about a minute while its data moves to the smaller disk.", change.TargetDiskGB)
	}
	return fmt.Sprintf("Growing to %d GB: the database stays online.", change.TargetDiskGB)
}

// dbStatusText is a database's status with its disk change: the target and
// phase while it runs, the error once one failed.
func dbStatusText(db api.DatabaseInfo) string {
	r := db.Resize
	switch {
	case r != nil && r.Error != "":
		return fmt.Sprintf("%s · disk change to %d GB failed: %s", db.Status, r.TargetDiskGB, oneLine(r.Error))
	case r != nil && db.Status == dbStatusResizing:
		text := fmt.Sprintf("%s to %d GB", dbStatusResizing, r.TargetDiskGB)
		if r.Phase != "" {
			text += " · " + diskPhaseLabel(r.Phase)
		}
		return text
	}
	return db.Status
}

// oneLine folds a multi-line server message (a copy job's log tail) into one.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// resizeErrorText renders a refused resize: the server's own sentence, plus
// the smallest disk that fits when the target was too small.
func resizeErrorText(err error, currentGB int) string {
	var de *api.DiskChangeError
	if !errors.As(err, &de) {
		return formatMarketplaceError(err)
	}
	if de.Code != api.CodeDiskTooSmall || de.MinDiskGB <= 0 {
		return de.Error()
	}
	text := fmt.Sprintf("%s\n   Smallest possible now: %d GB", de.Error(), de.MinDiskGB)
	if currentGB > 0 && de.MinDiskGB >= currentGB {
		text += fmt.Sprintf(" (the disk is %d GB, so it cannot shrink yet)", currentGB)
	}
	return text
}

// waitForDiskChange polls the database until its disk change ends, printing
// each new phase, and reports whether the change succeeded.
func waitForDiskChange(client *api.Client, name, id string, change api.DatabaseResize) bool {
	lastPhase := change.Phase
	if lastPhase != "" {
		fmt.Printf("⏳ %s\n", diskPhaseLabel(lastPhase))
	}
	for polls := int(dbResizeWaitTimeout / dbResizePollInterval); polls > 0; polls-- {
		time.Sleep(dbResizePollInterval)

		db, err := client.GetDatabase(id)
		if errors.Is(err, api.ErrUnauthorized) {
			fmt.Printf("❌ %v\n", err)
			return false
		}
		if err != nil {
			continue
		}
		if db.Status != dbStatusResizing {
			return reportDiskChangeEnd(name, db, change.TargetDiskGB)
		}
		if db.Resize != nil && db.Resize.Phase != "" && db.Resize.Phase != lastPhase {
			lastPhase = db.Resize.Phase
			fmt.Printf("⏳ %s\n", diskPhaseLabel(lastPhase))
		}
	}
	fmt.Printf("⚠️  Still resizing after %d minutes; the change continues on the platform. Check it with: ghayma db info %s\n",
		int(dbResizeWaitTimeout.Minutes()), name)
	return false
}

// reportDiskChangeEnd prints how a disk change ended: the new size, or the
// change's own error.
func reportDiskChangeEnd(name string, db *api.DatabaseInfo, targetGB int) bool {
	if db.Resize != nil && db.Resize.Error != "" {
		fmt.Printf("❌ %s\n", db.Resize.Error)
		if db.Status == dbStatusError {
			fmt.Println("   The database needs support to recover: contact support.")
		}
		return false
	}
	if db.Status != dbStatusRunning {
		fmt.Printf("❌ The disk change ended with the database %s. Check it with: ghayma db info %s\n", db.Status, name)
		return false
	}
	size := db.DiskGB
	if size == 0 {
		size = targetGB
	}
	fmt.Printf("✅ Disk is now %d GB\n", size)
	return true
}
