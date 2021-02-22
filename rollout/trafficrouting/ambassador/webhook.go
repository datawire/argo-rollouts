package ambassador

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/argoproj/argo-rollouts/pkg/apis/rollouts/v1alpha1"
	"github.com/sirupsen/logrus"
)

const (
	WebhookSecretEnvVar    = "AMBASSADOR_WEBHOOK_SECRET"
	WebhookSignatureHeader = "X-Rollout-Signature"
	WebhookTimestampHeader = "X-Rollout-Timestamp"
	AnnotationTargetURL    = "getambassador.io/webhookUrl"
	AnnotationRolloutID    = "getambassador.io/rolloutId"
)

type Webhook struct {
	secret string
	Log    *logrus.Entry
}

func NewWebhook() *Webhook {
	secret := os.Getenv(WebhookSecretEnvVar)
	return &Webhook{
		secret: secret,
	}
}

type SetWeightEvent struct {
	RolloutID     string `json:"rollout_id"`
	DesiredWeight int32  `json:"desired_weight"`
	VerifiedAt    string `json:"verified_at"`
}

func (w *Webhook) SendSetWeightEvent(desiredWeight int32, rollout *v1alpha1.Rollout) {
	if w.secret == "" {
		w.Log.Warnf("rollout webhook error: variable %q is not set", WebhookSecretEnvVar)
		return
	}
	annotations := rollout.GetAnnotations()
	if annotations == nil {
		w.Log.Warnf("rollout webhook error: no annotations found for %q", rollout.GetName())
		return
	}
	webhookURL := annotations[AnnotationTargetURL]

	now := time.Now().Format(time.RFC3339)
	event := &SetWeightEvent{
		RolloutID:     annotations[AnnotationRolloutID],
		DesiredWeight: desiredWeight,
		VerifiedAt:    now,
	}
	body, err := json.Marshal(event)
	if err != nil {
		w.Log.Warnf("rollout webhook error: error building body: %s", err)
		return
	}

	req, err := http.NewRequest(http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		w.Log.Warnf("rollout webhook error: error building request: %s", err)
		return
	}

	err = w.signRequest(req, body, now)
	if err != nil {
		w.Log.Warnf("rollout webhook error: error signing request: %s", err)
		return
	}
	c := &http.Client{
		Timeout: time.Second * 5,
	}
	_, err = c.Do(req)
	if err != nil {
		w.Log.Warnf("rollout webhook error: error sending request: %s", err)
		return
	}
}

func (w *Webhook) signRequest(req *http.Request, body []byte, timestamp string) error {
	req.Header.Add(WebhookTimestampHeader, timestamp)

	basestring := fmt.Sprintf("%s:%s", timestamp, string(body))
	hash := hmac.New(sha256.New, []byte(w.secret))

	_, err := hash.Write([]byte(basestring))
	if err != nil {
		return fmt.Errorf("error writing message signature hash: %s", err)
	}
	signature := hex.EncodeToString(hash.Sum(nil))

	req.Header.Add(WebhookSignatureHeader, signature)
	return nil
}
