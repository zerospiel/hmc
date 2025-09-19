// Copyright 2024
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package backup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	cron "github.com/robfig/cron/v3"
	velerov1 "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kcmv1 "github.com/K0rdent/kcm/api/v1beta1"
	"github.com/K0rdent/kcm/internal/utils"
)

// scheduleMgmtNameLabel holds a reference to the [github.com/K0rdent/kcm/api/v1beta1.ManagementBackup] object name.
const scheduleMgmtNameLabel = "k0rdent.mirantis.com/management-backup"

func (r *Reconciler) ReconcileBackup(ctx context.Context, mgmtBackup *kcmv1.ManagementBackup) (ctrl.Result, error) {
	if mgmtBackup == nil {
		return ctrl.Result{}, nil
	}

	s, err := getScope(ctx, r.mgmtCl, r.systemNamespace)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to construct backup scope: %w", err)
	}
	s.mgmtBackup = mgmtBackup

	if updated, err := utils.AddKCMComponentLabel(ctx, r.mgmtCl, mgmtBackup); updated || err != nil { // put all mgmtbackup to backup
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to add component label: %w", err)
		}
		return ctrl.Result{}, nil
	}

	if isRestored(mgmtBackup) {
		return r.updateAfterRestoration(ctx, mgmtBackup, s)
	}

	if mgmtBackup.IsSchedule() { // schedule-creation path
		cronSchedule, err := cron.ParseStandard(mgmtBackup.Spec.Schedule)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to parse cron schedule %s: %w", mgmtBackup.Spec.Schedule, err)
		}

		isDue, nextAttemptTime := getNextAttemptTime(mgmtBackup, cronSchedule)

		// here we can put as many conditions as we want, e.g. if upgrade is progressing
		// TODO: add a condition to check if management upgrade is progressing
		isOkayToCreateBackup := isDue && !r.isVeleroBackupProgressing(ctx, mgmtBackup)

		if isOkayToCreateBackup {
			return r.createScheduleBackup(ctx, mgmtBackup, nextAttemptTime)
		}

		newNextAttemptTime := &metav1.Time{Time: nextAttemptTime}
		if !mgmtBackup.Status.NextAttempt.Equal(newNextAttemptTime) {
			mgmtBackup.Status.NextAttempt = newNextAttemptTime

			if err := r.mgmtCl.Status().Update(ctx, mgmtBackup); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to update ManagementBackup %s status with next attempt time: %w", mgmtBackup.Name, err)
			}
		}

		if mgmtBackup.Status.LastBackupName == "" { // is not due, nothing to do
			return ctrl.Result{}, nil
		}
	} else if mgmtBackup.Status.LastBackupName == "" && !isRestored(mgmtBackup) { // single mgmtbackup, velero backup has not been created yet
		return r.createSingleBackup(ctx, mgmtBackup)
	}

	l := ctrl.LoggerFrom(ctx)
	l.V(1).Info("Collecting backup status")

	// TODO: several backups

	backupName := mgmtBackup.Name
	if mgmtBackup.IsSchedule() {
		backupName = mgmtBackup.Status.LastBackupName
	}
	veleroBackup := new(velerov1.Backup)
	// region TODO
	if err := r.mgmtCl.Get(ctx, client.ObjectKey{
		Name:      backupName,
		Namespace: r.systemNamespace,
	}, veleroBackup); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get velero Backup: %w", err)
	}

	l.V(1).Info("Updating backup status")
	mgmtBackup.Status.LastBackup = &veleroBackup.Status
	if err := r.mgmtCl.Status().Update(ctx, mgmtBackup); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update ManagementBackup %s status: %w", mgmtBackup.Name, err)
	}

	return ctrl.Result{}, nil
}

