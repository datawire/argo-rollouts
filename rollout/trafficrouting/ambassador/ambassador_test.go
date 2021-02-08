package ambassador_test

import (
	"context"
	"testing"

	"github.com/argoproj/argo-rollouts/pkg/apis/rollouts/v1alpha1"
	"github.com/argoproj/argo-rollouts/rollout/trafficrouting/ambassador"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer/yaml"
	"k8s.io/client-go/tools/record"
)

const (
	baseMapping = `
apiVersion: getambassador.io/v2
kind:  Mapping
metadata:
  name: myapp-mapping
  namespace: default
spec:
  host: somedomain.com
  prefix: /myapp/
  rewrite: /myapp/
  service: myapp:8080`

	baseMappingWithWeight = `
apiVersion: getambassador.io/v2
kind:  Mapping
metadata:
  name: myapp-mapping
  namespace: default
spec:
  host: somedomain.com
  prefix: /myapp/
  rewrite: /myapp/
  service: myapp:8080
  weight: 20`

	veryLongMappingName = `
apiVersion: getambassador.io/v2
kind:  Mapping
metadata:
  name: very-long-mapping-name-very-long-mapping-name-very-long-mapping-name-very-long-mapping-name-very-long-mapping-name-very-long-mapping-name-very-long-mapping-name-very-long-mapping-name-very-long-mapping-name-very-long-mapping-name-very-long-mapping-name-
  namespace: default`
)

type fakeClient struct {
	getInvokations    []*getInvokation
	getReturns        []*getReturn
	createInvokations []*createInvokation
	createReturns     []*createReturn
}

type getInvokation struct {
	name string
}

type createInvokation struct {
	obj          *unstructured.Unstructured
	options      metav1.CreateOptions
	subresources []string
}

type createReturn struct {
	obj *unstructured.Unstructured
	err error
}

type getReturn struct {
	obj *unstructured.Unstructured
	err error
}

func (f *fakeClient) Get(ctx context.Context, name string, options metav1.GetOptions, subresources ...string) (*unstructured.Unstructured, error) {
	invokation := &getInvokation{name: name}
	f.getInvokations = append(f.getInvokations, invokation)
	if len(f.getReturns) == 0 {
		return nil, nil
	}
	ret := f.getReturns[0]
	if len(f.getReturns) >= len(f.getInvokations) {
		ret = f.getReturns[len(f.getInvokations)-1]
	}
	return ret.obj, ret.err
}

func (f *fakeClient) Create(ctx context.Context, obj *unstructured.Unstructured, options metav1.CreateOptions, subresources ...string) (*unstructured.Unstructured, error) {
	invokation := &createInvokation{
		obj:          obj,
		options:      options,
		subresources: subresources,
	}
	f.createInvokations = append(f.createInvokations, invokation)
	if len(f.createReturns) == 0 {
		return nil, nil
	}
	ret := f.createReturns[0]
	if len(f.createReturns) >= len(f.createInvokations) {
		ret = f.createReturns[len(f.createInvokations)-1]
	}
	return ret.obj, ret.err
}

func (f *fakeClient) Update(ctx context.Context, obj *unstructured.Unstructured, options metav1.UpdateOptions, subresources ...string) (*unstructured.Unstructured, error) {
	return nil, nil
}

