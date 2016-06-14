package volume

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/camptocamp/conplicity/handler"
	"github.com/camptocamp/conplicity/util"
)

// Volume provides backup methods for a single Docker volume
type Volume struct {
	Name            string
	Target          string
	BackupDir       string
	Mount           string
	FullIfOlderThan string
	RemoveOlderThan string
	Client          *handler.Conplicity
}

// Constants
const cacheMount = "duplicity_cache:/root/.cache/duplicity"
const timeFormat = "Mon Jan 2 15:04:05 2006"

var fullBackupRx = regexp.MustCompile("Last full backup date: (.+)")
var chainEndTimeRx = regexp.MustCompile("Chain end time: (.+)")

// Backup performs the backup of a volume with duplicity
func (v *Volume) Backup() (metrics []string, err error) {
	_, _, err = v.Client.LaunchDuplicity(
		[]string{
			"--full-if-older-than", v.FullIfOlderThan,
			"--s3-use-new-style",
			"--ssh-options", "-oStrictHostKeyChecking=no",
			"--no-encryption",
			"--allow-source-mismatch",
			"--name", v.Name,
			v.BackupDir,
			v.Target,
		},
		[]string{
			v.Mount,
			cacheMount,
		},
	)
	return
}

// RemoveOld cleans up old backup data from duplicity
func (v *Volume) RemoveOld() (metrics []string, err error) {
	_, _, err = v.Client.LaunchDuplicity(
		[]string{
			"remove-older-than", v.RemoveOlderThan,
			"--s3-use-new-style",
			"--ssh-options", "-oStrictHostKeyChecking=no",
			"--no-encryption",
			"--force",
			"--name", v.Name,
			v.Target,
		},
		[]string{
			cacheMount,
		},
	)
	return
}

// Cleanup removes old index data from duplicity
func (v *Volume) Cleanup() (metrics []string, err error) {
	_, _, err = v.Client.LaunchDuplicity(
		[]string{
			"cleanup",
			"--s3-use-new-style",
			"--ssh-options", "-oStrictHostKeyChecking=no",
			"--no-encryption",
			"--force",
			"--extra-clean",
			"--name", v.Name,
			v.Target,
		},
		[]string{
			cacheMount,
		},
	)
	return
}

// Verify checks that the backup is usable
func (v *Volume) Verify() (metrics []string, err error) {
	state, _, err := v.Client.LaunchDuplicity(
		[]string{
			"verify",
			"--s3-use-new-style",
			"--ssh-options", "-oStrictHostKeyChecking=no",
			"--no-encryption",
			"--allow-source-mismatch",
			"--name", v.Name,
			v.Target,
			v.BackupDir,
		},
		[]string{
			v.Mount,
			cacheMount,
		},
	)

	metric := fmt.Sprintf("conplicity{volume=\"%v\",what=\"verifyExitCode\"} %v", v.Name, state.ExitCode)
	metrics = []string{
		metric,
	}
	return
}

// Status gets the latest backup date info from duplicity
func (v *Volume) Status() (metrics []string, err error) {
	_, stdout, err := v.Client.LaunchDuplicity(
		[]string{
			"collection-status",
			"--s3-use-new-style",
			"--ssh-options", "-oStrictHostKeyChecking=no",
			"--no-encryption",
			"--name", v.Name,
			v.Target,
		},
		[]string{
			v.Mount,
			cacheMount,
		},
	)

	fullBackup := fullBackupRx.FindStringSubmatch(stdout)
	fullBackupDate, err := time.Parse(timeFormat, strings.TrimSpace(fullBackup[1]))
	util.CheckErr(err, "Failed to parse full backup date: %v", -1)
	chainEndTime := chainEndTimeRx.FindStringSubmatch(stdout)
	chainEndTimeDate, err := time.Parse(timeFormat, strings.TrimSpace(chainEndTime[1]))
	util.CheckErr(err, "Failed to parse chain end time date: %v", -1)

	lastBackupMetric := fmt.Sprintf("conplicity{volume=\"%v\",what=\"lastBackup\"} %v", v.Name, chainEndTimeDate.Unix())

	lastFullBackupMetric := fmt.Sprintf("conplicity{volume=\"%v\",what=\"lastFullBackup\"} %v", v.Name, fullBackupDate.Unix())

	metrics = []string{
		lastBackupMetric,
		lastFullBackupMetric,
	}

	return
}
