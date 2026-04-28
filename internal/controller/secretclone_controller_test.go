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
	"errors"
	"testing"
	"time"

	syncv1alpha1 "github.com/firemanxbr/go-operator-cloner/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestReconcileCreatesAndUpdatesManagedSecrets(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 1, 7, 12, 0, 0, 0, time.UTC)
	sourceSecret := sourceDockerConfigSecret()
	sourceSecret.ResourceVersion = "13"
	managedTarget := desiredSecret(policyObject(), sourceSecret, "team-a", nil)
	managedTarget.Data[".dockerconfigjson"] = []byte(`{"auths":{"ghcr.io":{"username":"stale"}}}`)

	reconciler, kubeClient := newTestReconciler(t,
		sourceSecret,
		namespace("github-secrets", nil),
		namespace("team-a", map[string]string{"secret-sync.firemanxbr.dev/enabled": "true"}),
		namespace("team-b", map[string]string{}),
		policyObject(),
		managedTarget,
	)
	reconciler.Now = func() time.Time { return now }

	if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "github-pull-secret"}}); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "github-pull-secret"}}); err != nil {
		t.Fatalf("reconcile failed after finalizer update: %v", err)
	}

	target := &corev1.Secret{}
	if err := kubeClient.Get(context.Background(), types.NamespacedName{Name: "ghcr-pull-secret", Namespace: "team-a"}, target); err != nil {
		t.Fatalf("expected cloned Secret in team-a: %v", err)
	}
	if string(target.Data[".dockerconfigjson"]) != string(sourceSecret.Data[".dockerconfigjson"]) {
		t.Fatalf("target Secret data was not refreshed from source")
	}

	missingTarget := &corev1.Secret{}
	err := kubeClient.Get(context.Background(), types.NamespacedName{Name: "ghcr-pull-secret", Namespace: "team-b"}, missingTarget)
	if err == nil {
		t.Fatalf("did not expect Secret to be cloned into non-matching namespace")
	}

	policy := &syncv1alpha1.SecretClone{}
	if err := kubeClient.Get(context.Background(), types.NamespacedName{Name: "github-pull-secret"}, policy); err != nil {
		t.Fatalf("expected policy to exist: %v", err)
	}
	if policy.Status.SyncedNamespaces != 1 {
		t.Fatalf("expected 1 synced namespace, got %d", policy.Status.SyncedNamespaces)
	}
	if policy.Status.ObservedSourceResourceVersion != "13" {
		t.Fatalf("expected observed source resource version 13, got %q", policy.Status.ObservedSourceResourceVersion)
	}
	if policy.Status.LastSyncTime == nil || !policy.Status.LastSyncTime.Time.Equal(now) {
		t.Fatalf("expected last sync time %v, got %#v", now, policy.Status.LastSyncTime)
	}
}

func TestReconcileHandlesMissingPolicyAndFinalizerUpdateErrors(t *testing.T) {
	t.Parallel()

	reconciler, _ := newTestReconciler(t)
	if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "missing"}}); err != nil {
		t.Fatalf("missing policy should be ignored: %v", err)
	}

	policy := policyObject()
	baseClient := fake.NewClientBuilder().
		WithScheme(newScheme(t)).
		WithStatusSubresource(&syncv1alpha1.SecretClone{}).
		WithObjects(policy).
		Build()
	reconciler = &SecretCloneReconciler{
		Client: updateErrorClient{Client: baseClient, err: errors.New("update failed")},
		Scheme: newScheme(t),
	}
	if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: policy.Name}}); err == nil {
		t.Fatalf("expected finalizer update error to be returned")
	}
}

func TestReconcileCreatesManagedSecretsWhenMissing(t *testing.T) {
	t.Parallel()

	policy := policyObject()
	reconciler, kubeClient := newTestReconciler(t,
		sourceDockerConfigSecret(),
		namespace("github-secrets", nil),
		namespace("team-a", map[string]string{"secret-sync.firemanxbr.dev/enabled": "true"}),
		policy,
	)

	_, _ = reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: policy.Name}})
	if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: policy.Name}}); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	target := &corev1.Secret{}
	if err := kubeClient.Get(context.Background(), types.NamespacedName{Name: "ghcr-pull-secret", Namespace: "team-a"}, target); err != nil {
		t.Fatalf("expected cloned Secret to be created: %v", err)
	}
}

