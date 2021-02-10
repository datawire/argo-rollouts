package ambassador

import (
	"context"
	"fmt"

	"github.com/argoproj/argo-rollouts/pkg/apis/rollouts/v1alpha1"
	logutil "github.com/argoproj/argo-rollouts/utils/log"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"k8s.io/client-go/tools/record"
)

// Type defines the ambassador traffic routing type.
const (
	Type                         = "Ambassador"
	AmbassadorMappingNotFound    = "AmbassadorMappingNotFound"
	AmbassadorMappingConfigError = "AmbassadorMappingConfigError"
	CanaryMappingCleanupError    = "CanaryMappingCleanupError"
	CanaryMappingCreationError   = "CanaryMappingCreationError"
	CanaryMappingUpdateError     = "CanaryMappingUpdateError"
	CanaryMappingWeightUpdate    = "CanaryMappingWeightUpdate"
)

// Reconciler implements a TrafficRoutingReconciler for Ambassador.
type Reconciler struct {
	Rollout  *v1alpha1.Rollout
	Client   ClientInterface
	Recorder record.EventRecorder
	Log      *logrus.Entry
}

// ClientInterface defines a subset of k8s client operations having only the required
// ones.
type ClientInterface interface {
	Get(ctx context.Context, name string, options metav1.GetOptions, subresources ...string) (*unstructured.Unstructured, error)
	Create(ctx context.Context, obj *unstructured.Unstructured, options metav1.CreateOptions, subresources ...string) (*unstructured.Unstructured, error)
	Update(ctx context.Context, obj *unstructured.Unstructured, options metav1.UpdateOptions, subresources ...string) (*unstructured.Unstructured, error)
	Delete(ctx context.Context, name string, options metav1.DeleteOptions, subresources ...string) error
}

// NewDynamicClient will initialize a real kubernetes dynamic client to interact
// with Ambassador CRDs
func NewDynamicClient(di dynamic.Interface, namespace string) dynamic.ResourceInterface {
	return di.Resource(GetAmbassadorGVR()).Namespace(namespace)
}

// NewReconciler will build and return an ambassador Reconciler
func NewReconciler(r *v1alpha1.Rollout, c ClientInterface, rec record.EventRecorder) *Reconciler {
	return &Reconciler{
		Rollout:  r,
		Client:   c,
		Recorder: rec,
		Log:      logutil.WithRollout(r),
	}
}

// SetWeight will configure a canary ambassador mapping with the given desiredWeight.
// The canary ambassador mapping is dynamically created cloning the mapping provided
// in the ambassador configuration in the traffic routing section of the rollout. If
// the canary ambassador mapping is already present, it will be updated to the given
// desiredWeight.
func (r *Reconciler) SetWeight(desiredWeight int32) error {
	ctx := context.TODO()
	r.sendNormalEvent(CanaryMappingWeightUpdate, fmt.Sprintf("Updating canary mapping weight to %d", desiredWeight))
	baseMappingName := r.Rollout.Spec.Strategy.Canary.TrafficRouting.Ambassador.Mapping
	canaryMappingName := buildCanaryMappingName(baseMappingName)

	canaryMapping, err := r.Client.Get(ctx, canaryMappingName, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return r.createCanaryMapping(ctx, baseMappingName, desiredWeight, r.Client)
		}
		return err
	}
	return r.updateCanaryMapping(ctx, canaryMapping, desiredWeight, r.Client)
}

func (r *Reconciler) updateCanaryMapping(ctx context.Context,
	canaryMapping *unstructured.Unstructured,
	desiredWeight int32,
	client ClientInterface) error {

	if desiredWeight == 0 {
		// when rollout concludes the canary mapping needs to be removed
		return r.deleteCanaryMapping(ctx, canaryMapping, desiredWeight, client)
	}

	setMappingWeight(canaryMapping, desiredWeight)
	_, err := client.Update(ctx, canaryMapping, metav1.UpdateOptions{})
	if err != nil {
		msg := fmt.Sprintf("Error updating canary mapping %q: %s", canaryMapping.GetName(), err)
		r.sendWarningEvent(CanaryMappingUpdateError, msg)
	}
	return err
}