func (r *Reconciler) updateAfterRestoration(ctx context.Context, s *scope) (ctrl.Result, error) {
	mgmtBackup := &(*s.mgmtBackup) // shallow TODO

	removeVeleroLabels := func() {
		delete(mgmtBackup.Labels, velerov1.BackupNameLabel)
		delete(mgmtBackup.Labels, velerov1.RestoreNameLabel)
	}

	ldebug := ctrl.LoggerFrom(ctx).V(1)

	// fast-track: if all backups have already been seen, we are done, hence just clean velero labels
	shouldReturn := true
	for _, lb := range mgmtBackup.Status.LastBackups {
		if lb.LastBackup == nil && lb.LastBackupName == "" && lb.LastBackupTime.IsZero() {
			shouldReturn = false
			break
		}
	}
	if shouldReturn {
		ldebug.Info("Removing velero labels after restoration when status is already set")
		removeVeleroLabels()
		// TODO: patch instead of update!
		if err := r.mgmtCl.Update(ctx, mgmtBackup); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update ManagementBackup labels after restoration: %w", err)
		}

		return ctrl.Result{}, nil
	}

	// TODO: problem with names if I had to change names of the VELERO backups
	// mgmtName + region / mgmtName + region + timestamp

	updateStatus := false
	seenMgmt := false
	backupStatuses := make([]kcmv1.ManagementBackupSingleStatus, 0, len(s.clientsByDeployment))
	// TODO: if mgmt (no region) -> consider only ONCE!

	// TODO: concurrency
	for _, deploy := range s.clientsByDeployment {
		region := deploy.cld.Status.Region

		// if mgmt has already been processed then skip the same work
		if region == "" {
			if seenMgmt {
				continue
			}

			seenMgmt = true
		}

		// list backups in the corresponding cluster (region or mgmt)
		veleroBackups := new(velerov1.BackupList)
		if err := deploy.cl.List(ctx, veleroBackups); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to list velero Backups: %w", err)
		}

		if mgmtBackup.IsSchedule() {
			ldebug.Info("Updating schedule after restoration", "region", region)
			lastBackup, ok := getMostRecentlyProducedBackup(mgmtBackup.Name, veleroBackups.Items)
			if ok { // if have not found then there were no backups yet
				ldebug.Info("Found last backup", "last_backup_name", lastBackup.Name)

				// next attempt will be fetched on the next event
				mgmtBackup.Status.LastBackup = &lastBackup.Status
				mgmtBackup.Status.LastBackupName = lastBackup.Name
				mgmtBackup.Status.LastBackupTime = lastBackup.Status.StartTimestamp
				updateStatus = true
			} else {
				ldebug.Info("No last backup has been found", "region", region)
			}
		} else {
			ldebug.Info("Updating single backup after restoration", "region", region)

			mgmtBackupName := mgmtBackup.Name
			if region != "" {
				mgmtBackupName = mgmtBackup.Name + "-" + region
			}

			for _, v := range veleroBackups.Items {
				if mgmtBackupName == v.Name {
					backupStatuses = append(backupStatuses, kcmv1.ManagementBackupSingleStatus{})
					mgmtBackup.Status.LastBackup = &v.Status
					mgmtBackup.Status.LastBackupName = v.Name
					mgmtBackup.Status.LastBackupTime = v.Status.StartTimestamp
					updateStatus = true
					break
				}
			}
		}
	}

	// TODO: here we have to produce several backups: mgmt + region(s);
	// adjust names by region
	// mgmt backup now have to have list of backups instead of a single

	if updateStatus {
		ldebug.Info("Updating status after restoration")
		if err := r.mgmtCl.Status().Update(ctx, mgmtBackup); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update ManagementBackup status after restoration: %w", err)
		}

		return ctrl.Result{}, nil
	}

	ldebug.Info("Removing velero labels after restoration without status set")
	removeVeleroLabels()
	// TODO: consider patch
	if err := r.mgmtCl.Update(ctx, mgmtBackup); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update ManagementBackup labels after restoration: %w", err)
	}

	return ctrl.Result{}, nil
}

func TODOfindStatusBackup(mgmtBackup *kcmv1.ManagementBackup, name string, veleroBackups velerov1.BackupList) (kcmv1.ManagementBackupSingleStatus, bool) {
	for _, v := range mgmtBackup.Status.LastBackups {
		if v.LastBackupName == name {
			return v, true
		}
	}
	return kcmv1.ManagementBackupSingleStatus{}, false
}

func (r *Reconciler) createScheduleBackup(ctx context.Context, mgmtBackup *kcmv1.ManagementBackup, nextAttemptTime time.Time) (ctrl.Result, error) {
	now := time.Now().UTC()
	backupName := mgmtBackup.TimestampedBackupName(now)

	if err := r.createNewVeleroBackup(ctx, backupName, withScheduleLabel(mgmtBackup.Name), withStorageLocation(mgmtBackup.Spec.StorageLocation)); err != nil {
		if isMetaError(err) {
			return r.propagateMetaError(ctx, mgmtBackup, err.Error())
		}
		return ctrl.Result{}, err
	}

	mgmtBackup.Status.LastBackupName = backupName
	mgmtBackup.Status.LastBackupTime = &metav1.Time{Time: now}
	mgmtBackup.Status.NextAttempt = &metav1.Time{Time: nextAttemptTime}

	if err := r.mgmtCl.Status().Update(ctx, mgmtBackup); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update ManagementBackup %s status: %w", mgmtBackup.Name, err)
	}

	return ctrl.Result{}, nil
}

func (r *Reconciler) createSingleBackup(ctx context.Context, mgmtBackup *kcmv1.ManagementBackup) (ctrl.Result, error) {
	if err := r.createNewVeleroBackup(ctx, mgmtBackup.Name, withStorageLocation(mgmtBackup.Spec.StorageLocation)); err != nil {
		if isMetaError(err) {
			return r.propagateMetaError(ctx, mgmtBackup, err.Error())
		}
		return ctrl.Result{}, err
	}

	mgmtBackup.Status.LastBackupName = mgmtBackup.Name
	mgmtBackup.Status.LastBackupTime = &metav1.Time{Time: time.Now().UTC()}

	// TODO mgmt
	if err := r.mgmtCl.Status().Update(ctx, mgmtBackup); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update ManagementBackup %s status: %w", mgmtBackup.Name, err)
	}

	return ctrl.Result{}, nil
}