func TestReconcilePrunesOrphans(t *testing.T) {
	t.Parallel()

	policy := policyObject()
	orphan := desiredSecret(policy, sourceDockerConfigSecret(), "stale", nil)

	reconciler, kubeClient := newTestReconciler(t,
		sourceDockerConfigSecret(),
		namespace("github-secrets", nil),
		namespace("team-a", map[string]string{"secret-sync.firemanxbr.dev/enabled": "true"}),
		policy,
		orphan,
	)

	if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: policy.Name}}); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: policy.Name}}); err != nil {
		t.Fatalf("reconcile failed after finalizer update: %v", err)
	}

	if err := kubeClient.Get(context.Background(), types.NamespacedName{Name: "ghcr-pull-secret", Namespace: "stale"}, &corev1.Secret{}); err == nil {
		t.Fatalf("expected orphan Secret to be pruned")
	}
}

func TestReconcileReportsUnmanagedTargetConflict(t *testing.T) {
	t.Parallel()

	policy := policyObject()
	conflictingSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "ghcr-pull-secret", Namespace: "team-a"},
		Data:       map[string][]byte{".dockerconfigjson": []byte("keep-me")},
		Type:       corev1.SecretTypeDockerConfigJson,
	}

	reconciler, kubeClient := newTestReconciler(t,
		sourceDockerConfigSecret(),
		namespace("github-secrets", nil),
		namespace("team-a", map[string]string{"secret-sync.firemanxbr.dev/enabled": "true"}),
		policy,
		conflictingSecret,
	)

	result, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: policy.Name}})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("unexpected requeue after finalizer add on first reconcile: %v", result.RequeueAfter)
	}

	result, err = reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: policy.Name}})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if result.RequeueAfter != requeueDelay {
		t.Fatalf("expected requeue after %v, got %v", requeueDelay, result.RequeueAfter)
	}

	refreshedPolicy := &syncv1alpha1.SecretClone{}
	if err := kubeClient.Get(context.Background(), types.NamespacedName{Name: policy.Name}, refreshedPolicy); err != nil {
		t.Fatalf("could not reload policy: %v", err)
	}
	if readyConditionStatus(refreshedPolicy.Status.Conditions, syncv1alpha1.SecretCloneConditionReady) != metav1.ConditionFalse {
		t.Fatalf("expected Ready condition to be false after conflict")
	}
}

func TestReconcileHandlesMissingSourceSecret(t *testing.T) {
	t.Parallel()

	policy := policyObject()
	reconciler, kubeClient := newTestReconciler(t,
		namespace("github-secrets", nil),
		namespace("team-a", map[string]string{"secret-sync.firemanxbr.dev/enabled": "true"}),
		policy,
	)

	_, _ = reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: policy.Name}})
	result, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: policy.Name}})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if result.RequeueAfter != requeueDelay {
		t.Fatalf("expected requeue after %v, got %v", requeueDelay, result.RequeueAfter)
	}

	refreshedPolicy := &syncv1alpha1.SecretClone{}
	if err := kubeClient.Get(context.Background(), types.NamespacedName{Name: policy.Name}, refreshedPolicy); err != nil {
		t.Fatalf("could not reload policy: %v", err)
	}
	if readyConditionStatus(refreshedPolicy.Status.Conditions, syncv1alpha1.SecretCloneConditionSourceSecretReady) != metav1.ConditionFalse {
		t.Fatalf("expected SourceSecretReady condition to be false")
	}
}

func TestReconcileRejectsInvalidPolicy(t *testing.T) {
	t.Parallel()

	policy := policyObject()
	policy.Spec.SourceSecretRef = syncv1alpha1.SecretReference{}
	reconciler, kubeClient := newTestReconciler(t, policy)

	_, _ = reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: policy.Name}})
	result, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: policy.Name}})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if result.RequeueAfter != requeueDelay {
		t.Fatalf("expected invalid spec to requeue after %v, got %v", requeueDelay, result.RequeueAfter)
	}

	refreshedPolicy := &syncv1alpha1.SecretClone{}
	if err := kubeClient.Get(context.Background(), types.NamespacedName{Name: policy.Name}, refreshedPolicy); err != nil {
		t.Fatalf("could not reload policy: %v", err)
	}
	if readyConditionStatus(refreshedPolicy.Status.Conditions, syncv1alpha1.SecretCloneConditionReady) != metav1.ConditionFalse {
		t.Fatalf("expected Ready condition to be false for invalid policy")
	}
}