func (r *Reconciler) deleteCanaryMapping(ctx context.Context,
	canaryMapping *unstructured.Unstructured,
	desiredWeight int32,
	client ClientInterface) error {

	err := client.Delete(ctx, canaryMapping.GetName(), metav1.DeleteOptions{})
	if err != nil {
		msg := fmt.Sprintf("Error deleting canary mapping %q: %s", canaryMapping.GetName(), err)
		r.sendWarningEvent(CanaryMappingCleanupError, msg)
		return err
	}
	return nil

}

func (r *Reconciler) createCanaryMapping(ctx context.Context,
	baseMappingName string,
	desiredWeight int32,
	client ClientInterface) error {

	if desiredWeight == 0 {
		return nil
	}

	baseMapping, err := client.Get(ctx, baseMappingName, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			msg := fmt.Sprintf("Ambassador mapping %q not found", baseMappingName)
			r.sendWarningEvent(AmbassadorMappingNotFound, msg)
		}
		return err
	}
	weight := GetMappingWeight(baseMapping)
	if weight != 0 {
		msg := fmt.Sprintf("Ambassador mapping %q can not define weight", baseMappingName)
		r.sendWarningEvent(AmbassadorMappingConfigError, msg)
		return fmt.Errorf(msg)
	}

	canarySvc := r.Rollout.Spec.Strategy.Canary.CanaryService
	canaryMapping := buildCanaryMapping(baseMapping, canarySvc, desiredWeight)
	_, err = client.Create(ctx, canaryMapping, metav1.CreateOptions{})
	if err != nil {
		msg := fmt.Sprintf("Error creating canary mapping: %s", err)
		r.sendWarningEvent(CanaryMappingCreationError, msg)
	}
	return err
}

func buildCanaryMapping(baseMapping *unstructured.Unstructured, canarySvc string, desiredWeight int32) *unstructured.Unstructured {
	canaryMapping := baseMapping.DeepCopy()
	unstructured.RemoveNestedField(canaryMapping.Object, "metadata")
	cMappingName := buildCanaryMappingName(baseMapping.GetName())
	canaryMapping.SetName(cMappingName)
	canaryMapping.SetNamespace(baseMapping.GetNamespace())
	unstructured.SetNestedField(canaryMapping.Object, canarySvc, "spec", "service")
	setMappingWeight(canaryMapping, desiredWeight)
	return canaryMapping
}

func (r *Reconciler) VerifyWeight(desiredWeight int32) (bool, error) {
	return true, nil
}

func (r *Reconciler) Type() string {
	return Type
}

func setMappingWeight(obj *unstructured.Unstructured, weight int32) {
	unstructured.SetNestedField(obj.Object, int64(weight), "spec", "weight")
}

func GetMappingWeight(obj *unstructured.Unstructured) int64 {
	weight, found, err := unstructured.NestedInt64(obj.Object, "spec", "weight")
	if err != nil || !found {
		return 0
	}
	return weight
}

func buildCanaryMappingName(name string) string {
	n := name
	if len(name) > 246 {
		n = name[:246]
	}
	return fmt.Sprintf("%s-canary", n)
}

func GetAmbassadorGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    "getambassador.io",
		Version:  "v2",
		Resource: "mappings",
	}
}

func (r *Reconciler) sendNormalEvent(id, msg string) {
	r.sendEvent(corev1.EventTypeNormal, id, msg)
}

func (r *Reconciler) sendWarningEvent(id, msg string) {
	r.sendEvent(corev1.EventTypeWarning, id, msg)
}

func (r *Reconciler) sendEvent(eventType, id, msg string) {
	r.Recorder.Event(r.Rollout, eventType, id, msg)
}
