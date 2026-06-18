package e2e

import (
	"context"
	"io"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestWaitForNamespaceDeletedRetriesTransientGetErrors(t *testing.T) {
	RegisterTestingT(t)

	const namespace = "transient-cleanup"

	clientset := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: namespace},
	})

	getCalls := 0
	clientset.Fake.PrependReactor("get", "namespaces", func(action k8stesting.Action) (bool, runtime.Object, error) {
		getCalls++
		if getCalls == 1 {
			return true, nil, io.ErrUnexpectedEOF
		}

		return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, namespace)
	})

	helper := &K8sHelper{clientset: clientset}
	helper.waitForNamespaceDeleted(context.Background(), namespace, time.Second, time.Millisecond)

	if getCalls < 2 {
		t.Fatalf("namespace get calls = %d, want at least 2", getCalls)
	}
}