func TestReconcileDeletesManagedSecretsOnPolicyDeletion(t *testing.T) {
	t.Parallel()

	policy := policyObject()
	policy.Finalizers = []string{finalizerName}
	now := metav1.Now()
	policy.DeletionTimestamp = &now
	managedSecret := desiredSecret(policy, sourceDockerConfigSecret(), "team-a", nil)

	reconciler, kubeClient := newTestReconciler(t,
		policy,
		managedSecret,
	)

	if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: policy.Name}}); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	if err := kubeClient.Get(context.Background(), types.NamespacedName{Name: managedSecret.Name, Namespace: managedSecret.Namespace}, &corev1.Secret{}); err == nil {
		t.Fatalf("expected managed Secret to be deleted during finalization")
	}

	refreshedPolicy := &syncv1alpha1.SecretClone{}
	if err := kubeClient.Get(context.Background(), types.NamespacedName{Name: policy.Name}, refreshedPolicy); err == nil && len(refreshedPolicy.Finalizers) != 0 {
		t.Fatalf("expected finalizers to be removed")
	}
}

func TestReconcileDeleteBranches(t *testing.T) {
	t.Parallel()

	policy := policyObject()
	reconciler, _ := newTestReconciler(t, policy)
	if _, err := reconciler.reconcileDelete(context.Background(), policy); err != nil {
		t.Fatalf("reconcileDelete without finalizer should not fail: %v", err)
	}

	policyWithFinalizer := policyObject()
	policyWithFinalizer.Finalizers = []string{finalizerName}
	managedSecret := desiredSecret(policyWithFinalizer, sourceDockerConfigSecret(), "team-a", nil)
	deleteClient := deleteErrorClient{
		Client: fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(policyWithFinalizer, managedSecret).Build(),
		err:    errors.New("delete failed"),
	}
	reconciler = &SecretCloneReconciler{Client: deleteClient, Scheme: newScheme(t)}
	if _, err := reconciler.reconcileDelete(context.Background(), policyWithFinalizer); err == nil {
		t.Fatalf("expected reconcileDelete to propagate delete errors")
	}

	updateErrorReconciler := &SecretCloneReconciler{
		Client: updateErrorClient{
			Client: fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(policyWithFinalizer).Build(),
			err:    errors.New("update failed"),
		},
		Scheme: newScheme(t),
	}
	if _, err := updateErrorReconciler.reconcileDelete(context.Background(), policyWithFinalizer); err == nil {
		t.Fatalf("expected reconcileDelete to propagate update errors")
	}
}

func TestReconcilePropagatesSourceReadErrors(t *testing.T) {
	t.Parallel()

	policy := policyObject()
	scheme := newScheme(t)
	baseClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&syncv1alpha1.SecretClone{}).
		WithObjects(policy, namespace("github-secrets", nil)).
		Build()

	reconciler := &SecretCloneReconciler{
		Client: getErrorClient{
			Client: baseClient,
			key:    types.NamespacedName{Name: "github-pat", Namespace: "github-secrets"},
			err:    errors.New("boom"),
		},
		Scheme: scheme,
	}

	_, _ = reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: policy.Name}})
	if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: policy.Name}}); err == nil {
		t.Fatalf("expected source Secret read error to be returned")
	}
}

