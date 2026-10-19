package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestProbeReadsSuccessfulRetryPod(t *testing.T) {
	b := Backup{ID: "bk-retry", Mode: ModeSnapshot}
	checksum := strings.Repeat("a", 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/apis/batch/v1/namespaces/ns/jobs/" + jobName(b):
			_ = json.NewEncoder(w).Encode(batchv1.Job{Status: batchv1.JobStatus{Succeeded: 1}})
		case "/api/v1/namespaces/ns/pods":
			_ = json.NewEncoder(w).Encode(corev1.PodList{Items: []corev1.Pod{
				{ObjectMeta: metav1.ObjectMeta{Name: "failed"}, Status: corev1.PodStatus{Phase: corev1.PodFailed}},
				{ObjectMeta: metav1.ObjectMeta{Name: "succeeded"}, Status: corev1.PodStatus{Phase: corev1.PodSucceeded}},
			}})
		case "/api/v1/namespaces/ns/pods/succeeded/log":
			_, _ = fmt.Fprintf(w, "BK_META id=%s size=1024 sha256=%s\n", b.ID, checksum)
		default:
			t.Errorf("不应读取失败 Pod: %s", r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	}))
	defer srv.Close()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	got, err := (K8sExecutor{Clientset: client}).Probe(context.Background(), b, Target{Namespace: "ns"})
	if err != nil || got.Checksum != checksum || got.SizeBytes != 1024 {
		t.Fatalf("错误读取重试日志: %+v %v", got, err)
	}
}