type createOpt func(*velerov1.Backup)

func withScheduleLabel(scheduleName string) createOpt {
	return func(b *velerov1.Backup) {
		if b.Labels == nil {
			b.Labels = make(map[string]string)
		}
		b.Labels[scheduleMgmtNameLabel] = scheduleName
	}
}

func withStorageLocation(loc string) createOpt {
	return func(b *velerov1.Backup) {
		b.Spec.StorageLocation = loc
	}
}

func (r *Reconciler) createNewVeleroBackup(ctx context.Context, backupName string, s *scope, createOpts ...createOpt) error {
	l := ctrl.LoggerFrom(ctx)

	veleroBackup := r.getNewVeleroBackup(ctx, backupName, s)

	for _, o := range createOpts {
		o(veleroBackup)
	}

	// TODO region
	if err := r.mgmtCl.Create(ctx, veleroBackup); client.IgnoreAlreadyExists(err) != nil { // avoid err-loop on status update error
		return fmt.Errorf("failed to create velero Backup: %w", err)
	}

	l.V(1).Info("Velero Backup has been created", "new_backup_name", client.ObjectKeyFromObject(veleroBackup))
	return nil
}

func (r *Reconciler) getNewVeleroBackup(ctx context.Context, backupName string, s *scope) *velerov1.Backup {
	// TODO: name
	return &velerov1.Backup{
		TypeMeta: metav1.TypeMeta{
			APIVersion: velerov1.SchemeGroupVersion.String(),
			Kind:       "Backup",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      backupName,
			Namespace: r.systemNamespace,
		},
		Spec: *getBackupTemplateSpec(ctx, s),
	}
}

func (r *Reconciler) isVeleroBackupProgressing(ctx context.Context, schedule *kcmv1.ManagementBackup) bool {
	backups := &velerov1.BackupList{}
	// TODO region
	if err := r.mgmtCl.List(ctx, backups, client.InNamespace(r.systemNamespace), client.MatchingLabels{scheduleMgmtNameLabel: schedule.Name}); err != nil {
		return true
	}

	for _, backup := range backups.Items {
		if backup.Status.Phase == velerov1.BackupPhaseNew ||
			backup.Status.Phase == velerov1.BackupPhaseInProgress {
			return true
		}
	}

	return false
}

func (r *Reconciler) propagateMetaError(ctx context.Context, mgmtBackup *kcmv1.ManagementBackup, errorMsg string) (ctrl.Result, error) {
	mgmtBackup.Status.Error = "Probably Velero is not installed: " + errorMsg
	if err := r.mgmtCl.Status().Update(ctx, mgmtBackup); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update ManagementBackup %s status: %w", mgmtBackup.Name, err)
	}

	return ctrl.Result{}, nil // no need to requeue if got such error
}

func getMostRecentlyProducedBackup(mgmtBackupName string, backups []velerov1.Backup) (*velerov1.Backup, bool) {
	if len(backups) == 0 {
		return &velerov1.Backup{}, false
	}

	now := time.Now().UTC()

	const timeFormat = "20060102150405"

	var (
		mostRecent time.Time
		minIdx     int
		prefix     = mgmtBackupName + "-"
	)
	for i, backup := range backups {
		if backup.Labels[scheduleMgmtNameLabel] != mgmtBackupName {
			continue // process only backups produced by this schedule
		}

		ts := strings.TrimPrefix(backup.Name, prefix)

		t, err := time.Parse(timeFormat, ts)
		if err != nil {
			continue
		}

		if !t.After(now) && (mostRecent.IsZero() || t.After(mostRecent)) {
			mostRecent = t
			minIdx = i
		}
	}

	return &backups[minIdx], !mostRecent.IsZero()
}

func isRestored(mgmtBackup *kcmv1.ManagementBackup) bool {
	return mgmtBackup.Labels[velerov1.RestoreNameLabel] != "" && mgmtBackup.Labels[velerov1.BackupNameLabel] != ""
}

func getNextAttemptTime(schedule *kcmv1.ManagementBackup, cronSchedule cron.Schedule) (bool, time.Time) {
	lastBackupTime := schedule.CreationTimestamp.Time
	if !schedule.Status.LastBackupTime.IsZero() {
		lastBackupTime = schedule.Status.LastBackupTime.Time
	}

	nextAttemptTime := cronSchedule.Next(lastBackupTime) // might be in past so rely on now
	now := time.Now().UTC()
	isDue := now.After(nextAttemptTime)
	if isDue {
		nextAttemptTime = now
	}

	return isDue, nextAttemptTime
}

func isMetaError(err error) bool {
	return err != nil && (apimeta.IsNoMatchError(err) ||
		apimeta.IsAmbiguousError(err) ||
		apierrors.IsNotFound(err) || // if resource is not found
		errors.Is(err, &discovery.ErrGroupDiscoveryFailed{}))
}