func TestReconcileHandlesNamespaceSelectionAndPruneErrors(t *testing.T) {
	t.Parallel()

	policy := policyObject()
	source := sourceDockerConfigSecret()
	scheme := newScheme(t)
	baseClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&syncv1alpha1.SecretClone{}).
		WithObjects(policy, source, namespace("github-secrets", nil)).
		Build()

	listFailingReconciler := &SecretCloneReconciler{
		Client: listErrorClient{Client: baseClient, listErr: errors.New("list failed")},
		Scheme: scheme,
	}
	_, _ = listFailingReconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: policy.Name}})
	result, err := listFailingReconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: policy.Name}})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if result.RequeueAfter != requeueDelay {
		t.Fatalf("expected namespace selection error to requeue after %v, got %v", requeueDelay, result.RequeueAfter)
	}

	prunePolicy := policyObject()
	pruneSource := sourceDockerConfigSecret()
	orphan := desiredSecret(prunePolicy, pruneSource, "team-z", nil)
	teamA := namespace("team-a", map[string]string{"secret-sync.firemanxbr.dev/enabled": "true"})
	deleteFailClient := deleteErrorClient{
		Client: fake.NewClientBuilder().
			WithScheme(newScheme(t)).
			WithStatusSubresource(&syncv1alpha1.SecretClone{}).
			WithObjects(prunePolicy, pruneSource, namespace("github-secrets", nil), teamA, orphan).
			Build(),
		err: errors.New("delete failed"),
	}
	deleteFailReconciler := &SecretCloneReconciler{Client: deleteFailClient, Scheme: newScheme(t)}
	_, _ = deleteFailReconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: prunePolicy.Name}})
	result, err = deleteFailReconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: prunePolicy.Name}})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if result.RequeueAfter != requeueDelay {
		t.Fatalf("expected prune error to requeue after %v, got %v", requeueDelay, result.RequeueAfter)
	}
}

func TestDataAndMetadataHelpers(t *testing.T) {
	t.Parallel()

	policy := policyObject()
	source := sourceDockerConfigSecret()
	existing := desiredSecret(policy, source, "team-a", nil)
	existing.Labels["keep"] = "true"
	desired := desiredSecret(policy, source, "team-a", existing)
	desired.Annotations[sourceResourceVersionAnnotation] = "2"

	if !mergeDesiredSecret(existing, desired) {
		t.Fatalf("expected metadata update to be detected")
	}
	if !isManagedByPolicy(existing, policy) {
		t.Fatalf("expected Secret to be managed by policy")
	}
	if targetSecretName(policy, "different") != "ghcr-pull-secret" {
		t.Fatalf("targetSecretName should prefer explicit spec value")
	}
	if targetSecretName(&syncv1alpha1.SecretClone{}, "source-name") != "source-name" {
		t.Fatalf("targetSecretName should fall back to source name")
	}
	if conditionStatus(true) != metav1.ConditionTrue || conditionStatus(false) != metav1.ConditionFalse {
		t.Fatalf("conditionStatus should map booleans to condition values")
	}
	if !immutableEqual(nil, nil) {
		t.Fatalf("nil immutable pointers should compare equal")
	}
	if cloneImmutable(source.Immutable) == source.Immutable {
		t.Fatalf("cloneImmutable should return a new pointer")
	}
	if !immutableEqual(source.Immutable, cloneImmutable(source.Immutable)) {
		t.Fatalf("immutableEqual should compare true for equal values")
	}
	if cloneImmutable(nil) != nil {
		t.Fatalf("cloneImmutable should keep nil pointers nil")
	}
	if mergeDesiredSecret(desiredSecret(policy, source, "team-a", nil), desiredSecret(policy, source, "team-a", nil)) {
		t.Fatalf("mergeDesiredSecret should report false when nothing changed")
	}
	mutatedCurrent := desiredSecret(policy, source, "team-a", nil)
	mutatedDesired := desiredSecret(policy, source, "team-a", nil)
	mutatedDesired.Labels["another"] = "label"
	mutatedDesired.Annotations["another"] = "annotation"
	mutatedDesired.Type = corev1.SecretTypeOpaque
	falseValue := false
	mutatedDesired.Immutable = &falseValue
	mutatedDesired.Data["extra"] = []byte("value")
	if !mergeDesiredSecret(mutatedCurrent, mutatedDesired) {
		t.Fatalf("mergeDesiredSecret should detect updates across fields")
	}

	clonedData := cloneSecretData(source.Data)
	clonedData[".dockerconfigjson"][0] = '['
	if string(source.Data[".dockerconfigjson"]) == string(clonedData[".dockerconfigjson"]) {
		t.Fatalf("cloneSecretData should deep copy byte slices")
	}
	if !secretDataEqual(source.Data, cloneSecretData(source.Data)) {
		t.Fatalf("secretDataEqual should treat deep copies as equal")
	}
	if secretDataEqual(source.Data, map[string][]byte{"other": []byte("x")}) {
		t.Fatalf("secretDataEqual should detect different payloads")
	}
	if cloneSecretData(nil) != nil {
		t.Fatalf("cloneSecretData should keep nil maps nil")
	}
	if secretDataEqual(source.Data, map[string][]byte{}) {
		t.Fatalf("secretDataEqual should detect different map lengths")
	}
}

