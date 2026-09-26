package retention

import (
	"context"
	"errors"
	"regexp"
	"time"
)

const KubernetesLeaseName = "jandibat-backup-chain-operations"

var kubeNamespace = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$`)

// KubernetesLeaseRecord is the coordination.k8s.io/v1 Lease state required by
// the retention boundary. An API implementation must preserve resourceVersion
// and use Kubernetes conflict semantics for every Update.
type KubernetesLeaseRecord struct {
	Namespace            string
	Name                 string
	ResourceVersion      string
	HolderIdentity       string
	LeaseDurationSeconds int32
	RenewTime            time.Time
}

// KubernetesLeaseAPI is deliberately narrow: the Lease is precreated by
// homelab, so this identity never needs create/delete/list permission.
type KubernetesLeaseAPI interface {
	Get(context.Context, string, string) (KubernetesLeaseRecord, error)
	Update(context.Context, KubernetesLeaseRecord) (KubernetesLeaseRecord, error)
}

type KubernetesLease struct {
	api       KubernetesLeaseAPI
	namespace string
	now       func() time.Time
}

func NewKubernetesLease(api KubernetesLeaseAPI, namespace string, now func() time.Time) (*KubernetesLease, error) {
	if api == nil || now == nil || len(namespace) > 63 || !kubeNamespace.MatchString(namespace) {
		return nil, errors.New("invalid Kubernetes Lease adapter configuration")
	}
	return &KubernetesLease{api: api, namespace: namespace, now: now}, nil
}

func (l *KubernetesLease) read(ctx context.Context) (KubernetesLeaseRecord, error) {
	if l == nil || l.api == nil {
		return KubernetesLeaseRecord{}, errors.New("kubernetes Lease adapter unavailable")
	}
	if err := ctx.Err(); err != nil {
		return KubernetesLeaseRecord{}, err
	}
	record, err := l.api.Get(ctx, l.namespace, KubernetesLeaseName)
	if err != nil {
		return KubernetesLeaseRecord{}, errors.New("kubernetes Lease read failed")
	}
	if record.Namespace != l.namespace || record.Name != KubernetesLeaseName || record.ResourceVersion == "" {
		return KubernetesLeaseRecord{}, errors.New("kubernetes Lease identity mismatch")
	}
	return record, nil
}

func (l *KubernetesLease) update(ctx context.Context, record KubernetesLeaseRecord, previousExpiresAt time.Time) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	result, err := l.api.Update(ctx, record)
	if err != nil {
		return "", errors.New("kubernetes Lease CAS update failed")
	}
	if result.Namespace != record.Namespace || result.Name != record.Name || result.ResourceVersion == "" || result.ResourceVersion == record.ResourceVersion || result.HolderIdentity != record.HolderIdentity || result.LeaseDurationSeconds != record.LeaseDurationSeconds || !result.RenewTime.Equal(record.RenewTime) {
		return "", errors.New("kubernetes Lease CAS response mismatch")
	}
	// A successful HTTP response can arrive after the holder's 30-second
	// window, or after cancellation. Never report that ambiguous state live.
	if err := ctx.Err(); err != nil {
		return "", errors.New("kubernetes Lease CAS outcome uncertain after cancellation")
	}
	now := l.now()
	if now.IsZero() || now.Before(record.RenewTime) || !now.Before(record.RenewTime.Add(leaseDuration)) {
		return "", errors.New("kubernetes Lease CAS response arrived after expiry")
	}
	if !previousExpiresAt.IsZero() && !now.Before(previousExpiresAt) {
		return "", errors.New("kubernetes Lease prior holder expired during CAS update")
	}
	return result.ResourceVersion, nil
}

// Acquire never takes over any nonempty holder, even after its TTL elapsed.
// Expiry is a fault signal that requires separate operator recovery.
func (l *KubernetesLease) Acquire(ctx context.Context, holder string, duration time.Duration) (string, error) {
	if ctx == nil || holder == "" || len(holder) > 255 || duration != leaseDuration {
		return "", errors.New("invalid Kubernetes Lease acquisition")
	}
	record, err := l.read(ctx)
	if err != nil {
		return "", err
	}
	if record.HolderIdentity != "" {
		return "", errors.New("kubernetes Lease is held; operator recovery required")
	}
	now := l.now()
	if now.IsZero() {
		return "", errors.New("kubernetes Lease clock unavailable")
	}
	record.HolderIdentity = holder
	record.LeaseDurationSeconds = int32(leaseDuration / time.Second)
	record.RenewTime = now.UTC()
	return l.update(ctx, record, time.Time{})
}

func (l *KubernetesLease) ownCurrent(ctx context.Context, holder, resourceVersion string) (KubernetesLeaseRecord, time.Time, error) {
	if ctx == nil || holder == "" || resourceVersion == "" {
		return KubernetesLeaseRecord{}, time.Time{}, errors.New("invalid Kubernetes Lease holder token")
	}
	record, err := l.read(ctx)
	if err != nil {
		return KubernetesLeaseRecord{}, time.Time{}, err
	}
	if record.HolderIdentity != holder || record.ResourceVersion != resourceVersion || record.LeaseDurationSeconds != int32(leaseDuration/time.Second) || record.RenewTime.IsZero() {
		return KubernetesLeaseRecord{}, time.Time{}, errors.New("kubernetes Lease holder or resourceVersion changed")
	}
	now := l.now()
	if now.IsZero() || now.Before(record.RenewTime) || !now.Before(record.RenewTime.Add(leaseDuration)) {
		return KubernetesLeaseRecord{}, time.Time{}, errors.New("kubernetes Lease expired; operator recovery required")
	}
	return record, now, nil
}

func (l *KubernetesLease) Renew(ctx context.Context, holder, resourceVersion string) (string, error) {
	record, checkedAt, err := l.ownCurrent(ctx, holder, resourceVersion)
	if err != nil {
		return "", err
	}
	previousExpiresAt := record.RenewTime.Add(leaseDuration)
	record.RenewTime = checkedAt.UTC()
	return l.update(ctx, record, previousExpiresAt)
}

func (l *KubernetesLease) Release(ctx context.Context, holder, resourceVersion string) error {
	record, _, err := l.ownCurrent(ctx, holder, resourceVersion)
	if err != nil {
		return err
	}
	previousExpiresAt := record.RenewTime.Add(leaseDuration)
	record.HolderIdentity = ""
	_, err = l.update(ctx, record, previousExpiresAt)
	return err
}
