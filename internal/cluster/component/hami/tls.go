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
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/neutree-ai/neutree/internal/util"
)

type certificateBundle struct {
	CertPEM  []byte
	KeyPEM   []byte
	CAPEM    []byte
	NotAfter time.Time
}

func (h *HAMiComponent) EnsureTLS(ctx context.Context) error {
	secret := &corev1.Secret{}
	err := h.ctrlClient.Get(ctx, types.NamespacedName{Name: TLSSecretName, Namespace: h.namespace}, secret)
	if err == nil && !servingCertificateNeedsRenewal(secret, time.Now()) {
		return nil
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return errors.Wrap(err, "failed to get HAMi TLS secret")
	}

	bundle, err := generateTLSBundle(h.namespace, time.Now())
	if err != nil {
		return errors.Wrap(err, "failed to generate HAMi TLS bundle")
	}

	secret = &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Secret",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      TLSSecretName,
			Namespace: h.namespace,
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			corev1.TLSCertKey:       bundle.CertPEM,
			corev1.TLSPrivateKeyKey: bundle.KeyPEM,
			"ca.crt":                bundle.CAPEM,
		},
	}

	if err := util.CreateOrPatch(ctx, secret, h.ctrlClient); err != nil {
		return errors.Wrap(err, "failed to apply HAMi TLS secret")
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

func (h *HAMiComponent) PatchWebhookCABundle(ctx context.Context) error {
	secret := &corev1.Secret{}
	if err := h.ctrlClient.Get(ctx, types.NamespacedName{Name: TLSSecretName, Namespace: h.namespace}, secret); err != nil {
		return errors.Wrap(err, "failed to get HAMi TLS secret")
	}

	webhook := &unstructured.Unstructured{}
	webhook.SetAPIVersion("admissionregistration.k8s.io/v1")
	webhook.SetKind("MutatingWebhookConfiguration")
	if err := h.ctrlClient.Get(ctx, types.NamespacedName{Name: WebhookName}, webhook); err != nil {
		return errors.Wrap(err, "failed to get HAMi webhook")
	}

	webhooks, found, err := unstructured.NestedSlice(webhook.Object, "webhooks")
	if err != nil {
		return errors.Wrap(err, "failed to read HAMi webhook list")
	}
	if !found || len(webhooks) == 0 {
		return errors.New("HAMi webhook has no webhooks")
	}

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
		clientConfig["caBundle"] = base64.StdEncoding.EncodeToString(secret.Data["ca.crt"])
	}
	if err := unstructured.SetNestedSlice(webhook.Object, webhooks, "webhooks"); err != nil {
		return errors.Wrap(err, "failed to set HAMi webhook caBundle")
	}

	return h.ctrlClient.Update(ctx, webhook)
}