func TestSelectorAndNamespaceHelpers(t *testing.T) {
	t.Parallel()

	policy := policyObject()

	selector, err := selectorForPolicy(policy)
	if err != nil {
		t.Fatalf("selectorForPolicy returned error: %v", err)
	}
	if !selector.Matches(teamALabels()) {
		t.Fatalf("selector should match team-a namespace labels")
	}

	invalidPolicy := policyObject()
	invalidPolicy.Spec.NamespaceSelector = &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{{
			Key:      "broken",
			Operator: metav1.LabelSelectorOperator("Nope"),
		}},
	}
	if _, err := selectorForPolicy(invalidPolicy); err == nil {
		t.Fatalf("expected selectorForPolicy to reject invalid selector")
	}
	if validatePolicy(&syncv1alpha1.SecretClone{}) == nil {
		t.Fatalf("expected validatePolicy to reject missing source reference")
	}

	selectorFreePolicy := policyObject()
	selectorFreePolicy.Spec.NamespaceSelector = nil
	reconciler, _ := newTestReconciler(
		t,
		namespace("github-secrets", nil),
		namespace("team-a", teamALabels()),
		namespace("team-b", nil),
		selectorFreePolicy,
	)
	targetNamespaces, err := reconciler.listTargetNamespaces(context.Background(), selectorFreePolicy)
	if err != nil {
		t.Fatalf("listTargetNamespaces returned error: %v", err)
	}
	if len(targetNamespaces) != 2 {
		t.Fatalf("expected selector-free policy to include 2 target namespaces, got %d", len(targetNamespaces))
	}
	skippingPolicy := policyObject()
	deletingNamespace := namespace("terminating", map[string]string{"secret-sync.firemanxbr.dev/enabled": "true"})
	now := metav1.Now()
	deletingNamespace.DeletionTimestamp = &now
	deletingNamespace.Finalizers = []string{"kubernetes"}
	reconciler, _ = newTestReconciler(
		t,
		namespace("github-secrets", nil),
		namespace("team-a", teamALabels()),
		namespace("kube-system", teamALabels()),
		deletingNamespace,
		skippingPolicy,
	)
	targetNamespaces, err = reconciler.listTargetNamespaces(context.Background(), skippingPolicy)
	if err != nil {
		t.Fatalf("listTargetNamespaces returned error: %v", err)
	}
	if len(targetNamespaces) != 1 || targetNamespaces[0].Name != "team-a" {
		t.Fatalf("expected listTargetNamespaces to skip excluded and terminating namespaces, got %#v", targetNamespaces)
	}

	if (&SecretCloneReconciler{}).now().IsZero() {
		t.Fatalf("default now() should return a timestamp")
	}

	notFoundDeleteClient := deleteErrorClient{
		Client: fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
			desiredSecret(policy, sourceDockerConfigSecret(), "team-a", nil),
		).Build(),
		err: apierrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, "missing"),
	}
	reconcilerWithDeleteNotFound := &SecretCloneReconciler{Client: notFoundDeleteClient, Scheme: newScheme(t)}
	if err := reconcilerWithDeleteNotFound.pruneOrphans(context.Background(), policy, "ghcr-pull-secret", nil); err != nil {
		t.Fatalf("pruneOrphans should ignore not found delete errors: %v", err)
	}
	if err := reconcilerWithDeleteNotFound.deleteManagedSecrets(context.Background(), policy); err != nil {
		t.Fatalf("deleteManagedSecrets should ignore not found delete errors: %v", err)
	}

	invalidSelectorPolicy := policyObject()
	invalidSelectorPolicy.Spec.NamespaceSelector = &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{{
			Key:      "broken",
			Operator: metav1.LabelSelectorOperator("Nope"),
		}},
	}
	if _, err := reconciler.listTargetNamespaces(context.Background(), invalidSelectorPolicy); err == nil {
		t.Fatalf("listTargetNamespaces should reject invalid selectors")
	}

	if (&SecretCloneReconciler{}).now().IsZero() {
		t.Fatalf("default now() should return a timestamp")
	}
}

