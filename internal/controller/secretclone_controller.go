/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	syncv1alpha1 "github.com/firemanxbr/go-operator-cloner/api/v1alpha1"
)

const (
	finalizerName                   = "sync.firemanxbr.dev/finalizer"
	managedByLabel                  = "sync.firemanxbr.dev/managed-by"
	policyUIDLabel                  = "sync.firemanxbr.dev/policy-uid"
	policyNameAnnotation            = "sync.firemanxbr.dev/policy-name"
	sourceNamespaceAnnotation       = "sync.firemanxbr.dev/source-namespace"
	sourceNameAnnotation            = "sync.firemanxbr.dev/source-name"
	sourceResourceVersionAnnotation = "sync.firemanxbr.dev/source-resource-version"
	managedByValue                  = "go-operator-cloner"
	requeueDelay                    = 2 * time.Minute
)

// SecretCloneReconciler reconciles a SecretClone object.
type SecretCloneReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Now    func() time.Time
}

// +kubebuilder:rbac:groups=sync.firemanxbr.dev,resources=secretclones,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sync.firemanxbr.dev,resources=secretclones/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=sync.firemanxbr.dev,resources=secretclones/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

// Reconcile copies the source Secret into matching namespaces and prunes stale clones.
func (r *SecretCloneReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = logf.FromContext(ctx).WithValues("secretClone", req.Name)

	policy := &syncv1alpha1.SecretClone{}
	if err := r.Get(ctx, req.NamespacedName, policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !policy.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, policy)
	}

	if controllerutil.AddFinalizer(policy, finalizerName) {
		if err := r.Update(ctx, policy); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if err := validatePolicy(policy); err != nil {
		message := fmt.Sprintf("invalid SecretClone spec: %v", err)
		return r.requeueAfterStatus(ctx, policy, false, 0, "", "ValidationFailed", message)
	}

	sourceSecret := &corev1.Secret{}
	sourceKey := types.NamespacedName{
		Namespace: policy.Spec.SourceSecretRef.Namespace,
		Name:      policy.Spec.SourceSecretRef.Name,
	}
	if err := r.Get(ctx, sourceKey, sourceSecret); err != nil {
		if apierrors.IsNotFound(err) {
			message := fmt.Sprintf("source Secret %s/%s was not found", sourceKey.Namespace, sourceKey.Name)
			return r.requeueAfterStatus(ctx, policy, false, 0, "", "SourceSecretMissing", message)
		}
		return ctrl.Result{}, err
	}

	targetNamespaces, err := r.listTargetNamespaces(ctx, policy)
	if err != nil {
		return r.requeueAfterStatus(ctx, policy, true, 0, sourceSecret.ResourceVersion, "NamespaceSelectionFailed", err.Error())
	}

	syncedNamespaces, err := r.syncTargetNamespaces(ctx, policy, sourceSecret, targetNamespaces)
	if err != nil {
		return r.requeueAfterStatus(ctx, policy, true, syncedNamespaces, sourceSecret.ResourceVersion, "TargetSyncFailed", err.Error())
	}

	if err := r.pruneOrphans(ctx, policy, sourceSecret.Name, targetNamespaces); err != nil {
		return r.requeueAfterStatus(ctx, policy, true, syncedNamespaces, sourceSecret.ResourceVersion, "TargetPruneFailed", err.Error())
	}

	return r.completeAfterStatus(
		ctx,
		policy,
		true,
		true,
		syncedNamespaces,
		sourceSecret.ResourceVersion,
		"Reconciled",
		fmt.Sprintf("cloned %s/%s into %d namespace(s)", sourceKey.Namespace, sourceKey.Name, syncedNamespaces),
	)
}

func (r *SecretCloneReconciler) reconcileDelete(ctx context.Context, policy *syncv1alpha1.SecretClone) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(policy, finalizerName) {
		return ctrl.Result{}, nil
	}

	if err := r.deleteManagedSecrets(ctx, policy); err != nil {
		return ctrl.Result{}, err
	}

	controllerutil.RemoveFinalizer(policy, finalizerName)
	if err := r.Update(ctx, policy); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SecretCloneReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&syncv1alpha1.SecretClone{}).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(r.mapNamespaceToSecretClones)).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.mapSecretToSecretClones)).
		Named("secretclone").
		Complete(r)
}

