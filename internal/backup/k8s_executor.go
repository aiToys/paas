package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// K8sExecutor 是 Executor 的 K8s 实现：快照/恢复下沉为租户 ns 内的一次性 Job。
//
// 快照：postgres client 镜像跑 pg_dump -Fc 落平台 PVC（paas-backups），容器内算
// sha256 并把产物元数据 echo 成 BK_META 行（Pacer 读 Job 日志解析——builder 解析
// PAAS_DIGEST 行同款模式）。凭证经 secretKeyRef 引用数据服务 Secret，不落 Job spec。
//
// 恢复：pg_restore 到目标新实例（Job 内 pg_isready 循环等实例就绪，超时由
// activeDeadlineSeconds 兜底）。
type K8sExecutor struct {
	Clientset kubernetes.Interface
	Image     string // postgres client 镜像；空 = postgres:15-alpine（与 ds controller 默认一致）
}

// backupsPVCName 平台备份产物卷名（每个租户 ns 一个，executor 启动时 ensure）。
const backupsPVCName = "paas-backups"

const backupJobDeadline = 10 * time.Minute

func (e K8sExecutor) image() string {
	if e.Image != "" {
		return e.Image
	}
	img := "postgres:15-alpine"
	// 节点拉不到 docker.io 时与 ds controller 同款经 PAAS_IMAGE_REGISTRY 重写
	// （<registry>/library/<name>:<tag>；镜像需先 crane copy 推本地 registry）。
	if reg := os.Getenv("PAAS_IMAGE_REGISTRY"); reg != "" {
		name := img
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		return reg + "/library/" + name
	}
	return img
}

// jobName 由 backup ID 确定性派生（≤63 字符；ID 含长 ds ID 需压缩）。
// hash 而非截断：截断会撞前缀相同的 ID；Pacer 恢复扫描无状态重入仍能算出同名。
func jobName(b Backup) string {
	sum := sha256.Sum256([]byte(b.ID))
	return "bkjob-" + hex.EncodeToString(sum[:8])
}

// dumpFile 备份产物文件名（restore 时从源快照的 ArtifactRef 派生同一路径）。
func dumpFile(b Backup) string { return b.ID + ".dump" }

// ensurePVC 幂等确保备份卷存在（10Gi RWO；单副本 Job 顺序写足够，S3 留后续）。
func (e K8sExecutor) ensurePVC(ctx context.Context, ns string) error {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: backupsPVCName, Namespace: ns},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
			},
		},
	}
	_, err := e.Clientset.CoreV1().PersistentVolumeClaims(ns).Create(ctx, pvc, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("ensure backups pvc: %w", err)
	}
	return nil
}

// secretEnv 返回引用数据服务 Secret 的 env（PGUSER/PGPASSWORD/PGDATABASE）。
func secretEnv(t Target) []corev1.EnvVar {
	ref := func(key string) *corev1.SecretKeySelector {
		return &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: t.SecretName}, Key: key}
	}
	return []corev1.EnvVar{
		{Name: "PGUSER", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: ref("user")}},
		{Name: "PGPASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: ref("password")}},
		{Name: "PGDATABASE", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: ref("database")}},
	}
}

// jobScript 按 Mode 生成容器脚本。快照 dump+校验和；恢复 pg_isready 等待后 restore。
// BK_META 行是 Pacer 解析产物元数据的唯一通道（size/sha256 由容器内计算）。
func jobScript(b Backup, t Target) string {
	host := t.ServiceName // 同 ns 内 Service 短名（Job 与数据服务同 ns）
	if b.Mode == ModeRestore {
		src := dumpFile(Backup{ID: b.SourceBackupID})
		return fmt.Sprintf(`set -e
export PGHOST=%s
cd /backups
printf '%%s  %%s\n' '%s' '%s' | sha256sum -c -
until pg_isready -q -t 3; do sleep 2; done
pg_restore --exit-on-error -d "$PGDATABASE" --no-owner --no-privileges --clean --if-exists '%s'
# 对恢复后的每张用户表执行读取，失败即停止；不冒充业务数据一致性验收。
psql -X -v ON_ERROR_STOP=1 <<'VERIFY_SQL'
SELECT format('SELECT count(*) FROM %%I.%%I;', schemaname, tablename)
FROM pg_tables WHERE schemaname NOT IN ('pg_catalog', 'information_schema')
ORDER BY schemaname, tablename
\gexec
VERIFY_SQL
echo "BK_META id=%s size=0 sha256=%s"`, host, b.Checksum, src, src, b.ID, b.Checksum)
	}
	f := dumpFile(b)
	return fmt.Sprintf(`set -e
export PGHOST=%s
pg_dump -Fc -f /backups/%s
S=$(stat -c %%s /backups/%s)
C=$(sha256sum /backups/%s | cut -d' ' -f1)
echo "BK_META id=%s size=$S sha256=$C"`, host, f, f, f, b.ID)
}