func TestMapFunctions(t *testing.T) {
	t.Parallel()

	policy := policyObject()
	reconciler, _ := newTestReconciler(t,
		policy,
		sourceDockerConfigSecret(),
	)

	namespaceRequests := reconciler.mapNamespaceToSecretClones(context.Background(), namespace("team-a", nil))
	if len(namespaceRequests) != 1 || namespaceRequests[0].Name != policy.Name {
		t.Fatalf("unexpected namespace map requests: %#v", namespaceRequests)
	}

	secretRequests := reconciler.mapSecretToSecretClones(context.Background(), sourceDockerConfigSecret())
	if len(secretRequests) != 1 || secretRequests[0].Name != policy.Name {
		t.Fatalf("unexpected secret map requests: %#v", secretRequests)
	}

	unrelatedSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "default"}}
	if requests := reconciler.mapSecretToSecretClones(context.Background(), unrelatedSecret); len(requests) != 0 {
		t.Fatalf("unexpected requests for unrelated Secret: %#v", requests)
	}
	if requests := reconciler.mapSecretToSecretClones(context.Background(), namespace("not-a-secret", nil)); len(requests) != 0 {
		t.Fatalf("unexpected requests for non-Secret objects: %#v", requests)
	}
}

func TestMapFunctionsHandleListErrors(t *testing.T) {
	t.Parallel()

	reconciler := &SecretCloneReconciler{
		Client: listErrorClient{Client: fake.NewClientBuilder().Build(), listErr: errors.New("boom")},
	}

	if requests := reconciler.mapNamespaceToSecretClones(context.Background(), namespace("team-a", nil)); requests != nil {
		t.Fatalf("expected nil requests on list error")
	}
	if requests := reconciler.mapSecretToSecretClones(context.Background(), sourceDockerConfigSecret()); requests != nil {
		t.Fatalf("expected nil requests on list error")
	}
	if _, err := reconciler.listTargetNamespaces(context.Background(), policyObject()); err == nil {
		t.Fatalf("expected listTargetNamespaces to propagate list errors")
	}
}

func TestUpsertAndDeleteHelpers(t *testing.T) {
	t.Parallel()

	policy := policyObject()
	source := sourceDockerConfigSecret()
	scheme := newScheme(t)

	upsertError := &SecretCloneReconciler{
		Client: getErrorClient{
			Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
			key:    types.NamespacedName{Name: "ghcr-pull-secret", Namespace: "team-a"},
			err:    errors.New("get failed"),
		},
		Scheme: scheme,
	}
	if err := upsertError.upsertTargetSecret(context.Background(), policy, source, "team-a"); err == nil {
		t.Fatalf("expected upsertTargetSecret to propagate get errors")
	}

	current := desiredSecret(policy, source, "team-a", nil)
	stableClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(current).Build()
	stableReconciler := &SecretCloneReconciler{Client: stableClient, Scheme: scheme}
	if err := stableReconciler.upsertTargetSecret(context.Background(), policy, source, "team-a"); err != nil {
		t.Fatalf("expected upsertTargetSecret no-op update to succeed: %v", err)
	}

	listErrReconciler := &SecretCloneReconciler{
		Client: listErrorClient{Client: stableClient, listErr: errors.New("list failed")},
		Scheme: scheme,
	}
	if err := listErrReconciler.pruneOrphans(context.Background(), policy, source.Name, nil); err == nil {
		t.Fatalf("expected pruneOrphans to propagate list errors")
	}
	if err := listErrReconciler.deleteManagedSecrets(context.Background(), policy); err == nil {
		t.Fatalf("expected deleteManagedSecrets to propagate list errors")
	}
}

func TestStatusHelpersPropagatePatchErrors(t *testing.T) {
	t.Parallel()

	policy := policyObject()
	reconciler := &SecretCloneReconciler{
		Client: statusErrorClient{
			Client: fake.NewClientBuilder().
				WithScheme(newScheme(t)).
				WithStatusSubresource(&syncv1alpha1.SecretClone{}).
				WithObjects(policy).
				Build(),
			err: errors.New("patch failed"),
		},
		Scheme: newScheme(t),
	}

	if _, err := reconciler.requeueAfterStatus(context.Background(), policy, false, 0, "", "boom", "boom"); err == nil {
		t.Fatalf("expected requeueAfterStatus to propagate patch errors")
	}
	if _, err := reconciler.completeAfterStatus(context.Background(), policy, true, true, 1, "1", "ok", "ok"); err == nil {
		t.Fatalf("expected completeAfterStatus to propagate patch errors")
	}
}