func (r *SecretCloneReconciler) mapNamespaceToSecretClones(ctx context.Context, _ client.Object) []reconcile.Request {
	return r.listAllPolicyRequests(ctx)
}

func (r *SecretCloneReconciler) mapSecretToSecretClones(ctx context.Context, obj client.Object) []reconcile.Request {
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		return nil
	}

	policies := &syncv1alpha1.SecretCloneList{}
	if err := r.List(ctx, policies); err != nil {
		return nil
	}

	requests := make([]reconcile.Request, 0, len(policies.Items))
	for _, policy := range policies.Items {
		if policy.Spec.SourceSecretRef.Namespace == secret.Namespace && policy.Spec.SourceSecretRef.Name == secret.Name {
			requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: policy.Name}})
		}
	}

	return requests
}

func (r *SecretCloneReconciler) listAllPolicyRequests(ctx context.Context) []reconcile.Request {
	policies := &syncv1alpha1.SecretCloneList{}
	if err := r.List(ctx, policies); err != nil {
		return nil
	}

	requests := make([]reconcile.Request, 0, len(policies.Items))
	for _, policy := range policies.Items {
		requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: policy.Name}})
	}

	return requests
}

func (r *SecretCloneReconciler) listTargetNamespaces(ctx context.Context, policy *syncv1alpha1.SecretClone) ([]corev1.Namespace, error) {
	selector, err := selectorForPolicy(policy)
	if err != nil {
		return nil, err
	}

	namespaceList := &corev1.NamespaceList{}
	listOptions := []client.ListOption{}
	if policy.Spec.NamespaceSelector != nil {
		listOptions = append(listOptions, client.MatchingLabelsSelector{Selector: selector})
	}
	if err := r.List(ctx, namespaceList, listOptions...); err != nil {
		return nil, err
	}

	excluded := excludedNamespaces(policy.Spec.ExcludedNamespaces)
	targets := make([]corev1.Namespace, 0, len(namespaceList.Items))
	for _, namespace := range namespaceList.Items {
		if namespace.Name == policy.Spec.SourceSecretRef.Namespace {
			continue
		}
		if namespace.DeletionTimestamp != nil {
			continue
		}
		if excluded[namespace.Name] {
			continue
		}
		targets = append(targets, namespace)
	}

	slices.SortFunc(targets, func(left, right corev1.Namespace) int {
		return strings.Compare(left.Name, right.Name)
	})

	return targets, nil
}

func (r *SecretCloneReconciler) syncTargetNamespaces(
	ctx context.Context,
	policy *syncv1alpha1.SecretClone,
	sourceSecret *corev1.Secret,
	targetNamespaces []corev1.Namespace,
) (int, error) {
	synced := 0
	for _, namespace := range targetNamespaces {
		if err := r.upsertTargetSecret(ctx, policy, sourceSecret, namespace.Name); err != nil {
			return synced, err
		}
		synced++
	}

	return synced, nil
}

func (r *SecretCloneReconciler) upsertTargetSecret(
	ctx context.Context,
	policy *syncv1alpha1.SecretClone,
	sourceSecret *corev1.Secret,
	targetNamespace string,
) error {
	targetName := targetSecretName(policy, sourceSecret.Name)
	key := types.NamespacedName{Namespace: targetNamespace, Name: targetName}

	current := &corev1.Secret{}
	if err := r.Get(ctx, key, current); err != nil {
		if apierrors.IsNotFound(err) {
			return r.Create(ctx, desiredSecret(policy, sourceSecret, targetNamespace, nil))
		}
		return err
	}

	if !isManagedByPolicy(current, policy) {
		return fmt.Errorf("target Secret %s/%s already exists and is not managed by policy %s", targetNamespace, targetName, policy.Name)
	}

	desired := desiredSecret(policy, sourceSecret, targetNamespace, current)
	if !mergeDesiredSecret(current, desired) {
		return nil
	}

	return r.Update(ctx, current)
}

