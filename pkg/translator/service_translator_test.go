package translator_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/mesosphere/dcos-edge-lb/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/mesosphere/dklb/pkg/constants"
	dklberrors "github.com/mesosphere/dklb/pkg/errors"
	"github.com/mesosphere/dklb/pkg/translator"
	edgelbmanagertestutil "github.com/mesosphere/dklb/test/util/edgelb/manager"
	servicetestutil "github.com/mesosphere/dklb/test/util/kubernetes/service"
)

func TestServiceTranslator_Translate(t *testing.T) {
	tests := []struct {
		service        *v1.Service
		poolName       string
		mockCustomizer func(manager *edgelbmanagertestutil.MockEdgeLBManager)
		options        translator.ServiceTranslationOptions
		expectedError  error
	}{
		// Tests that a pool is created whenever it doesn't exist and the pool creation strategy is set of "IfNotPresent".
		{
			service:  servicetestutil.DummyServiceResource("foo", "bar"),
			poolName: "foo",
			mockCustomizer: func(manager *edgelbmanagertestutil.MockEdgeLBManager) {
				manager.On("GetPoolByName", mock.Anything, "foo").Return(nil, dklberrors.NotFound(errors.New("not found")))
				manager.On("CreatePool", mock.Anything, mock.Anything).Return(&models.V2Pool{}, nil)
			},
			options: translator.ServiceTranslationOptions{
				BaseTranslationOptions: translator.BaseTranslationOptions{
					EdgeLBPoolName:             "foo",
					EdgeLBPoolCreationStrategy: constants.EdgeLBPoolCreationStrategyIfNotPresent,
				},
			},
			expectedError: nil,
		},
		// Tests that a pool is NOT created whenever it doesn't exist and the pool creation strategy is set to "Never".
		{
			service:  servicetestutil.DummyServiceResource("foo", "bar"),
			poolName: "foo",
			mockCustomizer: func(manager *edgelbmanagertestutil.MockEdgeLBManager) {
				manager.On("GetPoolByName", mock.Anything, "foo").Return(nil, dklberrors.NotFound(errors.New("not found")))
			},
			options: translator.ServiceTranslationOptions{
				BaseTranslationOptions: translator.BaseTranslationOptions{
					EdgeLBPoolName:             "foo",
					EdgeLBPoolCreationStrategy: constants.EdgeLBPoolCreationStrategyNever,
				},
			},
			expectedError: fmt.Errorf("edgelb pool %q targeted by service %q does not exist, but the pool creation strategy is %q", "foo", "foo/bar", constants.EdgeLBPoolCreationStrategyNever),
		},
		// Tests that a pool is NOT created whenever it doesn't exist, the target Service resource has a non-empty status field and the pool creation strategy is set of "Once".
		{
			service: servicetestutil.DummyServiceResource("foo", "bar", func(service *v1.Service) {
				service.Status.LoadBalancer.Ingress = []v1.LoadBalancerIngress{
					{
						IP: "1.2.3.4",
					},
				}
			}),
			poolName: "foo",
			mockCustomizer: func(manager *edgelbmanagertestutil.MockEdgeLBManager) {
				manager.On("GetPoolByName", mock.Anything, "foo").Return(nil, dklberrors.NotFound(errors.New("not found")))
			},
			options: translator.ServiceTranslationOptions{
				BaseTranslationOptions: translator.BaseTranslationOptions{
					EdgeLBPoolName:             "foo",
					EdgeLBPoolCreationStrategy: constants.EdgeLBPoolCreationStrategyOnce,
				},
			},
			expectedError: fmt.Errorf("edgelb pool %q targeted by service %q has probably been manually deleted, and the pool creation strategy is %q", "foo", "foo/bar", constants.EdgeLBPoolCreationStrategyOnce),
		},
		// Tests that a pool is updated whenever it exists but is not in sync with the target Service resource.
		{
			service: servicetestutil.DummyServiceResource("foo", "bar", func(service *v1.Service) {
				service.Spec.Ports = []v1.ServicePort{
					{
						Name:       "http-1",
						NodePort:   34567,
						Protocol:   v1.ProtocolTCP,
						Port:       80,
						TargetPort: intstr.FromInt(8000),
					},
				}
			}),
			poolName: "foo",
			mockCustomizer: func(manager *edgelbmanagertestutil.MockEdgeLBManager) {
				manager.On("GetPoolByName", mock.Anything, "foo").Return(&models.V2Pool{
					Name:    "foo",
					Haproxy: &models.V2Haproxy{},
				}, nil)
				manager.On("UpdatePool", mock.Anything, mock.Anything).Return(&models.V2Pool{}, nil)
			},
			options: translator.ServiceTranslationOptions{
				BaseTranslationOptions: translator.BaseTranslationOptions{
					EdgeLBPoolName: "foo",
				},
			},
			expectedError: nil,
		},
	}
	for _, test := range tests {
		m := new(edgelbmanagertestutil.MockEdgeLBManager)
		test.mockCustomizer(m)
		err := translator.NewServiceTranslator(test.service, test.options, m).Translate()
		if test.expectedError != nil {
			assert.Equal(t, test.expectedError, err)
		} else {
			assert.NoError(t, err)
			m.AssertExpectations(t)
		}
	}
}