func TestSetupWithManager(t *testing.T) {
	t.Parallel()

	scheme := newScheme(t)
	testEnv := &envtest.Environment{}
	cfg, err := testEnv.Start()
	if err != nil {
		t.Fatalf("failed to start envtest: %v", err)
	}
	t.Cleanup(func() {
		if stopErr := testEnv.Stop(); stopErr != nil {
			t.Fatalf("failed to stop envtest: %v", stopErr)
		}
	})

	manager, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	reconciler := &SecretCloneReconciler{Client: manager.GetClient(), Scheme: manager.GetScheme()}
	if err := reconciler.SetupWithManager(manager); err != nil {
		t.Fatalf("setup with manager failed: %v", err)
	}
}

func newTestReconciler(t *testing.T, objects ...client.Object) (*SecretCloneReconciler, client.Client) {
	t.Helper()

	scheme := newScheme(t)
	builder := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&syncv1alpha1.SecretClone{})
	builder = builder.WithObjects(objects...)
	kubeClient := builder.Build()

	reconciler := &SecretCloneReconciler{
		Client: kubeClient,
		Scheme: scheme,
		Now: func() time.Time {
			return time.Date(2026, 1, 7, 12, 0, 0, 0, time.UTC)
		},
	}

	return reconciler, kubeClient
}

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add corev1 to scheme: %v", err)
	}
	if err := syncv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add API to scheme: %v", err)
	}

	return scheme
}

func policyObject() *syncv1alpha1.SecretClone {
	return &syncv1alpha1.SecretClone{
		ObjectMeta: metav1.ObjectMeta{
			Name: "github-pull-secret",
			UID:  types.UID("policy-uid-1234"),
		},
		Spec: syncv1alpha1.SecretCloneSpec{
			SourceSecretRef: syncv1alpha1.SecretReference{
				Namespace: "github-secrets",
				Name:      "github-pat",
			},
			TargetSecretName: "ghcr-pull-secret",
			NamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"secret-sync.firemanxbr.dev/enabled": "true"},
			},
			ExcludedNamespaces: []string{"kube-system"},
		},
	}
}

func sourceDockerConfigSecret() *corev1.Secret {
	immutable := true
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "github-pat",
			Namespace:       "github-secrets",
			ResourceVersion: "1",
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			".dockerconfigjson": []byte(`{"auths":{"ghcr.io":{"username":"firemanxbr","password":"ghp_example"}}}`),
		},
		Immutable: &immutable,
	}
}

func namespace(name string, namespaceLabels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: namespaceLabels,
		},
	}
}

func teamALabels() labels.Set {
	return labels.Set{"secret-sync.firemanxbr.dev/enabled": "true"}
}

func readyConditionStatus(conditions []metav1.Condition, conditionType string) metav1.ConditionStatus {
	for _, condition := range conditions {
		if condition.Type == conditionType {
			return condition.Status
		}
	}
	return metav1.ConditionUnknown
}

type listErrorClient struct {
	client.Client
	listErr error
}

func (c listErrorClient) List(_ context.Context, _ client.ObjectList, _ ...client.ListOption) error {
	return c.listErr
}

type getErrorClient struct {
	client.Client
	key types.NamespacedName
	err error
}

func (c getErrorClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if key == c.key {
		return c.err
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

type deleteErrorClient struct {
	client.Client
	err error
}

func (c deleteErrorClient) Delete(_ context.Context, _ client.Object, _ ...client.DeleteOption) error {
	return c.err
}

type updateErrorClient struct {
	client.Client
	err error
}

func (c updateErrorClient) Update(_ context.Context, _ client.Object, _ ...client.UpdateOption) error {
	return c.err
}

type statusErrorClient struct {
	client.Client
	err error
}

func (c statusErrorClient) Status() client.SubResourceWriter {
	return statusErrorWriter{SubResourceWriter: c.Client.Status(), err: c.err}
}

type statusErrorWriter struct {
	client.SubResourceWriter
	err error
}

func (w statusErrorWriter) Patch(_ context.Context, _ client.Object, _ client.Patch, _ ...client.SubResourcePatchOption) error {
	return w.err
}