func (r *SecretCloneReconciler) pruneOrphans(
	ctx context.Context,
	policy *syncv1alpha1.SecretClone,
	sourceSecretName string,
	targetNamespaces []corev1.Namespace,
) error {
	secretList := &corev1.SecretList{}
	if err := r.List(ctx, secretList, client.MatchingLabels{policyUIDLabel: string(policy.UID)}); err != nil {
		return err
	}

	targetName := targetSecretName(policy, sourceSecretName)
	desiredNamespaces := make(map[string]struct{}, len(targetNamespaces))
	for _, namespace := range targetNamespaces {
		desiredNamespaces[namespace.Name] = struct{}{}
	}

	for _, item := range secretList.Items {
		_, keepNamespace := desiredNamespaces[item.Namespace]
		if keepNamespace && item.Name == targetName {
			continue
		}
		if err := r.Delete(ctx, &item); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}

	return nil
}

func (r *SecretCloneReconciler) deleteManagedSecrets(ctx context.Context, policy *syncv1alpha1.SecretClone) error {
	secretList := &corev1.SecretList{}
	if err := r.List(ctx, secretList, client.MatchingLabels{policyUIDLabel: string(policy.UID)}); err != nil {
		return err
	}

	for _, item := range secretList.Items {
		if err := r.Delete(ctx, &item); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}

	return nil
}

func (r *SecretCloneReconciler) patchStatus(
	ctx context.Context,
	policy *syncv1alpha1.SecretClone,
	sourceReady bool,
	targetsReady bool,
	syncedNamespaces int,
	sourceResourceVersion string,
	reason string,
	message string,
) error {
	before := policy.DeepCopy()
	now := metav1.NewTime(r.now())

	policy.Status.ObservedGeneration = policy.Generation
	policy.Status.ObservedSourceResourceVersion = sourceResourceVersion
	policy.Status.SyncedNamespaces = safeInt32(syncedNamespaces)
	policy.Status.LastSyncTime = &now

	meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type:               syncv1alpha1.SecretCloneConditionSourceSecretReady,
		Status:             conditionStatus(sourceReady),
		Reason:             reason,
		Message:            message,
		ObservedGeneration: policy.Generation,
		LastTransitionTime: now,
	})
	meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type:               syncv1alpha1.SecretCloneConditionTargetsSynced,
		Status:             conditionStatus(targetsReady),
		Reason:             reason,
		Message:            message,
		ObservedGeneration: policy.Generation,
		LastTransitionTime: now,
	})
	meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type:               syncv1alpha1.SecretCloneConditionReady,
		Status:             conditionStatus(sourceReady && targetsReady),
		Reason:             reason,
		Message:            message,
		ObservedGeneration: policy.Generation,
		LastTransitionTime: now,
	})

	return r.Status().Patch(ctx, policy, client.MergeFrom(before))
}

func safeInt32(value int) int32 {
	if value > math.MaxInt32 {
		return math.MaxInt32
	}

	if value < math.MinInt32 {
		return math.MinInt32
	}

	return int32(value)
}

func (r *SecretCloneReconciler) requeueAfterStatus(
	ctx context.Context,
	policy *syncv1alpha1.SecretClone,
	sourceReady bool,
	syncedNamespaces int,
	sourceResourceVersion string,
	reason string,
	message string,
) (ctrl.Result, error) {
	if err := r.patchStatus(ctx, policy, sourceReady, false, syncedNamespaces, sourceResourceVersion, reason, message); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueDelay}, nil
}

func (r *SecretCloneReconciler) completeAfterStatus(
	ctx context.Context,
	policy *syncv1alpha1.SecretClone,
	sourceReady bool,
	targetsReady bool,
	syncedNamespaces int,
	sourceResourceVersion string,
	reason string,
	message string,
) (ctrl.Result, error) {
	if err := r.patchStatus(ctx, policy, sourceReady, targetsReady, syncedNamespaces, sourceResourceVersion, reason, message); err != nil {
		return ctrl.Result{}, err
	}
	logf.FromContext(ctx).Info("reconciled SecretClone", "syncedNamespaces", syncedNamespaces)
	return ctrl.Result{}, nil
}

