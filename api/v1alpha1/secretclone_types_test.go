package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestSecretCloneAPITypes(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme failed: %v", err)
	}

	secretClone := &SecretClone{
		ObjectMeta: metav1.ObjectMeta{Name: "sample"},
		Spec: SecretCloneSpec{
			SourceSecretRef: SecretReference{
				Namespace: "github-secrets",
				Name:      "github-pat",
			},
			TargetSecretName: "ghcr-pull-secret",
			NamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"secret-sync.firemanxbr.dev/enabled": "true"},
			},
			ExcludedNamespaces: []string{"kube-system"},
		},
		Status: SecretCloneStatus{
			ObservedGeneration:            7,
			ObservedSourceResourceVersion: "42",
			SyncedNamespaces:              3,
			Conditions: []metav1.Condition{{
				Type:   SecretCloneConditionReady,
				Status: metav1.ConditionTrue,
			}},
		},
	}
	now := metav1.Now()
	secretClone.Status.LastSyncTime = &now

	copyInto := &SecretClone{}
	secretClone.DeepCopyInto(copyInto)
	if copyInto.Name != secretClone.Name {
		t.Fatalf("DeepCopyInto did not copy metadata")
	}
	copyInto.Spec.ExcludedNamespaces[0] = "changed"
	if secretClone.Spec.ExcludedNamespaces[0] == "changed" {
		t.Fatalf("DeepCopyInto should deep copy slices")
	}

	copied := secretClone.DeepCopy()
	if copied == secretClone || copied.Spec.NamespaceSelector == secretClone.Spec.NamespaceSelector {
		t.Fatalf("DeepCopy should return distinct objects")
	}
	if copied.DeepCopyObject() == nil {
		t.Fatalf("DeepCopyObject should return a runtime.Object")
	}

	list := &SecretCloneList{Items: []SecretClone{*secretClone}}
	listCopy := &SecretCloneList{}
	list.DeepCopyInto(listCopy)
	if len(listCopy.Items) != 1 {
		t.Fatalf("DeepCopyInto should copy list items")
	}
	if list.DeepCopy() == nil || list.DeepCopyObject() == nil {
		t.Fatalf("SecretCloneList deepcopy helpers should return objects")
	}

	specCopy := secretClone.Spec.DeepCopy()
	if specCopy == nil || specCopy == &secretClone.Spec {
		t.Fatalf("SecretCloneSpec DeepCopy should return a distinct value")
	}

	statusCopy := secretClone.Status.DeepCopy()
	if statusCopy == nil || statusCopy == &secretClone.Status {
		t.Fatalf("SecretCloneStatus DeepCopy should return a distinct value")
	}

	refCopy := secretClone.Spec.SourceSecretRef.DeepCopy()
	if refCopy == nil || refCopy == &secretClone.Spec.SourceSecretRef {
		t.Fatalf("SecretReference DeepCopy should return a distinct value")
	}
}

func TestSecretCloneAPINilDeepCopyHelpers(t *testing.T) {
	t.Parallel()

	var (
		secretClone     *SecretClone
		secretCloneList *SecretCloneList
		secretCloneSpec *SecretCloneSpec
		secretStatus    *SecretCloneStatus
		secretReference *SecretReference
	)

	if secretClone.DeepCopy() != nil || secretClone.DeepCopyObject() != nil {
		t.Fatalf("nil SecretClone deepcopy helpers should return nil")
	}
	if secretCloneList.DeepCopy() != nil || secretCloneList.DeepCopyObject() != nil {
		t.Fatalf("nil SecretCloneList deepcopy helpers should return nil")
	}
	if secretCloneSpec.DeepCopy() != nil {
		t.Fatalf("nil SecretCloneSpec DeepCopy should return nil")
	}
	if secretStatus.DeepCopy() != nil {
		t.Fatalf("nil SecretCloneStatus DeepCopy should return nil")
	}
	if secretReference.DeepCopy() != nil {
		t.Fatalf("nil SecretReference DeepCopy should return nil")
	}
}