func TestReconciler_SetWeight(t *testing.T) {
	type fixture struct {
		rollout    *v1alpha1.Rollout
		fakeClient *fakeClient
		recorder   record.EventRecorder
		reconciler *ambassador.Reconciler
	}

	setup := func() *fixture {
		r := rollout("main-service", "canary-service", "myapp-mapping")
		fakeClient := &fakeClient{}
		rec := &record.FakeRecorder{}
		l, _ := test.NewNullLogger()
		return &fixture{
			rollout:    r,
			fakeClient: fakeClient,
			recorder:   rec,
			reconciler: &ambassador.Reconciler{
				Rollout:  r,
				Client:   fakeClient,
				Recorder: rec,
				Log:      l.WithContext(context.TODO()),
			},
		}
	}
	t.Run("will create canary mapping and set weight successfully", func(t *testing.T) {
		// given
		t.Parallel()
		f := setup()
		getReturns := []*getReturn{
			{err: k8serrors.NewNotFound(schema.GroupResource{}, "canary-mapping")},
			{obj: toUnstructured(t, baseMapping)},
		}
		createReturns := []*createReturn{
			{nil, nil},
		}
		f.fakeClient.getReturns = getReturns
		f.fakeClient.createReturns = createReturns

		// when
		err := f.reconciler.SetWeight(13)

		// then
		assert.NoError(t, err)
		assert.Equal(t, 2, len(f.fakeClient.getInvokations))
		assert.Equal(t, "myapp-mapping-canary", f.fakeClient.getInvokations[0].name)
		assert.Equal(t, "myapp-mapping", f.fakeClient.getInvokations[1].name)
		assert.Equal(t, 1, len(f.fakeClient.createInvokations))
		assert.Equal(t, int64(13), ambassador.GetMappingWeight(f.fakeClient.createInvokations[0].obj))
	})
	t.Run("will return error if base mapping defines the weight", func(t *testing.T) {
		// given
		t.Parallel()
		f := setup()
		getReturns := []*getReturn{
			{err: k8serrors.NewNotFound(schema.GroupResource{}, "canary-mapping")},
			{obj: toUnstructured(t, baseMappingWithWeight)},
		}
		f.fakeClient.getReturns = getReturns

		// when
		err := f.reconciler.SetWeight(20)

		// then
		assert.Error(t, err)
		assert.Equal(t, 2, len(f.fakeClient.getInvokations))
		assert.Equal(t, "myapp-mapping-canary", f.fakeClient.getInvokations[0].name)
		assert.Equal(t, "myapp-mapping", f.fakeClient.getInvokations[1].name)
		assert.Equal(t, 0, len(f.fakeClient.createInvokations))
	})
	t.Run("will return error if base mapping not found", func(t *testing.T) {
		// given
		t.Parallel()
		f := setup()
		getReturns := []*getReturn{
			{err: k8serrors.NewNotFound(schema.GroupResource{}, "canary-mapping")},
			{err: k8serrors.NewNotFound(schema.GroupResource{}, "base-mapping")},
		}
		f.fakeClient.getReturns = getReturns

		// when
		err := f.reconciler.SetWeight(20)

		// then
		assert.Error(t, err)
		assert.Equal(t, 2, len(f.fakeClient.getInvokations))
		assert.Equal(t, "myapp-mapping-canary", f.fakeClient.getInvokations[0].name)
		assert.Equal(t, "myapp-mapping", f.fakeClient.getInvokations[1].name)
		assert.Equal(t, 0, len(f.fakeClient.createInvokations))
	})
	t.Run("will respect kube resource name size when creating the canary mapping", func(t *testing.T) {
		// given
		t.Parallel()
		f := setup()
		providedMappingName := "very-long-mapping-name-very-long-mapping-name-very-long-mapping-name-very-long-mapping-name-very-long-mapping-name-very-long-mapping-name-very-long-mapping-name-very-long-mapping-name-very-long-mapping-name-very-long-mapping-name-very-long-mapping-name-"
		expectedName := "very-long-mapping-name-very-long-mapping-name-very-long-mapping-name-very-long-mapping-name-very-long-mapping-name-very-long-mapping-name-very-long-mapping-name-very-long-mapping-name-very-long-mapping-name-very-long-mapping-name-very-long-mappin-canary"
		f.rollout.Spec.Strategy.Canary.TrafficRouting.Ambassador.Mapping = providedMappingName

		getReturns := []*getReturn{
			{obj: toUnstructured(t, baseMapping)},
		}
		f.fakeClient.getReturns = getReturns

		// when
		err := f.reconciler.SetWeight(20)

		// then
		assert.NoError(t, err)
		assert.Equal(t, 1, len(f.fakeClient.getInvokations))
		assert.Equal(t, expectedName, f.fakeClient.getInvokations[0].name)
	})
}

func toUnstructured(t *testing.T, manifest string) *unstructured.Unstructured {
	t.Helper()
	obj := &unstructured.Unstructured{}

	dec := yaml.NewDecodingSerializer(unstructured.UnstructuredJSONScheme)
	_, _, err := dec.Decode([]byte(manifest), nil, obj)
	if err != nil {
		t.Fatal(err)
	}
	return obj
}

func rollout(stableSvc, canarySvc, mapping string) *v1alpha1.Rollout {
	return &v1alpha1.Rollout{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rollout",
			Namespace: "default",
		},
		Spec: v1alpha1.RolloutSpec{
			Strategy: v1alpha1.RolloutStrategy{
				Canary: &v1alpha1.CanaryStrategy{
					StableService: stableSvc,
					CanaryService: canarySvc,
					TrafficRouting: &v1alpha1.RolloutTrafficRouting{
						Ambassador: &v1alpha1.AmbassadorTrafficRouting{
							Mapping: mapping,
						},
					},
				},
			},
		},
	}
}