func selectorForPolicy(policy *syncv1alpha1.SecretClone) (labels.Selector, error) {
	if policy.Spec.NamespaceSelector == nil {
		return labels.Everything(), nil
	}

	selector, err := metav1.LabelSelectorAsSelector(policy.Spec.NamespaceSelector)
	if err != nil {
		return nil, err
	}

	return selector, nil
}

func validatePolicy(policy *syncv1alpha1.SecretClone) error {
	var allErrs field.ErrorList
	if policy.Spec.SourceSecretRef.Namespace == "" {
		allErrs = append(allErrs, field.Required(field.NewPath("spec", "sourceSecretRef", "namespace"), "namespace is required"))
	}
	if policy.Spec.SourceSecretRef.Name == "" {
		allErrs = append(allErrs, field.Required(field.NewPath("spec", "sourceSecretRef", "name"), "name is required"))
	}
	return allErrs.ToAggregate()
}

func excludedNamespaces(names []string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, name := range names {
		out[name] = true
	}
	return out
}

func targetSecretName(policy *syncv1alpha1.SecretClone, sourceName string) string {
	if policy.Spec.TargetSecretName != "" {
		return policy.Spec.TargetSecretName
	}
	return sourceName
}

func desiredSecret(
	policy *syncv1alpha1.SecretClone,
	sourceSecret *corev1.Secret,
	targetNamespace string,
	existing *corev1.Secret,
) *corev1.Secret {
	var labelsMap map[string]string
	var annotationsMap map[string]string
	if existing != nil {
		labelsMap = maps.Clone(existing.Labels)
		annotationsMap = maps.Clone(existing.Annotations)
	}
	if labelsMap == nil {
		labelsMap = map[string]string{}
	}
	if annotationsMap == nil {
		annotationsMap = map[string]string{}
	}

	labelsMap[managedByLabel] = managedByValue
	labelsMap[policyUIDLabel] = string(policy.UID)
	annotationsMap[policyNameAnnotation] = policy.Name
	annotationsMap[sourceNamespaceAnnotation] = sourceSecret.Namespace
	annotationsMap[sourceNameAnnotation] = sourceSecret.Name
	annotationsMap[sourceResourceVersionAnnotation] = sourceSecret.ResourceVersion

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        targetSecretName(policy, sourceSecret.Name),
			Namespace:   targetNamespace,
			Labels:      labelsMap,
			Annotations: annotationsMap,
		},
		Type:      sourceSecret.Type,
		Data:      cloneSecretData(sourceSecret.Data),
		Immutable: cloneImmutable(sourceSecret.Immutable),
	}
}

func mergeDesiredSecret(current *corev1.Secret, desired *corev1.Secret) bool {
	updated := false

	if !maps.Equal(current.Labels, desired.Labels) {
		current.Labels = desired.Labels
		updated = true
	}
	if !maps.Equal(current.Annotations, desired.Annotations) {
		current.Annotations = desired.Annotations
		updated = true
	}
	if current.Type != desired.Type {
		current.Type = desired.Type
		updated = true
	}
	if !immutableEqual(current.Immutable, desired.Immutable) {
		current.Immutable = cloneImmutable(desired.Immutable)
		updated = true
	}
	if !secretDataEqual(current.Data, desired.Data) {
		current.Data = cloneSecretData(desired.Data)
		updated = true
	}

	return updated
}

func isManagedByPolicy(secret *corev1.Secret, policy *syncv1alpha1.SecretClone) bool {
	return secret.Labels[managedByLabel] == managedByValue && secret.Labels[policyUIDLabel] == string(policy.UID)
}

func cloneSecretData(data map[string][]byte) map[string][]byte {
	if data == nil {
		return nil
	}

	out := make(map[string][]byte, len(data))
	for key, value := range data {
		out[key] = slices.Clone(value)
	}

	return out
}

func secretDataEqual(left, right map[string][]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for key, leftValue := range left {
		rightValue, ok := right[key]
		if !ok || !slices.Equal(leftValue, rightValue) {
			return false
		}
	}
	return true
}

func cloneImmutable(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func immutableEqual(left, right *bool) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func conditionStatus(ready bool) metav1.ConditionStatus {
	if ready {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

func (r *SecretCloneReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}