// Start 创建快照/恢复 Job（幂等：已存在成功返回）。
func (e K8sExecutor) Start(ctx context.Context, b Backup, t Target) error {
	if !EngineSupported(t.Engine) {
		return fmt.Errorf("目标引擎不支持 PostgreSQL 快照/恢复")
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(b.ID) {
		return fmt.Errorf("备份标识不合法")
	}
	if b.Mode == ModeRestore {
		if b.RestoredResourceID == "" || b.RestoredResourceID == b.ResourceID || t.ServiceName != b.RestoredResourceID {
			return fmt.Errorf("恢复必须指向确定的新实例")
		}
		if !ValidChecksum(b.Checksum) || !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(b.SourceBackupID) {
			return fmt.Errorf("源快照缺少有效完整性证据")
		}
	}

	if err := e.ensurePVC(ctx, t.Namespace); err != nil {
		return err
	}
	labels := map[string]string{
		"app.kubernetes.io/managed-by": "paas-core",
		"paas.aitoys/backup":           b.ID,
	}
	backoff := int32(2)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName(b), Namespace: t.Namespace, Labels: labels},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			ActiveDeadlineSeconds:   int64Ptr(int64(backupJobDeadline.Seconds())),
			TTLSecondsAfterFinished: int32Ptr(3600), // 终态 1h 后清理（日志解析窗口内保留）
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:            "backup",
						Image:           backupImage(e, t),
						ImagePullPolicy: corev1.PullIfNotPresent,
						Command:         []string{"sh", "-c", jobScript(b, t)},
						Env:             secretEnv(t),
						VolumeMounts:    []corev1.VolumeMount{{Name: "backups", MountPath: "/backups"}},
					}},
					Volumes: []corev1.Volume{{
						Name: "backups",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: backupsPVCName},
						},
					}},
				},
			},
		},
	}
	_, err := e.Clientset.BatchV1().Jobs(t.Namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create backup job: %w", err)
	}
	return nil
}

// Probe 观察 Job 终态与产物元数据。
func (e K8sExecutor) Probe(ctx context.Context, b Backup, t Target) (ProbeResult, error) {
	job, err := e.Clientset.BatchV1().Jobs(t.Namespace).Get(ctx, jobName(b), metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ProbeResult{Phase: "pending"}, nil
		}
		return ProbeResult{}, fmt.Errorf("get backup job: %w", err)
	}
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			return ProbeResult{Phase: "failed", ErrMsg: truncate(c.Message, 200)}, nil
		}
	}
	if job.Status.Succeeded > 0 {
		size, sum := parseBKMeta(ctx, e, t, jobName(b))
		return ProbeResult{Phase: "succeeded", SizeBytes: size, Checksum: sum}, nil
	}
	return ProbeResult{Phase: "running"}, nil
}

// parseBKMeta 读 Job Pod 日志解析 BK_META 行（builder PAAS_DIGEST 同款）。
// 缺少有效元数据时由 Pacer 拒绝判定成功；重试过的 Job 必须读取成功 Pod 的日志。
func parseBKMeta(ctx context.Context, e K8sExecutor, t Target, jn string) (int64, string) {
	pods, err := e.Clientset.CoreV1().Pods(t.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "job-name=" + jn,
	})
	if err != nil || len(pods.Items) == 0 {
		return 0, ""
	}
	successful := ""
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodSucceeded {
			successful = pod.Name
			break
		}
	}
	if successful == "" {
		return 0, ""
	}
	logs, err := e.Clientset.CoreV1().Pods(t.Namespace).GetLogs(successful,
		&corev1.PodLogOptions{Container: "backup", TailLines: int64Ptr(50)}).Stream(ctx)
	if err != nil {
		return 0, ""
	}
	defer logs.Close() //nolint:errcheck // 读日志流，关闭失败无补救动作
	buf := make([]byte, 8192)
	var sb strings.Builder
	for {
		n, rerr := logs.Read(buf)
		sb.Write(buf[:n])
		if rerr != nil || n == 0 {
			break
		}
		if sb.Len() > 64*1024 {
			break
		}
	}
	var size int64
	var sum string
	for _, line := range strings.Split(sb.String(), "\n") {
		if !strings.HasPrefix(line, "BK_META ") {
			continue
		}
		for _, kv := range strings.Fields(strings.TrimPrefix(line, "BK_META ")) {
			switch {
			case strings.HasPrefix(kv, "size="):
				_, _ = fmt.Sscanf(kv, "size=%d", &size) // 解析失败保持零值（size 缺省 0 不影响语义）
			case strings.HasPrefix(kv, "sha256="):
				sum = strings.TrimPrefix(kv, "sha256=")
			}
		}
	}
	return size, sum
}

func int64Ptr(i int64) *int64 { return &i }
func int32Ptr(i int32) *int32 { return &i }

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func backupImage(e K8sExecutor, t Target) string {
	if t.Image != "" {
		return t.Image
	}
	return e.image()
}
