package hami

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"time"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const schedulerTLSRolloutAnnotation = "neutree.ai/hami-tls-restarted-at"

type certificateBundle struct {
	CertPEM  []byte
	KeyPEM   []byte
	CAPEM    []byte
	NotAfter time.Time
}

// EnsureTLS creates or renews the HAMi scheduler serving certificate Secret.
// It returns true when the Secret changed and the scheduler should reload it.
func (h *HAMiComponent) EnsureTLS(ctx context.Context) (bool, error) {
	secret := &corev1.Secret{}
	err := h.ctrlClient.Get(ctx, types.NamespacedName{Name: TLSSecretName, Namespace: h.namespace}, secret)
	if err == nil && !servingCertificateNeedsRenewal(secret, time.Now()) {
		return false, nil
	}
	secretNotFound := apierrors.IsNotFound(err)
	if err != nil && !secretNotFound {
		return false, errors.Wrap(err, "failed to get HAMi TLS secret")
	}

	bundle, err := generateTLSBundle(h.namespace, time.Now())
	if err != nil {
		return false, errors.Wrap(err, "failed to generate HAMi TLS bundle")
	}

	if secretNotFound {
		secret = &corev1.Secret{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Secret",
				APIVersion: "v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      TLSSecretName,
				Namespace: h.namespace,
			},
		}
	}

	secret.Type = corev1.SecretTypeTLS
	secret.Data = map[string][]byte{
		corev1.TLSCertKey:       bundle.CertPEM,
		corev1.TLSPrivateKeyKey: bundle.KeyPEM,
		"ca.crt":                bundle.CAPEM,
	}

	if secretNotFound {
		if err := h.ctrlClient.Create(ctx, secret); err != nil {
			return false, errors.Wrap(err, "failed to create HAMi TLS secret")
		}
		return true, nil
	}

	if err := h.ctrlClient.Update(ctx, secret); err != nil {
		return false, errors.Wrap(err, "failed to update HAMi TLS secret")
	}

	return true, nil
}

func (h *HAMiComponent) schedulerDeploymentExists(ctx context.Context) (bool, error) {
	deployment := &appsv1.Deployment{}
	err := h.ctrlClient.Get(ctx, types.NamespacedName{Name: SchedulerName, Namespace: h.namespace}, deployment)
	if err == nil {
		return true, nil
	}
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return false, errors.Wrap(err, "failed to get HAMi scheduler deployment")
}

func (h *HAMiComponent) rolloutScheduler(ctx context.Context) error {
	deployment := &appsv1.Deployment{}
	err := h.ctrlClient.Get(ctx, types.NamespacedName{Name: SchedulerName, Namespace: h.namespace}, deployment)
	if err != nil {
		return clientIgnoreNotFound(err)
	}

	patch := client.MergeFrom(deployment.DeepCopy())
	if deployment.Spec.Template.Annotations == nil {
		deployment.Spec.Template.Annotations = map[string]string{}
	}
	// The scheduler reads serving certs from the Secret. Changing the Pod
	// template is the least invasive way to reload a regenerated bundle.
	deployment.Spec.Template.Annotations[schedulerTLSRolloutAnnotation] = time.Now().UTC().Format(time.RFC3339Nano)

	if err := h.ctrlClient.Patch(ctx, deployment, patch); err != nil {
		return errors.Wrap(err, "failed to rollout HAMi scheduler")
	}

	return nil
}

func (h *HAMiComponent) CleanupTLS(ctx context.Context) error {
	secret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Secret",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      TLSSecretName,
			Namespace: h.namespace,
		},
	}

	if err := h.ctrlClient.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
		return errors.Wrap(err, "failed to delete HAMi TLS secret")
	}

	return nil
}

func servingCertificateNeedsRenewal(secret *corev1.Secret, now time.Time) bool {
	if secret == nil || secret.Data == nil {
		return true
	}

	certPEM := secret.Data[corev1.TLSCertKey]
	keyPEM := secret.Data[corev1.TLSPrivateKeyKey]
	caPEM := secret.Data["ca.crt"]
	if len(certPEM) == 0 || len(keyPEM) == 0 || len(caPEM) == 0 {
		return true
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		return true
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return true
	}

	return cert.NotAfter.Before(now.Add(servingRenewBefore()))
}

func generateTLSBundle(namespace string, now time.Time) (*certificateBundle, error) {
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}

	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(now.UnixNano()),
		Subject:               pkix.Name{CommonName: "neutree-hami-ca"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(CACertificateYears, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}

	serverKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}

	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano() + 1),
		Subject:      pkix.Name{CommonName: SchedulerName},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(ServingCertificateYears, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames: []string{
			SchedulerName,
			SchedulerName + "." + namespace,
			SchedulerName + "." + namespace + ".svc",
			SchedulerName + "." + namespace + ".svc.cluster.local",
		},
	}

	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}

	return &certificateBundle{
		CertPEM:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}),
		KeyPEM:   pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(serverKey)}),
		CAPEM:    pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		NotAfter: serverTemplate.NotAfter,
	}, nil
}

// PatchWebhookCABundle syncs the generated CA into HAMi's admission webhook.
// It returns true only when the MutatingWebhookConfiguration was changed.
func (h *HAMiComponent) PatchWebhookCABundle(ctx context.Context) (bool, error) {
	secret := &corev1.Secret{}
	if err := h.ctrlClient.Get(ctx, types.NamespacedName{Name: TLSSecretName, Namespace: h.namespace}, secret); err != nil {
		return false, errors.Wrap(err, "failed to get HAMi TLS secret")
	}

	webhook := &unstructured.Unstructured{}
	webhook.SetAPIVersion("admissionregistration.k8s.io/v1")
	webhook.SetKind("MutatingWebhookConfiguration")
	if err := h.ctrlClient.Get(ctx, types.NamespacedName{Name: WebhookName}, webhook); err != nil {
		return false, errors.Wrap(err, "failed to get HAMi webhook")
	}

	webhooks, found, err := unstructured.NestedSlice(webhook.Object, "webhooks")
	if err != nil {
		return false, errors.Wrap(err, "failed to read HAMi webhook list")
	}
	if !found || len(webhooks) == 0 {
		return false, errors.New("HAMi webhook has no webhooks")
	}

	desiredCABundle := base64.StdEncoding.EncodeToString(secret.Data["ca.crt"])
	changed := false
	for i := range webhooks {
		webhookItem, ok := webhooks[i].(map[string]interface{})
		if !ok {
			continue
		}
		clientConfig, ok := webhookItem["clientConfig"].(map[string]interface{})
		if !ok {
			clientConfig = map[string]interface{}{}
			webhookItem["clientConfig"] = clientConfig
		}
		if clientConfig["caBundle"] == desiredCABundle {
			continue
		}
		// admissionregistration.k8s.io stores caBundle as base64 text inside
		// the object, while Secrets hold the decoded PEM bytes.
		clientConfig["caBundle"] = desiredCABundle
		changed = true
	}
	if err := unstructured.SetNestedSlice(webhook.Object, webhooks, "webhooks"); err != nil {
		return false, errors.Wrap(err, "failed to set HAMi webhook caBundle")
	}
	if !changed {
		return false, nil
	}

	if err := h.ctrlClient.Update(ctx, webhook); err != nil {
		return false, err
	}
	return true, nil
}
