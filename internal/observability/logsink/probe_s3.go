package logsink

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const s3ProbeKeyPrefix = ".connection-test-"

// testS3Connection checks that the bucket exists and accepts an object written
// with the sink's keys, the only call Vector makes. It writes a small object
// under the static part of the key prefix and deletes it again.
func testS3Connection(ctx context.Context, spec *v1.S3SinkSpec) error {
	client := newS3Client(spec)
	bucket := spec.Bucket

	headCtx, cancel := context.WithTimeout(ctx, defaultProbeTimeout)
	_, headErr := client.HeadBucket(headCtx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})

	cancel()

	// A 403 here may only mean the keys lack s3:ListBucket, which delivery
	// does not need; the write below decides.
	if headErr != nil && s3Status(headErr) != http.StatusForbidden {
		return describeS3Error(spec, "check the bucket", headErr)
	}

	key := KeyPrefixStatic(spec.GetKeyPrefix()) + s3ProbeKeyPrefix + fmt.Sprint(time.Now().UnixNano())

	putCtx, cancel := context.WithTimeout(ctx, defaultProbeTimeout)
	_, err := client.PutObject(putCtx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   strings.NewReader("log sink connection test\n"),
	})

	cancel()

	if err != nil {
		return describeS3Error(spec, "write a test object", err)
	}

	// Write-only keys (a common policy for log shipping) may not delete, which
	// is no problem for delivery; the object is then left behind.
	delCtx, cancel := context.WithTimeout(ctx, defaultProbeTimeout)
	defer cancel()

	if _, err := client.DeleteObject(delCtx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}); err != nil {
		klog.V(2).Infof("log sink connection test could not delete s3://%s/%s: %v", bucket, key, err)
	}

	return nil
}

func newS3Client(spec *v1.S3SinkSpec) *s3.Client {
	auth := spec.Auth
	if auth == nil {
		auth = &v1.S3SinkAuth{}
	}

	creds := aws.Credentials{AccessKeyID: auth.AccessKeyID, SecretAccessKey: auth.SecretAccessKey, Source: "LogSink"}

	opts := s3.Options{
		Region:       spec.Region,
		UsePathStyle: spec.UsePathStyle(),
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return creds, nil
		}),
		// Fail fast and report the first error; Vector does its own retries.
		Retryer:    aws.NopRetryer{},
		HTTPClient: &http.Client{Timeout: defaultProbeTimeout},
	}

	if spec.Endpoint != "" {
		opts.BaseEndpoint = aws.String(strings.TrimSuffix(spec.Endpoint, "/"))
	}

	return s3.New(opts)
}

// s3Status returns the HTTP status of a failed call, or 0 when the store did not answer.
func s3Status(err error) int {
	var re *smithyhttp.ResponseError
	if errors.As(err, &re) {
		return re.HTTPStatusCode()
	}

	return 0
}

func describeS3Error(spec *v1.S3SinkSpec, action string, err error) error {
	status := s3Status(err)

	code := ""

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code = apiErr.ErrorCode()
	}

	switch {
	case status == 0:
		endpoint := spec.Endpoint
		if endpoint == "" {
			endpoint = "the AWS endpoint of region " + spec.Region
		}

		cause := err

		var ue *url.Error

		switch {
		case errors.As(err, &ue):
			cause = ue.Err
		case errors.Is(err, context.DeadlineExceeded):
			cause = errors.Errorf("no answer within %s", defaultProbeTimeout)
		}

		return errors.Errorf("cannot reach %s: %v", endpoint, cause)
	case code == "InvalidAccessKeyId":
		return errors.New("access denied (403): the store does not know access_key_id")
	case code == "SignatureDoesNotMatch":
		return errors.New("access denied (403): secret_access_key does not match access_key_id")
	case status == http.StatusForbidden:
		return errors.Errorf("access denied (403): the keys are wrong or not allowed to write to bucket %q", spec.Bucket)
	case status == http.StatusNotFound || code == "NoSuchBucket":
		return errors.Errorf("bucket %q does not exist (404)", spec.Bucket)
	case status == http.StatusMovedPermanently || code == "PermanentRedirect" || code == "AuthorizationHeaderMalformed":
		return errors.Errorf("bucket %q is in another region (%d); check spec.s3.region", spec.Bucket, status)
	}

	if code != "" {
		return errors.Errorf("failed to %s: status %d %s", action, status, code)
	}

	return errors.Errorf("failed to %s: status %d", action, status)
}
