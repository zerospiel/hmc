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
	"fmt"

	cron "github.com/robfig/cron/v3"
	velerov1api "github.com/zerospiel/velero/pkg/apis/velero/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	hmcv1alpha1 "github.com/Mirantis/hmc/api/v1alpha1"
)

// Anno ease the process of distinguishing velero resources created by the HMC
// and created externally, to watch and rely only on the former.
const Anno = "hmc.mirantis.com/backup"

func (c *Config) ReconcileScheduledBackup(ctx context.Context, scheduledBackup *hmcv1alpha1.Backup, cronRaw string) error {
	if scheduledBackup == nil {
		return nil
	}

	l := ctrl.LoggerFrom(ctx).WithName("schedule-reconciler")

	if scheduledBackup.Status.Reference == nil {
		veleroSchedule := &velerov1api.Schedule{
			ObjectMeta: metav1.ObjectMeta{
				Name:        scheduledBackup.Name,
				Namespace:   c.systemNamespace,
				Annotations: map[string]string{Anno: ""},
			},
			Spec: velerov1api.ScheduleSpec{
				Template: velerov1api.BackupSpec{
					// TODO collect the spec / selectors
				},
				Schedule:                   cronRaw,
				UseOwnerReferencesInBackup: ref(true),
				SkipImmediately:            ref(false),
			},
		}

		err := c.cl.Create(ctx, veleroSchedule)
		isAlreadyExistsErr := apierrors.IsAlreadyExists(err)
		if err != nil && !isAlreadyExistsErr {
			return fmt.Errorf("failed to create velero Schedule: %w", err)
		}

		scheduledBackup.Status.Reference = &corev1.ObjectReference{
			APIVersion: velerov1api.SchemeGroupVersion.String(),
			Kind:       "Schedule",
			Namespace:  veleroSchedule.Namespace,
			Name:       veleroSchedule.Name,
		}

		if !isAlreadyExistsErr {
			l.Info("Initial schedule has been created")
			if err := c.cl.Status().Patch(ctx, scheduledBackup, client.Merge); err != nil {
				return fmt.Errorf("failed to patch scheduled backup status with updated reference: %w", err)
			}
			// velero schedule has been created, nothing yet to update here
			return nil
		}

		// velero schedule is already exists, scheduled-backup has been "restored", update its status
	}

	l.Info("Collecting scheduled backup status")

	veleroSchedule := new(velerov1api.Schedule)
	if err := c.cl.Get(ctx, client.ObjectKey{
		Name:      scheduledBackup.Status.Reference.Name,
		Namespace: scheduledBackup.Status.Reference.Namespace,
	}, veleroSchedule); err != nil {
		return fmt.Errorf("failed to get velero Schedule: %w", err)
	}

	// if backup does not exist then it has not been run yet
	veleroBackup := new(velerov1api.Backup)
	if err := c.cl.Get(ctx, client.ObjectKey{
		Name:      veleroSchedule.TimestampedName(veleroSchedule.Status.LastBackup.Time),
		Namespace: scheduledBackup.Status.Reference.Namespace,
	}, veleroBackup); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("failed to get velero Backup: %w", err)
	}

	cronSchedule, err := cron.ParseStandard(cronRaw)
	if err != nil {
		return fmt.Errorf("failed to parse cron schedule %s: %w", cronRaw, err)
	}

	scheduledBackup.Status.Schedule = &veleroSchedule.Status
	scheduledBackup.Status.NextAttempt = getNextAttemptTime(veleroSchedule, cronSchedule)
	if !veleroBackup.CreationTimestamp.IsZero() { // exists
		scheduledBackup.Status.LastBackup = &veleroBackup.Status
	}

	l.Info("Updating scheduled backup status")
	return c.cl.Status().Update(ctx, scheduledBackup)
}

func getNextAttemptTime(schedule *velerov1api.Schedule, cronSchedule cron.Schedule) *metav1.Time {
	lastBackupTime := schedule.CreationTimestamp.Time
	if schedule.Status.LastBackup != nil {
		lastBackupTime = schedule.Status.LastBackup.Time
	}

	if schedule.Status.LastSkipped != nil && schedule.Status.LastSkipped.After(lastBackupTime) {
		lastBackupTime = schedule.Status.LastSkipped.Time
	}

	return &metav1.Time{Time: cronSchedule.Next(lastBackupTime)}
}

func ref[T any](v T) *T { return &v }
